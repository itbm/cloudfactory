package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/itbm/cloudfactory/cli/internal/client"
)

// certificateStages are the texts of the stages of an order with DNS validation.
var certificateStages = map[string]string{
	"new":        "Make the order and add the TXT records",
	"dns":        "Wait for the DNS servers",
	"validating": "Let's Encrypt checks the records",
	"finalizing": "Get the certificate",
}

// findCertificate gets a certificate, with its chain, by its ID or by its first name.
func (a *app) findCertificate(ctx context.Context, ref string) (map[string]any, error) {
	rows, err := client.All[map[string]any](ctx, a.api, "/certificates", nil)
	if err != nil {
		return nil, err
	}
	name := strings.ToLower(strings.TrimSuffix(ref, "."))
	var found []map[string]any
	for _, c := range rows {
		names, _ := c["names"].([]any)
		if c["id"] == ref {
			found = []map[string]any{c}
			break
		}
		if len(names) > 0 && names[0] == name {
			found = append(found, c)
		}
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("there is no certificate %s", ref)
	case 1:
		var c map[string]any
		err := a.api.Do(ctx, http.MethodGet, fmt.Sprintf("/certificates/%v", found[0]["id"]), nil, nil, &c)
		return c, err
	}
	return nil, fmt.Errorf("more than one certificate has the name %s. Use the ID of the certificate", ref)
}

// certificateNames gives the names of a certificate, separated by commas.
func certificateNames(c map[string]any) string {
	names, _ := c["names"].([]any)
	list := make([]string, len(names))
	for i, n := range names {
		list[i] = fmt.Sprint(n)
	}
	return strings.Join(list, ", ")
}

// printCertificate shows a certificate, the order that runs and the domains that use it.
func (a *app) printCertificate(c map[string]any) {
	source := "uploaded"
	if c["source"] == "acme" {
		source = "Let's Encrypt, DNS validation"
	}
	fmt.Fprintf(a.out, "Names:         %s\nID:            %s\nSource:        %s\nState:         %s\nCustomer:      %s\nIssuer:        %s\nValid from:    %s\nExpires:       %s\n",
		certificateNames(c), cell(c["id"]), source, cell(c["state"]), cell(c["customerId"]), cell(c["issuer"]), cell(c["notBefore"]), cell(c["expiresAt"]))
	if c["source"] == "acme" {
		fmt.Fprintf(a.out, "Renewal:       %s\n", cell(c["renewAt"]))
	}
	fmt.Fprintf(a.out, "SHA-256:       %s\n", cell(c["fingerprint"]))
	if c["trusted"] == false {
		fmt.Fprintln(a.out, "Browsers do not trust the issuer of this certificate.")
	}
	if order, ok := c["order"].(map[string]any); ok {
		fmt.Fprintf(a.out, "Order:         %s\n", certificateStages[fmt.Sprint(order["stage"])])
		records, _ := order["records"].([]any)
		for _, r := range records {
			m, _ := r.(map[string]any)
			fmt.Fprintf(a.out, "TXT record:    %s %s\n", cell(m["name"]), cell(m["value"]))
		}
	}
	if c["error"] != nil {
		fmt.Fprintf(a.out, "Error:         %s\n", cell(c["error"]))
		if c["nextTryAt"] != nil {
			fmt.Fprintf(a.out, "Next try:      %s\n", cell(c["nextTryAt"]))
		}
	}
	used, _ := c["usedBy"].([]any)
	for _, u := range used {
		m, _ := u.(map[string]any)
		fmt.Fprintf(a.out, "Used by:       %s (%s, %s)\n", cell(m["domain"]), cell(m["websiteName"]), cell(m["serverName"]))
	}
}

// waitForOrder reads the certificate again until its order ends. It shows each stage.
func (a *app) waitForOrder(ctx context.Context, c map[string]any) error {
	if a.noWait {
		fmt.Fprintf(a.out, "The order of the certificate %s runs. Run: cf certificates get %s\n", cell(c["id"]), cell(c["id"]))
		return nil
	}
	poll := a.poll
	if poll == 0 {
		poll = 3 * time.Second
	}
	last := ""
	for {
		order, running := c["order"].(map[string]any)
		if !running {
			break
		}
		if stage := fmt.Sprint(order["stage"]); stage != last {
			fmt.Fprintf(a.out, "[%s] %s\n", stage, certificateStages[stage])
			last = stage
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
		if err := a.api.Do(ctx, http.MethodGet, fmt.Sprintf("/certificates/%v", c["id"]), nil, nil, &c); err != nil {
			return err
		}
	}
	if a.json {
		return a.printJSON(c)
	}
	if c["error"] != nil {
		return fmt.Errorf("the order failed: %v", c["error"])
	}
	a.printCertificate(c)
	return nil
}

// readPem reads the PEM data of a flag: a file, or "-" for the standard input.
func readPem(path, what string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("give the file of the %s", what)
	}
	if path == "-" {
		b, err := io.ReadAll(os.Stdin)
		return string(b), err
	}
	b, err := os.ReadFile(path)
	return string(b), err
}

