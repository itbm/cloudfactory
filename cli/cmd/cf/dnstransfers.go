package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/itbm/cloudfactory/cli/internal/client"
)

// The secondary DNS servers of the panel copy all zones from the DNS servers of the panel with
// zone transfers. Only an administrator manages them. The owner of a zone can also let their own
// secondary DNS servers copy one zone. The panel never copies a zone from a different server.

type dnsSecondaries struct {
	Data        []map[string]any `json:"data"`
	CatalogZone string           `json:"catalogZone"`
	Primaries   []struct {
		NameServer string   `json:"nameServer"`
		Addresses  []string `json:"addresses"`
	} `json:"primaries"`
	Servers []struct {
		Server string `json:"server"`
		State  string `json:"state"`
	} `json:"servers"`
}

type tsigKey struct {
	Name      string `json:"name"`
	Algorithm string `json:"algorithm"`
	Secret    string `json:"secret"`
}

// tsigSecretEnv is the environment variable for a TSIG secret that you copy from a secondary.
const tsigSecretEnv = "CF_TSIG_SECRET"

func (a *app) loadDnsSecondaries(ctx context.Context) (dnsSecondaries, error) {
	var out dnsSecondaries
	err := a.api.Do(ctx, http.MethodGet, "/dns-secondaries", nil, nil, &out)
	return out, err
}

// findDnsSecondary finds a secondary by its ID or by its name.
func (a *app) findDnsSecondary(ctx context.Context, ref string) (map[string]any, error) {
	all, err := a.loadDnsSecondaries(ctx)
	if err != nil {
		return nil, err
	}
	var found []map[string]any
	for _, s := range all.Data {
		if s["id"] == ref {
			return s, nil
		}
		if strings.EqualFold(fmt.Sprint(s["name"]), ref) {
			found = append(found, s)
		}
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("there is no secondary DNS server %s. Run: cf dns secondaries list", ref)
	case 1:
		return found[0], nil
	}
	return nil, fmt.Errorf("more than one secondary DNS server has the name %s. Give the ID", ref)
}

// strings of a JSON list.
func stringList(v any) []string {
	out := []string{}
	if list, ok := v.([]any); ok {
		for _, s := range list {
			out = append(out, fmt.Sprint(s))
		}
	}
	return out
}

// printKey shows a TSIG key with its secret, and the same key for BIND.
func (a *app) printKey(key tsigKey) error {
	if a.json {
		return a.printJSON(key)
	}
	fmt.Fprintf(a.out, "Key name:   %s\nAlgorithm:  %s\nSecret:     %s\n\nFor BIND:\nkey \"%s\" { algorithm %s; secret \"%s\"; };\n",
		key.Name, key.Algorithm, key.Secret, key.Name, key.Algorithm, key.Secret)
	return nil
}

// ownSecret reads a TSIG secret that you copy from a secondary DNS service: from CF_TSIG_SECRET
// or stdin, never from an argument.
func (a *app) ownSecret(own bool) (any, error) {
	if !own {
		return nil, nil
	}
	return a.secret(tsigSecretEnv, "TSIG secret (base64): ")
}

