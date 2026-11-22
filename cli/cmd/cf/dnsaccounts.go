package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"unicode"
)

// dnsProvider is a DNS provider of a plugin, for example Cloudflare.
type dnsProvider struct {
	PluginID    string `json:"pluginId"`
	PluginName  string `json:"pluginName"`
	ID          string `json:"id"`
	Name        string `json:"name"`
	DNSSEC      bool   `json:"dnssec"`
	Credentials []struct {
		ID       string `json:"id"`
		Label    string `json:"label"`
		Secret   bool   `json:"secret"`
		Optional bool   `json:"optional"`
	} `json:"credentials"`
}

type dnsAccounts struct {
	Data      []map[string]any `json:"data"`
	Providers []dnsProvider    `json:"providers"`
}

func (a *app) loadDnsAccounts(ctx context.Context) (dnsAccounts, error) {
	var out dnsAccounts
	err := a.api.Do(ctx, http.MethodGet, "/dns-accounts", nil, nil, &out)
	return out, err
}

// findDnsAccount finds an account by its ID or by its name.
func (a *app) findDnsAccount(ctx context.Context, ref string) (map[string]any, dnsAccounts, error) {
	all, err := a.loadDnsAccounts(ctx)
	if err != nil {
		return nil, all, err
	}
	var found []map[string]any
	for _, acc := range all.Data {
		if acc["id"] == ref {
			return acc, all, nil
		}
		if strings.EqualFold(fmt.Sprint(acc["name"]), ref) {
			found = append(found, acc)
		}
	}
	switch len(found) {
	case 0:
		return nil, all, fmt.Errorf("there is no DNS account %s. Run: cf dns accounts list", ref)
	case 1:
		return found[0], all, nil
	}
	return nil, all, fmt.Errorf("more than one DNS account has the name %s. Give the ID", ref)
}

// secretEnv is the environment variable for a secret field: apiToken is CF_API_TOKEN.
func secretEnv(id string) string {
	var b strings.Builder
	for i, r := range id {
		if i > 0 && unicode.IsUpper(r) {
			b.WriteByte('_')
		}
		b.WriteRune(unicode.ToUpper(r))
	}
	return "CF_" + b.String()
}

// dnsCredentials makes the credentials of an account: --set FIELD=VALUE for the fields that are
// not secret, and the environment or stdin for each secret field. With keep, a secret field that
// has no value keeps its current value in the panel.
func (a *app) dnsCredentials(p dnsProvider, set []string, keep bool) (map[string]string, error) {
	out := map[string]string{}
	known := map[string]bool{}
	for _, f := range p.Credentials {
		known[f.ID] = !f.Secret
	}
	for _, kv := range set {
		key, value, ok := strings.Cut(kv, "=")
		if !ok {
			return nil, fmt.Errorf("give --set FIELD=VALUE, not %q", kv)
		}
		plain, exists := known[key]
		if !exists {
			return nil, fmt.Errorf("%s has no field %q", p.Name, key)
		}
		if !plain {
			return nil, fmt.Errorf("%s is a secret. Give it in %s or on stdin, not as an argument", key, secretEnv(key))
		}
		out[key] = value
	}
	for _, f := range p.Credentials {
		if !f.Secret {
			continue
		}
		if keep || f.Optional {
			// Only the environment: an empty value keeps the current secret, or gives none.
			if v := os.Getenv(secretEnv(f.ID)); v != "" {
				out[f.ID] = v
			}
			continue
		}
		v, err := a.secret(secretEnv(f.ID), f.Label+": ")
		if err != nil {
			return nil, err
		}
		out[f.ID] = v
	}
	return out, nil
}

