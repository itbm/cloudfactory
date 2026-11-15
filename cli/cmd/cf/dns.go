package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
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

// printZone shows a zone: what the customer gives to the registrar, and the state of the zone.
func (a *app) printZone(zone map[string]any) {
	fmt.Fprintf(a.out, "Name:          %s\nID:            %s\nStatus:        %s\nCustomer:      %s\nRecords:       %s\nSerial:        %s\n",
		cell(zone["name"]), cell(zone["id"]), cell(zone["status"]), cell(zone["customerId"]), cell(zone["recordCount"]), cell(zone["serial"]))
	if servers, ok := zone["servers"].([]any); ok {
		for _, s := range servers {
			m, _ := s.(map[string]any)
			fmt.Fprintf(a.out, "Name server:   %s (%s)\n", cell(m["nameServer"]), cell(m["state"]))
		}
	}
	fmt.Fprintf(a.out, "Public DNS:    %s\nDNSSEC:        %s\n", delegationText(zone), dnssecText(zone))
	if d, _ := zone["dnssec"].(map[string]any); d["ds"] != nil {
		ds, _ := d["ds"].(map[string]any)
		fmt.Fprintf(a.out, "DS record:     %s\n", cell(ds["record"]))
	}
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
		}
		a.table(out.Data, []string{"name", "status", "publicDns", "dnssec", "records", "customerId", "id"})
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
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		body := map[string]any{"name": strings.ToLower(strings.TrimSuffix(args[0], "."))}
		if *customer != "" {
			body["customerId"] = *customer
		}
		return a.changeZone(ctx, http.MethodPost, "/dns-zones", body)
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
		if len(rest) != 1 || (rest[0] != "on" && rest[0] != "off") {
			return errors.New("give on or off")
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
		return nil
	case "sync":
		return a.changeZone(ctx, http.MethodPost, path+"/sync", nil)
	case "delete":
		return a.changeZone(ctx, http.MethodDelete, path, nil)
	}
	return fmt.Errorf("unknown command dns %s", sub)
}
