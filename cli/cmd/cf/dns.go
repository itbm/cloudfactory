package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/itbm/cloudfactory/cli/internal/client"
)

// findZone gets a DNS zone, with its records, by its name or by its ID.
func (a *app) findZone(ctx context.Context, ref string) (map[string]any, error) {
	rows, err := client.All[map[string]any](ctx, a.api, "/dns-zones", nil)
	if err != nil {
		return nil, err
	}
	for _, z := range rows {
		if z["id"] == ref || z["name"] == strings.ToLower(strings.TrimSuffix(ref, ".")) {
			var zone map[string]any
			err := a.api.Do(ctx, http.MethodGet, fmt.Sprintf("/dns-zones/%v", z["id"]), nil, nil, &zone)
			return zone, err
		}
	}
	return nil, fmt.Errorf("there is no DNS zone %s", ref)
}

// delegationText tells what the public DNS says about a zone.
func delegationText(zone map[string]any) string {
	d, _ := zone["delegation"].(map[string]any)
	seen := []string{}
	if list, ok := d["nameServers"].([]any); ok {
		for _, n := range list {
			seen = append(seen, fmt.Sprint(n))
		}
	}
	switch d["state"] {
	case "here":
		if p, ok := zone["provider"].(map[string]any); ok {
			return "the registrar sends the queries to " + cell(p["name"])
		}
		return "the registrar sends the queries to the panel"
	case "elsewhere":
		return "the registrar sends the queries to other name servers: " + strings.Join(seen, ", ")
	case "none":
		return "the public DNS has no name servers for this domain"
	}
	return "not examined yet"
}

// dnssecText is "on" or "off" for a zone.
func dnssecText(zone map[string]any) string {
	if d, _ := zone["dnssec"].(map[string]any); d["enabled"] == true {
		return "on"
	}
	return "off"
}

// rolloverSteps tells what happens in each step of a change of the DNSSEC key.
var rolloverSteps = map[string]string{
	"publish": "step 1 of 3: the DNS servers publish the new key. Do not change the DS record at the registrar yet",
	"submit":  "step 2 of 3: give the new DS record to the registrar of the domain",
	"retire":  "step 3 of 3: both keys sign the zone. Then the old key goes",
}

// keyTags joins the key tags of a list of the API.
func keyTags(v any) string {
	list, _ := v.([]any)
	tags := make([]string, len(list))
	for i, t := range list {
		tags[i] = cell(t)
	}
	return strings.Join(tags, ", ")
}

// registryText tells which DS records the registry of the domain has.
func registryText(p map[string]any) string {
	others := keyTags(p["otherKeyTags"])
	switch p["state"] {
	case "ok":
		if others != "" {
			return "has the DS record of key " + keyTags(p["keyTags"]) + ", and a DS record of key " + others + ", which the zone does not have. Remove it at the registrar"
		}
		return "has the DS record of key " + keyTags(p["keyTags"])
	case "none":
		return "has no DS record. Resolvers do not check the signatures"
	case "other":
		return "has a DS record of key " + others + ", which the zone does not have. Resolvers that check DNSSEC refuse the answers"
	}
	return "not known. The panel could not ask the public DNS"
}

// printDnssec shows the DS record, a change of the key and the DS records at the registry.
func (a *app) printDnssec(zone map[string]any) {
	d, _ := zone["dnssec"].(map[string]any)
	r, _ := d["rollover"].(map[string]any)
	if ds, ok := d["ds"].(map[string]any); ok {
		if r != nil && r["step"] != "publish" {
			fmt.Fprintf(a.out, "New DS record: %s\n", cell(ds["record"]))
		} else {
			fmt.Fprintf(a.out, "DS record:     %s\n", cell(ds["record"]))
		}
	}
	p, _ := d["parent"].(map[string]any)
	if r != nil {
		fmt.Fprintf(a.out, "Key change:    %s\n", rolloverSteps[cell(r["step"])])
		if r["until"] != nil {
			fmt.Fprintf(a.out, "Step ends:     not before %s\n", cell(r["until"]))
		}
		old, _ := r["oldDs"].(map[string]any)
		if r["step"] == "retire" && p != nil && strings.Contains(", "+keyTags(p["keyTags"])+", ", ", "+cell(old["keyTag"])+", ") {
			fmt.Fprintf(a.out, "Old DS record: %s (remove it at the registrar)\n", cell(old["record"]))
		}
	}
	if p != nil {
		fmt.Fprintf(a.out, "Registry:      %s (checked %s)\n", registryText(p), cell(p["checkedAt"]))
	}
}