// dnsSecondariesCommand manages the secondary DNS servers of the panel.
func (a *app) dnsSecondariesCommand(ctx context.Context, args []string) error {
	sub := "list"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "list", "ls":
		all, err := a.loadDnsSecondaries(ctx)
		if err != nil {
			return err
		}
		if a.json {
			return a.printJSON(all)
		}
		for _, s := range all.Data {
			s["addresses"] = strings.Join(stringList(s["addresses"]), ", ")
			s["key"] = "none"
			if k, ok := s["tsig"].(map[string]any); ok {
				s["key"] = fmt.Sprintf("%v (%v)", k["name"], k["algorithm"])
			}
			s["copies"] = "all zones"
			if s["catalog"] == true {
				s["copies"] = "all zones and the catalog zone"
			}
		}
		a.table(all.Data, []string{"name", "addresses", "nameServer", "key", "notify", "copies", "id"})
		primaries := []string{}
		for _, p := range all.Primaries {
			primaries = append(primaries, fmt.Sprintf("%s (%s)", p.NameServer, strings.Join(p.Addresses, ", ")))
		}
		if len(primaries) == 0 {
			fmt.Fprintln(a.out, "The panel has no DNS server.")
		} else {
			fmt.Fprintln(a.out, "Copy the zones from: "+strings.Join(primaries, "; "))
		}
		fmt.Fprintf(a.out, "Catalog zone: %s\n", all.CatalogZone)
		states := []string{}
		for _, s := range all.Servers {
			states = append(states, s.Server+" "+map[string]string{"ok": "has the list", "pending": "waits for the list", "failed": "could not take the list"}[s.State])
		}
		if len(states) > 0 {
			fmt.Fprintln(a.out, "DNS servers: "+strings.Join(states, ", "))
		}
		return nil
	case "add":
		if err := need(args, 1, "a name, for example: cf dns secondaries add \"Backup DNS\" --address 192.0.2.53"); err != nil {
			return err
		}
		fs := flag.NewFlagSet("add", flag.ContinueOnError)
		var addresses multi
		fs.Var(&addresses, "address", "an IP address of the secondary. You can give it more than one time.")
		nameServer := fs.String("name-server", "", "the host name of the secondary. Each zone gets an NS record for it.")
		notify := fs.Bool("notify", true, "send NOTIFY to the secondary after each change")
		catalog := fs.Bool("catalog", false, "also let the secondary copy the catalog zone, so that it finds each new zone itself")
		tsig := fs.Bool("tsig", true, "sign the transfers with a TSIG key")
		algorithm := fs.String("algorithm", "hmac-sha256", "the algorithm of the TSIG key: hmac-sha256 or hmac-sha512")
		keyName := fs.String("key-name", "", "the name of the TSIG key. With none, the panel makes a name.")
		own := fs.Bool("own-secret", false, "use the secret of the secondary DNS service, from "+tsigSecretEnv+" or stdin. With none, the panel makes the secret.")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if len(addresses) == 0 {
			return errors.New("give the addresses of the secondary with --address")
		}
		body := map[string]any{"name": args[0], "addresses": []string(addresses), "notify": *notify, "catalog": *catalog, "tsig": *tsig}
		if *nameServer != "" {
			body["nameServer"] = strings.ToLower(strings.TrimSuffix(*nameServer, "."))
		}
		if *tsig {
			body["algorithm"] = *algorithm
			if *keyName != "" {
				body["keyName"] = *keyName
			}
			secret, err := a.ownSecret(*own)
			if err != nil {
				return err
			}
			body["secret"] = secret
		}
		return a.changeSecondary(ctx, http.MethodPost, "/dns-secondaries", body, "Added")
	case "set":
		if err := need(args, 1, "a secondary DNS server"); err != nil {
			return err
		}
		fs := flag.NewFlagSet("set", flag.ContinueOnError)
		var addresses multi
		fs.Var(&addresses, "address", "an IP address of the secondary. The addresses replace all addresses.")
		name := fs.String("name", "", "a new name")
		nameServer := fs.String("name-server", "", "the host name of the secondary")
		noNameServer := fs.Bool("no-name-server", false, "remove the host name. The zones lose the NS record for it.")
		notify := fs.Bool("notify", true, "send NOTIFY to the secondary after each change")
		catalog := fs.Bool("catalog", false, "also let the secondary copy the catalog zone")
		tsig := fs.Bool("tsig", true, "sign the transfers with a TSIG key")
		algorithm := fs.String("algorithm", "", "a new algorithm of the TSIG key. The key gets a new secret.")
		keyName := fs.String("key-name", "", "a new name of the TSIG key")
		own := fs.Bool("own-secret", false, "use the secret of the secondary DNS service, from "+tsigSecretEnv+" or stdin")
		newSecret := fs.Bool("new-secret", false, "make a new secret. Then give the new secret to the secondary.")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *noNameServer && *nameServer != "" {
			return errors.New("give --name-server or --no-name-server, not both")
		}
		s, err := a.findDnsSecondary(ctx, args[0])
		if err != nil {
			return err
		}
		body := map[string]any{}
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "name":
				body["name"] = *name
			case "address":
				body["addresses"] = []string(addresses)
			case "name-server":
				body["nameServer"] = strings.ToLower(strings.TrimSuffix(*nameServer, "."))
			case "no-name-server":
				body["nameServer"] = nil
			case "notify":
				body["notify"] = *notify
			case "catalog":
				body["catalog"] = *catalog
			case "tsig":
				body["tsig"] = *tsig
			case "algorithm":
				body["algorithm"] = *algorithm
			case "key-name":
				body["keyName"] = *keyName
			case "new-secret":
				body["newSecret"] = *newSecret
			}
		})
		if *own {
			secret, err := a.ownSecret(true)
			if err != nil {
				return err
			}
			body["secret"] = secret
		}
		if len(body) == 0 {
			return errors.New("give a change, for example --address or --new-secret")
		}
		return a.changeSecondary(ctx, http.MethodPatch, "/dns-secondaries/"+url.PathEscape(fmt.Sprint(s["id"])), body, "Changed")
	case "remove", "rm":
		if err := need(args, 1, "a secondary DNS server"); err != nil {
			return err
		}
		s, err := a.findDnsSecondary(ctx, args[0])
		if err != nil {
			return err
		}
		if s["nameServer"] != nil && !a.json {
			fmt.Fprintf(a.out, "Each zone loses the NS record for %s. First remove it at the registrar of each domain.\n", cell(s["nameServer"]))
		}
		if err := a.changeZone(ctx, http.MethodDelete, "/dns-secondaries/"+url.PathEscape(fmt.Sprint(s["id"])), nil); err != nil {
			return err
		}
		if !a.json {
			fmt.Fprintf(a.out, "Removed the secondary DNS server %s. It cannot copy the zones now.\n", cell(s["name"]))
		}
		return nil
	case "secret":
		if err := need(args, 1, "a secondary DNS server"); err != nil {
			return err
		}
		s, err := a.findDnsSecondary(ctx, args[0])
		if err != nil {
			return err
		}
		var key tsigKey
		if err := a.api.Do(ctx, http.MethodGet, "/dns-secondaries/"+url.PathEscape(fmt.Sprint(s["id"]))+"/secret", nil, nil, &key); err != nil {
			return err
		}
		return a.printKey(key)
	}
	return fmt.Errorf("unknown command dns secondaries %s", sub)
}

