package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/itbm/cloudfactory/cli/internal/client"
)

// Email. A customer has mail domains. Each mail domain has mailboxes, and addresses that
// send their mail to other addresses: aliases and forwarders. The mail server of a server
// holds the mail of all customers on that server.

// mailDomain gets a mail domain by its name or by its ID.
func (a *app) mailDomain(ctx context.Context, ref string) (map[string]any, error) {
	rows, err := client.All[map[string]any](ctx, a.api, "/mail/domains", nil)
	if err != nil {
		return nil, err
	}
	for _, d := range rows {
		if d["id"] == ref || d["name"] == strings.ToLower(ref) {
			return d, nil
		}
	}
	return nil, fmt.Errorf("there is no mail domain %s. See: cf mail domains list", ref)
}

// splitAddress gives the two parts of an address, and the mail domain of the address.
func (a *app) splitAddress(ctx context.Context, address string) (local string, domain map[string]any, err error) {
	local, name, ok := strings.Cut(strings.ToLower(address), "@")
	if !ok || local == "" || name == "" {
		return "", nil, fmt.Errorf("%q is not an address. Give an address, for example info@example.com", address)
	}
	domain, err = a.mailDomain(ctx, name)
	return local, domain, err
}

// mailRow finds a mailbox or an alias by its address or by its ID.
func (a *app) mailRow(ctx context.Context, path, what, ref string) (map[string]any, error) {
	rows, err := client.All[map[string]any](ctx, a.api, path, nil)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if r["id"] == ref || r["address"] == strings.ToLower(ref) {
			return r, nil
		}
	}
	return nil, fmt.Errorf("there is no %s %s", what, ref)
}

// printRecords shows the DNS records of a mail domain. The last column says whether the last
// check found the record.
func (a *app) printRecords(d map[string]any) {
	records, _ := d["records"].([]any)
	if len(records) == 0 {
		fmt.Fprintln(a.out, "The DNS records are not ready. The panel makes them when the job of the domain is complete.")
		return
	}
	rows := make([]map[string]any, 0, len(records))
	for _, v := range records {
		r, _ := v.(map[string]any)
		state := "not checked"
		switch r["found"] {
		case true:
			state = "found"
		case false:
			state = "missing"
		}
		rows = append(rows, map[string]any{"purpose": r["purpose"], "type": r["type"], "name": r["name"], "value": r["value"], "dns": state})
	}
	a.table(rows, []string{"purpose", "type", "name", "value", "dns"})
}

func (a *app) mail(ctx context.Context, sub string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("give a command for mail %s; see cf help", sub)
	}
	verb, rest := args[0], args[1:]
	switch sub {
	case "domains", "domain":
		return a.mailDomains(ctx, verb, rest)
	case "mailboxes", "mailbox":
		return a.mailboxes(ctx, verb, rest)
	case "aliases", "alias":
		return a.mailAliases(ctx, verb, rest)
	}
	return fmt.Errorf("unknown command mail %s", sub)
}