// printZone shows a zone: what the customer gives to the registrar, and the state of the zone.
func (a *app) printZone(zone map[string]any) {
	fmt.Fprintf(a.out, "Name:          %s\nID:            %s\nStatus:        %s\nCustomer:      %s\nRecords:       %s\nSerial:        %s\n",
		cell(zone["name"]), cell(zone["id"]), cell(zone["status"]), cell(zone["customerId"]), cell(zone["recordCount"]), cell(zone["serial"]))
	a.printProvider(zone)
	if servers, ok := zone["servers"].([]any); ok {
		for _, s := range servers {
			m, _ := s.(map[string]any)
			fmt.Fprintf(a.out, "Name server:   %s (%s)\n", cell(m["nameServer"]), cell(m["state"]))
		}
	}
	if p, ok := zone["provider"].(map[string]any); ok {
		names := []string{}
		if list, ok := zone["nameServers"].([]any); ok {
			for _, n := range list {
				names = append(names, fmt.Sprint(n))
			}
		}
		if len(names) == 0 {
			fmt.Fprintf(a.out, "Name servers:  %s gives them after the first change\n", cell(p["name"]))
		} else {
			fmt.Fprintf(a.out, "Name servers:  %s (from %s)\n", strings.Join(names, ", "), cell(p["name"]))
		}
	}
	fmt.Fprintf(a.out, "Public DNS:    %s\nDNSSEC:        %s\n", delegationText(zone), dnssecText(zone))
	a.printDnssec(zone)
	a.printTransfers(zone)
	if d, _ := zone["delegation"].(map[string]any); d["state"] != "here" {
		fmt.Fprintln(a.out, "Give the name servers to the registrar of the domain. Then run: cf dns check "+cell(zone["name"]))
	}
}

// changeZone sends a change of a DNS zone, and waits for the job of each DNS server.
func (a *app) changeZone(ctx context.Context, method, path string, body any) error {
	var out map[string]json.RawMessage
	if err := a.api.Do(ctx, method, path, nil, body, &out); err != nil {
		return err
	}
	if a.json {
		return a.printJSON(out)
	}
	if raw, ok := out["record"]; ok {
		var r map[string]any
		json.Unmarshal(raw, &r)
		fmt.Fprintf(a.out, "Record %s: %s %s %s\n", cell(r["id"]), cell(r["name"]), cell(r["type"]), cell(r["value"]))
	}
	if raw, ok := out["zone"]; ok {
		var zone map[string]any
		json.Unmarshal(raw, &zone)
		a.printZone(zone)
	}
	var jobs []client.Job
	json.Unmarshal(out["jobs"], &jobs)
	if len(jobs) == 0 {
		fmt.Fprintln(a.out, "Done.")
		return nil
	}
	// One job for each DNS server. A failure of one server does not stop the others.
	var errs []error
	for _, job := range jobs {
		if a.noWait {
			fmt.Fprintf(a.out, "Job %s is in the queue: %s\n", job.ID, job.Title)
			continue
		}
		errs = append(errs, a.wait(ctx, job.ID))
	}
	return errors.Join(errs...)
}