// changeSecondary sends a change of a secondary, shows the result, and waits for the job of
// each DNS server.
func (a *app) changeSecondary(ctx context.Context, method, path string, body map[string]any, verb string) error {
	var out struct {
		Secondary map[string]any `json:"secondary"`
		Jobs      []client.Job   `json:"jobs"`
	}
	if err := a.api.Do(ctx, method, path, nil, body, &out); err != nil {
		return err
	}
	if a.json {
		return a.printJSON(out)
	}
	s := out.Secondary
	fmt.Fprintf(a.out, "%s the secondary DNS server %s: %s.\n", verb, cell(s["name"]), cell(s["id"]))
	if k, ok := s["tsig"].(map[string]any); ok && body["secret"] == nil && (method == http.MethodPost || body["newSecret"] == true || body["algorithm"] != nil) {
		fmt.Fprintf(a.out, "Give the TSIG key %s to the secondary. Show it with: cf dns secondaries secret %s\n", cell(k["name"]), cell(s["id"]))
	}
	var errs []error
	for _, job := range out.Jobs {
		if a.noWait {
			fmt.Fprintf(a.out, "Job %s is in the queue: %s\n", job.ID, job.Title)
			continue
		}
		errs = append(errs, a.wait(ctx, job.ID))
	}
	return errors.Join(errs...)
}

// printTransfers shows the zone transfers of a zone to the secondary DNS servers of its owner.
func (a *app) printTransfers(zone map[string]any) {
	t, ok := zone["transfers"].(map[string]any)
	if !ok {
		return
	}
	addresses := stringList(t["addresses"])
	if len(addresses) == 0 {
		fmt.Fprintln(a.out, "Transfers:     off. Your own secondary DNS servers can copy this zone: cf dns transfers "+cell(zone["name"])+" set --address IP")
		return
	}
	notify := "no NOTIFY"
	if t["notify"] == true {
		notify = "NOTIFY after each change"
	}
	fmt.Fprintf(a.out, "Transfers to:  %s (%s)\n", strings.Join(addresses, ", "), notify)
	if k, ok := t["tsig"].(map[string]any); ok {
		fmt.Fprintf(a.out, "TSIG key:      %s (%s). Show the secret with: cf dns transfers %s secret\n", cell(k["name"]), cell(k["algorithm"]), cell(zone["name"]))
	} else {
		fmt.Fprintln(a.out, "TSIG key:      none. Only the addresses limit the transfers.")
	}
	if ns := stringList(t["nameServers"]); len(ns) > 0 {
		fmt.Fprintf(a.out, "Their NS:      %s. Give them to the registrar too.\n", strings.Join(ns, ", "))
	}
	fmt.Fprintf(a.out, "Copy from:     %s\n", strings.Join(stringList(t["primaries"]), ", "))
}