func (a *app) mailDomains(ctx context.Context, verb string, args []string) error {
	switch verb {
	case "list", "ls":
		fs := flag.NewFlagSet("list", flag.ContinueOnError)
		customer := fs.String("customer", "", "show the mail domains of this customer ID")
		if err := fs.Parse(args); err != nil {
			return err
		}
		q := url.Values{}
		if *customer != "" {
			q.Set("customerId", *customer)
		}
		rows, err := client.All[map[string]any](ctx, a.api, "/mail/domains", q)
		if err != nil {
			return err
		}
		if a.json {
			return a.printJSON(rows)
		}
		for _, d := range rows {
			d["dns"] = "records missing"
			if d["dnsReady"] == true {
				d["dns"] = "ready"
			} else if d["dnsCheckedAt"] == nil {
				d["dns"] = "not checked"
			}
		}
		a.table(rows, []string{"name", "status", "dns", "mailboxes", "aliases", "mailHost", "customerId", "id"})
		return nil
	case "add", "create":
		if err := need(args, 1, "a domain"); err != nil {
			return err
		}
		fs := flag.NewFlagSet("add", flag.ContinueOnError)
		customer := fs.String("customer", "", "the customer ID. With none, it is your customer.")
		server := fs.String("server", "", "the server with the mail server. With none, the panel selects one.")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		body := map[string]any{"name": args[0]}
		if *customer != "" {
			body["customerId"] = *customer
		}
		if *server != "" {
			id, err := a.serverID(ctx, *server)
			if err != nil {
				return err
			}
			body["serverId"] = id
		}
		err := a.change(ctx, http.MethodPost, "/mail/domains", body)
		// The panel asks for a TXT record when a different customer could own the domain.
		var apiErr *client.Error
		if errors.As(err, &apiErr) && len(apiErr.Body) > 0 {
			var payload struct {
				Verification *struct{ Type, Name, Value string } `json:"verification"`
			}
			if json.Unmarshal(apiErr.Body, &payload) == nil && payload.Verification != nil {
				v := payload.Verification
				return fmt.Errorf("show that you control %s. Add this DNS record, wait some minutes, and add the domain again:\n  %s %s %q", args[0], v.Name, v.Type, v.Value)
			}
		}
		if err == nil && !a.json {
			fmt.Fprintf(a.out, "See the DNS records to add: cf mail domains get %s\n", args[0])
		}
		return err
	case "get", "check", "retry", "remove", "rm", "delete":
		if err := need(args, 1, "a mail domain"); err != nil {
			return err
		}
		d, err := a.mailDomain(ctx, args[0])
		if err != nil {
			return err
		}
		path := fmt.Sprintf("/mail/domains/%v", d["id"])
		switch verb {
		case "retry":
			return a.change(ctx, http.MethodPost, path+"/retry", nil)
		case "remove", "rm", "delete":
			return a.change(ctx, http.MethodDelete, path, nil)
		case "check":
			// The panel reads the DNS now.
			if err := a.api.Do(ctx, http.MethodPost, path+"/check", nil, nil, &d); err != nil {
				return err
			}
		}
		if a.json {
			return a.printJSON(d)
		}
		fmt.Fprintf(a.out, "Domain:     %s\nID:         %s\nStatus:     %s\nCustomer:   %s\nMail host:  %s (IMAP port 993, SMTP port 465 or 587)\nMailboxes:  %s\nAddresses:  %s\n\n",
			cell(d["name"]), cell(d["id"]), cell(d["status"]), cell(d["customerId"]), cell(d["mailHost"]), cell(d["mailboxes"]), cell(d["aliases"]))
		a.printRecords(d)
		return nil
	}
	return fmt.Errorf("unknown command mail domains %s", verb)
}

func (a *app) mailboxes(ctx context.Context, verb string, args []string) error {
	switch verb {
	case "list", "ls":
		q, err := a.mailFilter(ctx, args)
		if err != nil {
			return err
		}
		rows, err := client.All[map[string]any](ctx, a.api, "/mail/mailboxes", q)
		if err != nil {
			return err
		}
		if a.json {
			return a.printJSON(rows)
		}
		for _, m := range rows {
			m["encryption_"] = m["encryption"]
			m["use"] = byteSize(m["usedBytes"]) + " of " + byteSize(m["quotaBytes"])
			m["encryption"] = "off"
			if e, ok := m["encryption_"].(map[string]any); ok {
				m["encryption"] = "OpenPGP"
				if prints, _ := e["fingerprints"].([]any); len(prints) > 0 {
					m["encryption"] = fmt.Sprintf("OpenPGP %v", prints[0])
				}
			}
		}
		a.table(rows, []string{"address", "status", "use", "encryption", "customerId", "id"})
		return nil
	case "create", "add":
		if err := need(args, 1, "an address"); err != nil {
			return err
		}
		fs := flag.NewFlagSet("create", flag.ContinueOnError)
		quota := fs.Int64("quota-mb", 1024, "the size of the mailbox in MB")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		local, domain, err := a.splitAddress(ctx, args[0])
		if err != nil {
			return err
		}
		return a.change(ctx, http.MethodPost, "/mail/mailboxes", map[string]any{"domainId": domain["id"], "localPart": local, "quotaBytes": *quota << 20})
	case "set", "delete", "rm", "remove":
		if err := need(args, 1, "a mailbox"); err != nil {
			return err
		}
		m, err := a.mailRow(ctx, "/mail/mailboxes", "mailbox", args[0])
		if err != nil {
			return err
		}
		path := fmt.Sprintf("/mail/mailboxes/%v", m["id"])
		if verb != "set" {
			return a.change(ctx, http.MethodDelete, path, nil)
		}
		fs := flag.NewFlagSet("set", flag.ContinueOnError)
		quota := fs.Int64("quota-mb", 0, "the new size of the mailbox in MB")
		password := fs.Bool("new-password", false, "give the mailbox a new password")
		keyFile := fs.String("public-key", "", "a file with the OpenPGP public key of the owner, or - for the standard input: the mail server encrypts new mail with it")
		noKey := fs.Bool("no-encryption", false, "turn off the encryption of new mail")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		body := map[string]any{}
		if *quota > 0 {
			body["quotaBytes"] = *quota << 20
		}
		if *password {
			body["newPassword"] = true
		}
		if *keyFile != "" && *noKey {
			return errors.New("give --public-key or --no-encryption, not the two")
		}
		if *keyFile != "" {
			// "-" reads the key from the standard input: gpg --armor --export NAME | cf mail mailboxes set ADDRESS --public-key -
			var key []byte
			var err error
			if *keyFile == "-" {
				key, err = io.ReadAll(io.LimitReader(a.in, 1<<20))
			} else {
				key, err = os.ReadFile(*keyFile)
			}
			if err != nil {
				return fmt.Errorf("the public key: %w", err)
			}
			body["publicKey"] = string(key)
		}
		if *noKey {
			body["publicKey"] = nil
		}
		if len(body) == 0 {
			return errors.New("give --quota-mb, --new-password, --public-key or --no-encryption")
		}
		return a.change(ctx, http.MethodPatch, path, body)
	}
	return fmt.Errorf("unknown command mail mailboxes %s", verb)
}