// dns manages DNS hosting: the zones of a customer and their records. The customer makes a
// zone for a domain, and then gives the name servers of the panel to the registrar of the
// domain.
func (a *app) dns(ctx context.Context, sub string, args []string) error {
	switch sub {
	case "list", "ls":
		fs := flag.NewFlagSet("list", flag.ContinueOnError)
		customer := fs.String("customer", "", "show the zones of this customer ID")
		if err := fs.Parse(args); err != nil {
			return err
		}
		q := url.Values{}
		if *customer != "" {
			q.Set("customerId", *customer)
		}
		var out struct {
			Data        []map[string]any `json:"data"`
			NameServers []string         `json:"nameServers"`
		}
		if err := a.api.Do(ctx, http.MethodGet, "/dns-zones", q, nil, &out); err != nil {
			return err
		}
		if a.json {
			return a.printJSON(out)
		}
		for _, z := range out.Data {
			d, _ := z["delegation"].(map[string]any)
			z["publicDns"] = d["state"]
			z["dnssec"] = dnssecText(z)
			z["records"] = z["recordCount"]
			z["dnsProvider"] = "panel"
			if p, ok := z["provider"].(map[string]any); ok {
				z["dnsProvider"] = p["name"]
			}
		}
		a.table(out.Data, []string{"name", "status", "dnsProvider", "publicDns", "dnssec", "records", "customerId", "id"})
		if len(out.NameServers) == 0 {
			fmt.Fprintln(a.out, "The panel has no DNS server.")
		} else {
			fmt.Fprintln(a.out, "Name servers: "+strings.Join(out.NameServers, ", "))
		}
		return nil
	case "create":
		if err := need(args, 1, "a domain"); err != nil {
			return err
		}
		fs := flag.NewFlagSet("create", flag.ContinueOnError)
		customer := fs.String("customer", "", "the customer ID. With none, it is your customer.")
		account := fs.String("account", "", "a DNS account at an external DNS provider. With none, the zone uses the name servers of the panel.")
		noImport := fs.Bool("no-import", false, "do not copy the records that the provider has for the domain now")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		body := map[string]any{"name": strings.ToLower(strings.TrimSuffix(args[0], "."))}
		if *customer != "" {
			body["customerId"] = *customer
		}
		if *account != "" {
			acc, _, err := a.findDnsAccount(ctx, *account)
			if err != nil {
				return err
			}
			body["accountId"] = acc["id"]
			body["importRecords"] = !*noImport
		}
		return a.changeZone(ctx, http.MethodPost, "/dns-zones", body)
	case "accounts":
		return a.dnsAccountsCommand(ctx, args)
	case "secondaries":
		return a.dnsSecondariesCommand(ctx, args)
	}
	if err := need(args, 1, "a DNS zone"); err != nil {
		return err
	}
	zone, err := a.findZone(ctx, args[0])
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/dns-zones/%v", zone["id"])
	rest := args[1:]
	switch sub {
	case "get":
		if a.json {
			return a.printJSON(zone)
		}
		a.printZone(zone)
		return nil
	case "records":
		fs := flag.NewFlagSet("records", flag.ContinueOnError)
		kind := fs.String("type", "", "show only the records of this type, for example MX")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		var rows []map[string]any
		list, _ := zone["records"].([]any)
		for _, r := range list {
			if m, ok := r.(map[string]any); ok && (*kind == "" || m["type"] == strings.ToUpper(*kind)) {
				rows = append(rows, m)
			}
		}
		if a.json {
			return a.printJSON(rows)
		}
		for _, r := range rows {
			// The panel keeps the name servers. It makes and removes the records of a website.
			switch r["managed"] {
			case "ns":
				r["from"] = "panel"
			case "website":
				r["from"] = "website"
			default:
				r["from"] = "you"
			}
		}
		a.table(rows, []string{"name", "type", "value", "priority", "ttl", "from", "id"})
		return nil
	case "add":
		if len(rest) < 3 {
			return errors.New(`give a type, a name and a value, for example: cf dns add example.com TXT @ "v=spf1 -all"`)
		}
		fs := flag.NewFlagSet("add", flag.ContinueOnError)
		ttl := fs.Int("ttl", 0, "the TTL in seconds. The default is 3600.")
		priority := fs.Int("priority", -1, "the priority of an MX or SRV record. The default is 10.")
		if err := fs.Parse(rest[3:]); err != nil {
			return err
		}
		body := map[string]any{"type": strings.ToUpper(rest[0]), "name": rest[1], "value": rest[2]}
		if *ttl > 0 {
			body["ttl"] = *ttl
		}
		if *priority >= 0 {
			body["priority"] = *priority
		}
		return a.changeZone(ctx, http.MethodPost, path+"/records", body)
	case "set":
		if err := need(rest, 1, "the ID of a record: cf dns records "+cell(zone["name"])); err != nil {
			return err
		}
		fs := flag.NewFlagSet("set", flag.ContinueOnError)
		name := fs.String("name", "", "a new name")
		value := fs.String("value", "", "a new value")
		ttl := fs.Int("ttl", 0, "the TTL in seconds")
		priority := fs.Int("priority", -1, "the priority of an MX or SRV record")
		if err := fs.Parse(rest[1:]); err != nil {
			return err
		}
		body := map[string]any{}
		if *name != "" {
			body["name"] = *name
		}
		if *value != "" {
			body["value"] = *value
		}
		if *ttl > 0 {
			body["ttl"] = *ttl
		}
		if *priority >= 0 {
			body["priority"] = *priority
		}
		if len(body) == 0 {
			return errors.New("give --name, --value, --ttl or --priority")
		}
		return a.changeZone(ctx, http.MethodPatch, path+"/records/"+url.PathEscape(rest[0]), body)
	case "remove", "rm":
		if err := need(rest, 1, "the ID of a record: cf dns records "+cell(zone["name"])); err != nil {
			return err
		}
		return a.changeZone(ctx, http.MethodDelete, path+"/records/"+url.PathEscape(rest[0]), nil)
	case "dnssec":
		if len(rest) != 1 || (rest[0] != "on" && rest[0] != "off" && rest[0] != "rollover" && rest[0] != "cancel") {
			return errors.New("give on, off, rollover or cancel")
		}
		switch rest[0] {
		case "rollover":
			// A change of the key: the panel moves it to the next step when it can.
			if err := a.changeZone(ctx, http.MethodPost, path+"/dnssec/rollover", nil); err != nil {
				return err
			}
			if !a.json {
				fmt.Fprintln(a.out, "In approximately 3 hours, give the new DS record to the registrar. Run \"cf dns get "+cell(zone["name"])+"\" to see the step.")
			}
			return nil
		case "cancel":
			return a.changeZone(ctx, http.MethodDelete, path+"/dnssec/rollover", nil)
		}
		if err := a.changeZone(ctx, http.MethodPut, path+"/dnssec", map[string]any{"enabled": rest[0] == "on"}); err != nil {
			return err
		}
		if !a.json && rest[0] == "on" {
			fmt.Fprintln(a.out, "Give the DS record to the registrar of the domain. DNSSEC has an effect only after that.")
		}
		return nil
	case "check":
		var out map[string]any
		if err := a.api.Do(ctx, http.MethodPost, path+"/check", nil, nil, &out); err != nil {
			return err
		}
		if a.json {
			return a.printJSON(out)
		}
		fmt.Fprintf(a.out, "%s: %s.\n", cell(out["name"]), delegationText(out))
		if d, _ := out["dnssec"].(map[string]any); d["rollover"] != nil || d["parent"] != nil {
			a.printDnssec(out)
		}
		return nil
	case "sync":
		return a.changeZone(ctx, http.MethodPost, path+"/sync", nil)
	case "transfers":
		return a.dnsTransfers(ctx, zone, path, rest)
	case "import":
		return a.importZone(ctx, zone, path, rest)
	case "export":
		fs := flag.NewFlagSet("export", flag.ContinueOnError)
		file := fs.String("file", "", "write the zone file to this file. With none, it goes to the output.")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		var out struct {
			Name     string `json:"name"`
			ZoneFile string `json:"zoneFile"`
		}
		if err := a.api.Do(ctx, http.MethodGet, path+"/export", nil, nil, &out); err != nil {
			return err
		}
		if a.json {
			return a.printJSON(out)
		}
		if *file != "" {
			if err := os.WriteFile(*file, []byte(out.ZoneFile), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Wrote the zone %s to %s.\n", out.Name, *file)
			return nil
		}
		fmt.Fprint(a.out, out.ZoneFile)
		return nil
	case "move":
		if err := need(rest, 1, "a DNS account, or panel for the name servers of the panel"); err != nil {
			return err
		}
		fs := flag.NewFlagSet("move", flag.ContinueOnError)
		noImport := fs.Bool("no-import", false, "do not keep the records that the new provider has for the domain now")
		if err := fs.Parse(rest[1:]); err != nil {
			return err
		}
		body := map[string]any{"accountId": nil, "importRecords": !*noImport}
		if rest[0] != "panel" {
			acc, _, err := a.findDnsAccount(ctx, rest[0])
			if err != nil {
				return err
			}
			body["accountId"] = acc["id"]
		}
		return a.changeZone(ctx, http.MethodPost, path+"/move", body)
	case "delete":
		fs := flag.NewFlagSet("delete", flag.ContinueOnError)
		keep := fs.Bool("keep-at-provider", false, "remove the zone from the panel only, and keep it at its external DNS provider")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if *keep {
			path += "?keepAtProvider=1"
		}
		return a.changeZone(ctx, http.MethodDelete, path, nil)
	}
	return fmt.Errorf("unknown command dns %s", sub)
}

// importZone adds the records of a zone file to a zone. With --dry-run, it shows what the panel
// would add, and makes no change.
func (a *app) importZone(ctx context.Context, zone map[string]any, path string, args []string) error {
	if err := need(args, 1, "a zone file, for example: cf dns import "+cell(zone["name"])+" example.com.db"); err != nil {
		return err
	}
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "show what the panel would import, and make no change")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	text, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	var out struct {
		Added    []map[string]any `json:"added"`
		Existing int              `json:"existing"`
		Skipped  []struct {
			Line   int    `json:"line"`
			Text   string `json:"text"`
			Reason string `json:"reason"`
		} `json:"skipped"`
		Jobs []client.Job `json:"jobs"`
	}
	if err := a.api.Do(ctx, http.MethodPost, path+"/import", nil, map[string]any{"zoneFile": string(text), "dryRun": *dryRun}, &out); err != nil {
		return err
	}
	if a.json {
		return a.printJSON(out)
	}
	if len(out.Added) > 0 {
		a.table(out.Added, []string{"line", "name", "type", "value", "priority", "ttl", "note"})
	}
	verb := "Added"
	if *dryRun {
		verb = "Would add"
	}
	fmt.Fprintf(a.out, "%s %s. %s in the zone already. %s not imported.\n", verb, plural(len(out.Added), "record", "records"),
		plural(out.Existing, "record is", "records are"), plural(len(out.Skipped), "line is", "lines are"))
	for _, s := range out.Skipped {
		fmt.Fprintf(a.out, "Line %d: %s\n  %s\n", s.Line, s.Reason, s.Text)
	}
	if *dryRun {
		return nil
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

// plural gives the number with the word for one or for more.
func plural(n int, one, more string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, more)
}