// certificates manages the certificates of the customers: uploads, and certificates from
// Let's Encrypt with DNS validation in a DNS zone of the customer on the panel.
func (a *app) certificates(ctx context.Context, sub string, args []string) error {
	switch sub {
	case "list", "ls":
		fs := flag.NewFlagSet("list", flag.ContinueOnError)
		customer := fs.String("customer", "", "show the certificates of this customer ID")
		if err := fs.Parse(args); err != nil {
			return err
		}
		q := url.Values{}
		if *customer != "" {
			q.Set("customerId", *customer)
		}
		var out struct {
			Data []map[string]any `json:"data"`
		}
		if err := a.api.Do(ctx, http.MethodGet, "/certificates", q, nil, &out); err != nil {
			return err
		}
		if a.json {
			return a.printJSON(out.Data)
		}
		for _, c := range out.Data {
			c["names"] = certificateNames(c)
			used, _ := c["usedBy"].([]any)
			c["domains"] = len(used)
		}
		a.table(out.Data, []string{"names", "source", "state", "expiresAt", "domains", "customerId", "id"})
		return nil
	case "upload":
		fs := flag.NewFlagSet("upload", flag.ContinueOnError)
		cert := fs.String("cert", "", "the file of the certificate chain in PEM format")
		key := fs.String("key", "", "the file of the private key in PEM format, with no password")
		customer := fs.String("customer", "", "the customer ID. With none, it is your customer.")
		if err := fs.Parse(args); err != nil {
			return err
		}
		chain, err := readPem(*cert, "certificate chain (--cert)")
		if err != nil {
			return err
		}
		private, err := readPem(*key, "private key (--key)")
		if err != nil {
			return err
		}
		body := map[string]any{"source": "upload", "certificate": chain, "privateKey": private}
		if *customer != "" {
			body["customerId"] = *customer
		}
		var c map[string]any
		if err := a.api.Do(ctx, http.MethodPost, "/certificates", nil, body, &c); err != nil {
			return err
		}
		if a.json {
			return a.printJSON(c)
		}
		a.printCertificate(c)
		return nil
	case "request":
		if err := need(args, 1, "a domain"); err != nil {
			return err
		}
		fs := flag.NewFlagSet("request", flag.ContinueOnError)
		wildcard := fs.Bool("wildcard", false, "the certificate is also valid for each name directly below the domain")
		customer := fs.String("customer", "", "the customer ID. With none, it is your customer.")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		body := map[string]any{"source": "acme", "domain": strings.ToLower(strings.TrimSuffix(args[0], ".")), "wildcard": *wildcard}
		if *customer != "" {
			body["customerId"] = *customer
		}
		var c map[string]any
		if err := a.api.Do(ctx, http.MethodPost, "/certificates", nil, body, &c); err != nil {
			return err
		}
		return a.waitForOrder(ctx, c)
	}
	if err := need(args, 1, "a certificate"); err != nil {
		return err
	}
	c, err := a.findCertificate(ctx, args[0])
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/certificates/%v", c["id"])
	switch sub {
	case "get":
		fs := flag.NewFlagSet("get", flag.ContinueOnError)
		pem := fs.Bool("pem", false, "show only the certificate chain in PEM format")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *pem {
			chain, _ := c["certificatePem"].(string)
			if chain == "" {
				return fmt.Errorf("the certificate %s has no chain yet", args[0])
			}
			fmt.Fprint(a.out, chain)
			return nil
		}
		if a.json {
			return a.printJSON(c)
		}
		a.printCertificate(c)
		return nil
	case "replace":
		fs := flag.NewFlagSet("replace", flag.ContinueOnError)
		cert := fs.String("cert", "", "the file of the new certificate chain in PEM format")
		key := fs.String("key", "", "the file of the new private key in PEM format, with no password")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		chain, err := readPem(*cert, "certificate chain (--cert)")
		if err != nil {
			return err
		}
		private, err := readPem(*key, "private key (--key)")
		if err != nil {
			return err
		}
		var out map[string]any
		if err := a.api.Do(ctx, http.MethodPut, path, nil, map[string]any{"certificate": chain, "privateKey": private}, &out); err != nil {
			return err
		}
		if a.json {
			return a.printJSON(out)
		}
		a.printCertificate(out)
		return nil
	case "renew":
		var out map[string]any
		if err := a.api.Do(ctx, http.MethodPost, path+"/renew", nil, nil, &out); err != nil {
			return err
		}
		return a.waitForOrder(ctx, out)
	case "delete", "remove", "rm":
		if err := a.api.Do(ctx, http.MethodDelete, path, nil, nil, nil); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "Removed the certificate %s. Its domains get a different certificate.\n", certificateNames(c))
		return nil
	}
	return fmt.Errorf("unknown command certificates %s", sub)
}