// dnsAccountsCommand manages the accounts at external DNS providers. A plugin offers each
// provider. A zone at a provider gets each change from the panel.
func (a *app) dnsAccountsCommand(ctx context.Context, args []string) error {
	sub := "list"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "list", "ls":
		all, err := a.loadDnsAccounts(ctx)
		if err != nil {
			return err
		}
		if a.json {
			return a.printJSON(all)
		}
		for _, acc := range all.Data {
			if acc["customerId"] == nil {
				acc["owner"] = "panel"
			} else {
				acc["owner"] = acc["customerId"]
			}
			if acc["available"] != true {
				acc["providerName"] = fmt.Sprintf("%v (plugin off)", acc["providerName"])
			}
		}
		a.table(all.Data, []string{"name", "providerName", "account", "owner", "zones", "id"})
		if len(all.Providers) == 0 {
			fmt.Fprintln(a.out, "There is no DNS provider. A plugin, for example the Cloudflare DNS plugin, gives one.")
		}
		for _, p := range all.Providers {
			fields := []string{}
			for _, f := range p.Credentials {
				switch {
				case f.Secret && f.Optional:
					fields = append(fields, f.ID+" (secret, optional: "+secretEnv(f.ID)+")")
				case f.Secret:
					fields = append(fields, f.ID+" (secret: "+secretEnv(f.ID)+" or stdin)")
				case f.Optional:
					fields = append(fields, f.ID+" (optional)")
				default:
					fields = append(fields, f.ID)
				}
			}
			fmt.Fprintf(a.out, "Provider %s: %s, from the plugin %s. Fields: %s.\n", p.ID, p.Name, p.PluginName, strings.Join(fields, ", "))
		}
		return nil
	case "add":
		if len(args) < 2 {
			return errors.New("give a provider and a name, for example: cf dns accounts add cloudflare \"Main account\"")
		}
		fs := flag.NewFlagSet("add", flag.ContinueOnError)
		var set multi
		fs.Var(&set, "set", "a field that is not secret, as FIELD=VALUE. You can give it more than one time.")
		customer := fs.String("customer", "", "the customer ID. With none, it is your customer.")
		panel := fs.Bool("panel", false, "an account of the panel, for the zones of each customer (administrators only)")
		plugin := fs.String("plugin", "", "the plugin ID, when two plugins have the provider")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		if *panel && *customer != "" {
			return errors.New("give --customer or --panel, not both")
		}
		all, err := a.loadDnsAccounts(ctx)
		if err != nil {
			return err
		}
		var provider *dnsProvider
		for i, p := range all.Providers {
			if strings.EqualFold(p.ID, args[0]) && (*plugin == "" || p.PluginID == *plugin) {
				if provider != nil {
					return fmt.Errorf("two plugins have the provider %s. Give --plugin ID", p.ID)
				}
				provider = &all.Providers[i]
			}
		}
		if provider == nil {
			return fmt.Errorf("there is no DNS provider %s. Run: cf dns accounts list", args[0])
		}
		credentials, err := a.dnsCredentials(*provider, set, false)
		if err != nil {
			return err
		}
		body := map[string]any{"name": args[1], "pluginId": provider.PluginID, "provider": provider.ID, "credentials": credentials}
		switch {
		case *panel:
			body["customerId"] = nil
		case *customer != "":
			body["customerId"] = *customer
		}
		var acc map[string]any
		if err := a.api.Do(ctx, http.MethodPost, "/dns-accounts", nil, body, &acc); err != nil {
			return err
		}
		if a.json {
			return a.printJSON(acc)
		}
		fmt.Fprintf(a.out, "Added the DNS account %s (%s, %s): %s.\n", cell(acc["name"]), provider.Name, cell(acc["account"]), cell(acc["id"]))
		fmt.Fprintf(a.out, "Make a zone there with: cf dns create DOMAIN --account %s\n", cell(acc["id"]))
		return nil
	case "set":
		if err := need(args, 1, "a DNS account"); err != nil {
			return err
		}
		fs := flag.NewFlagSet("set", flag.ContinueOnError)
		var set multi
		fs.Var(&set, "set", "a field that is not secret, as FIELD=VALUE")
		name := fs.String("name", "", "a new name")
		secrets := fs.Bool("credentials", false, "send the credentials again: the secrets come from their environment variables")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		acc, all, err := a.findDnsAccount(ctx, args[0])
		if err != nil {
			return err
		}
		body := map[string]any{}
		if *name != "" {
			body["name"] = *name
		}
		if len(set) > 0 || *secrets {
			var provider *dnsProvider
			for i, p := range all.Providers {
				if p.PluginID == acc["pluginId"] && p.ID == acc["provider"] {
					provider = &all.Providers[i]
				}
			}
			if provider == nil {
				return errors.New("the plugin of this DNS account is off")
			}
			credentials, err := a.dnsCredentials(*provider, set, true)
			if err != nil {
				return err
			}
			body["credentials"] = credentials
		}
		if len(body) == 0 {
			return errors.New("give --name, --set or --credentials")
		}
		var out map[string]any
		if err := a.api.Do(ctx, http.MethodPatch, "/dns-accounts/"+url.PathEscape(fmt.Sprint(acc["id"])), nil, body, &out); err != nil {
			return err
		}
		if a.json {
			return a.printJSON(out)
		}
		fmt.Fprintf(a.out, "Changed the DNS account %s.\n", cell(out["name"]))
		return nil
	case "remove", "rm":
		if err := need(args, 1, "a DNS account"); err != nil {
			return err
		}
		acc, _, err := a.findDnsAccount(ctx, args[0])
		if err != nil {
			return err
		}
		if err := a.api.Do(ctx, http.MethodDelete, "/dns-accounts/"+url.PathEscape(fmt.Sprint(acc["id"])), nil, nil, nil); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "Removed the DNS account %s. The zones at the provider did not change.\n", cell(acc["name"]))
		return nil
	}
	return fmt.Errorf("unknown command dns accounts %s", sub)
}

// printProvider shows the state of a zone at an external DNS provider.
func (a *app) printProvider(zone map[string]any) {
	p, ok := zone["provider"].(map[string]any)
	if !ok {
		return
	}
	state := map[any]string{"ok": "has the last change", "pending": "waits for the last change", "failed": "could not take the last change"}[p["state"]]
	fmt.Fprintf(a.out, "Provider:      %s (account %s): %s\n", cell(p["name"]), cell(p["accountName"]), state)
	if p["error"] != nil {
		fmt.Fprintf(a.out, "Error:         %s\n", cell(p["error"]))
	}
	if h, ok := p["handover"].(map[string]any); ok {
		if h["endsAt"] != nil {
			fmt.Fprintf(a.out, "Handover:      the name servers of the panel also answer until %s\n", cell(h["endsAt"]))
		} else {
			fmt.Fprintln(a.out, "Handover:      the name servers of the panel also answer until two days after the registrar has the new name servers")
		}
	}
}