// mailFilter reads --domain and --customer of a list.
func (a *app) mailFilter(ctx context.Context, args []string) (url.Values, error) {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	domain := fs.String("domain", "", "show only this mail domain")
	customer := fs.String("customer", "", "show only this customer ID")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	q := url.Values{}
	if *customer != "" {
		q.Set("customerId", *customer)
	}
	if *domain != "" {
		d, err := a.mailDomain(ctx, *domain)
		if err != nil {
			return nil, err
		}
		q.Set("domainId", fmt.Sprint(d["id"]))
	}
	return q, nil
}

func (a *app) mailAliases(ctx context.Context, verb string, args []string) error {
	switch verb {
	case "list", "ls":
		q, err := a.mailFilter(ctx, args)
		if err != nil {
			return err
		}
		rows, err := client.All[map[string]any](ctx, a.api, "/mail/aliases", q)
		if err != nil {
			return err
		}
		if a.json {
			return a.printJSON(rows)
		}
		for _, r := range rows {
			if list, ok := r["destinations"].([]any); ok {
				parts := make([]string, len(list))
				for i, v := range list {
					parts[i] = fmt.Sprint(v)
				}
				r["destinations"] = strings.Join(parts, ", ")
			}
		}
		a.table(rows, []string{"address", "kind", "destinations", "status", "id"})
		return nil
	case "create", "add":
		if err := need(args, 1, "an address"); err != nil {
			return err
		}
		fs := flag.NewFlagSet("create", flag.ContinueOnError)
		var to multi
		fs.Var(&to, "to", "a destination address. You can give it more than one time.")
		forward := fs.Bool("forward", false, "make a forwarder: the destinations are addresses on other mail servers")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if len(to) == 0 {
			return errors.New("give --to with a destination address")
		}
		local, domain, err := a.splitAddress(ctx, args[0])
		if err != nil {
			return err
		}
		kind := "alias"
		if *forward {
			kind = "forwarder"
		}
		return a.change(ctx, http.MethodPost, "/mail/aliases", map[string]any{"domainId": domain["id"], "localPart": local, "kind": kind, "destinations": []string(to)})
	case "delete", "rm", "remove":
		if err := need(args, 1, "an address"); err != nil {
			return err
		}
		r, err := a.mailRow(ctx, "/mail/aliases", "alias or forwarder", args[0])
		if err != nil {
			return err
		}
		return a.change(ctx, http.MethodDelete, fmt.Sprintf("/mail/aliases/%v", r["id"]), nil)
	}
	return fmt.Errorf("unknown command mail aliases %s", verb)
}