// dnsTransfers shows or changes the zone transfers of one zone.
func (a *app) dnsTransfers(ctx context.Context, zone map[string]any, path string, args []string) error {
	t, ok := zone["transfers"].(map[string]any)
	if !ok {
		return errors.New("the zone is at an external DNS provider. Zone transfers are only for a zone on the DNS servers of the panel")
	}
	sub := "get"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "get", "show":
		if a.json {
			return a.printJSON(t)
		}
		a.printTransfers(zone)
		return nil
	case "set":
		fs := flag.NewFlagSet("set", flag.ContinueOnError)
		var addresses, nameServers multi
		fs.Var(&addresses, "address", "a public IP address of your secondary DNS server. The addresses replace all addresses.")
		fs.Var(&nameServers, "name-server", "the host name of your secondary. The zone gets an NS record for it. The names replace all names.")
		noNameServers := fs.Bool("no-name-servers", false, "remove the NS records of your secondaries")
		notify := fs.Bool("notify", true, "send NOTIFY to your secondaries after each change")
		tsig := fs.Bool("tsig", true, "sign the transfers with a TSIG key")
		algorithm := fs.String("algorithm", "", "the algorithm of the TSIG key: hmac-sha256 or hmac-sha512")
		own := fs.Bool("own-secret", false, "use the secret of your secondary DNS service, from "+tsigSecretEnv+" or stdin")
		newSecret := fs.Bool("new-secret", false, "make a new secret. Then give the new secret to your secondaries.")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if *noNameServers && len(nameServers) > 0 {
			return errors.New("give --name-server or --no-name-servers, not both")
		}
		// A flag that you do not give keeps the current value.
		body := map[string]any{
			"addresses":   stringList(t["addresses"]),
			"notify":      t["notify"] == true || len(stringList(t["addresses"])) == 0,
			"nameServers": stringList(t["nameServers"]),
			"tsig":        t["tsig"] != nil || len(stringList(t["addresses"])) == 0,
		}
		if k, ok := t["tsig"].(map[string]any); ok {
			body["algorithm"] = k["algorithm"]
		}
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "address":
				body["addresses"] = []string(addresses)
			case "name-server":
				names := []string{}
				for _, n := range nameServers {
					names = append(names, strings.ToLower(strings.TrimSuffix(n, ".")))
				}
				body["nameServers"] = names
			case "no-name-servers":
				body["nameServers"] = []string{}
			case "notify":
				body["notify"] = *notify
			case "tsig":
				body["tsig"] = *tsig
			case "algorithm":
				body["algorithm"] = *algorithm
			case "new-secret":
				body["newSecret"] = *newSecret
			}
		})
		if len(body["addresses"].([]string)) == 0 {
			return errors.New("give the addresses of your secondary DNS servers with --address")
		}
		if *own {
			secret, err := a.ownSecret(true)
			if err != nil {
				return err
			}
			body["secret"] = secret
		}
		return a.changeZone(ctx, http.MethodPut, path+"/transfers", body)
	case "stop":
		if ns := stringList(t["nameServers"]); len(ns) > 0 && !a.json {
			fmt.Fprintf(a.out, "The zone loses the NS records for %s. First remove them at the registrar.\n", strings.Join(ns, ", "))
		}
		return a.changeZone(ctx, http.MethodDelete, path+"/transfers", nil)
	case "secret":
		var key tsigKey
		if err := a.api.Do(ctx, http.MethodGet, path+"/transfers/secret", nil, nil, &key); err != nil {
			return err
		}
		return a.printKey(key)
	}
	return fmt.Errorf("unknown command dns transfers ZONE %s", sub)
}
