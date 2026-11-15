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

// findNetwork gets a private network by its ID or by its name. A name is unique only for one
// customer, so a name that more than one customer uses is an error.
func (a *app) findNetwork(ctx context.Context, ref string) (map[string]any, error) {
	rows, err := client.All[map[string]any](ctx, a.api, "/networks", nil)
	if err != nil {
		return nil, err
	}
	var found []map[string]any
	for _, n := range rows {
		if n["id"] == ref {
			return n, nil
		}
		if n["name"] == strings.ToLower(ref) {
			found = append(found, n)
		}
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("there is no network %s", ref)
	case 1:
		return found[0], nil
	}
	return nil, fmt.Errorf("more than one customer has a network with the name %s. Use the ID: cf networks list", ref)
}

// ids gives the strings of a list in an answer of the API.
func ids(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, x := range list {
		out = append(out, fmt.Sprint(x))
	}
	return out
}

// memberFlags adds the flags that select websites and database servers.
func memberFlags(fs *flag.FlagSet) (websites, databaseServers *multi) {
	websites, databaseServers = new(multi), new(multi)
	fs.Var(websites, "website", "a website, by its name or its ID. You can give it more than one time.")
	fs.Var(databaseServers, "database-server", "a database server, by its name or its ID. You can give it more than one time.")
	return websites, databaseServers
}

// memberIDs gets the IDs of the websites and the database servers of the flags.
func (a *app) memberIDs(ctx context.Context, websites, databaseServers multi) (w, d []string, err error) {
	for _, ref := range websites {
		site, err := a.api.FindWebsite(ctx, ref)
		if err != nil {
			return nil, nil, err
		}
		w = append(w, site.ID)
	}
	for _, ref := range databaseServers {
		server, err := a.findDatabaseServer(ctx, ref)
		if err != nil {
			return nil, nil, err
		}
		d = append(d, fmt.Sprint(server["id"]))
	}
	return w, d, nil
}

// with gives the list with the IDs added, or with the IDs removed. An ID is in it one time.
func with(list, change []string, add bool) []string {
	changed := map[string]bool{}
	for _, id := range change {
		changed[id] = true
	}
	out := []string{}
	seen := map[string]bool{}
	for _, id := range list {
		if !seen[id] && (add || !changed[id]) {
			out = append(out, id)
			seen[id] = true
		}
	}
	if add {
		for _, id := range change {
			if !seen[id] {
				out = append(out, id)
				seen[id] = true
			}
		}
	}
	return out
}

// printNetwork shows a network with the network names of its members.
func (a *app) printNetwork(ctx context.Context, n map[string]any) error {
	if a.json {
		return a.printJSON(n)
	}
	kind := "named"
	if n["isDefault"] == true {
		kind = "default"
	}
	fmt.Fprintf(a.out, "Name:          %s (%s)\nID:            %s\nCustomer:      %s\n", cell(n["name"]), kind, cell(n["id"]), cell(n["customerId"]))
	for _, part := range []struct{ label, path, key string }{{"Website:      ", "/websites", "websiteIds"}, {"Database:     ", "/database-servers", "databaseServerIds"}} {
		want := ids(n[part.key])
		if len(want) == 0 {
			continue
		}
		rows, err := client.All[map[string]any](ctx, a.api, part.path, nil)
		if err != nil {
			return err
		}
		for _, id := range want {
			for _, r := range rows {
				if r["id"] != id {
					continue
				}
				host := "no network name"
				if r["networkHost"] != nil {
					host = fmt.Sprintf("%s:%s", cell(r["networkHost"]), cell(r["networkPort"]))
				}
				fmt.Fprintf(a.out, "%s %s (%s)\n", part.label, cell(r["name"]), host)
			}
		}
	}
	return nil
}

// networks manages the private networks of the customers. Each website in a network connects
// to each other member of the network by its network name, on its own server and on a
// different server. Each customer has the network "default". The plan of the customer gives
// the number of named networks.
func (a *app) networks(ctx context.Context, sub string, args []string) error {
	switch sub {
	case "list", "ls":
		fs := flag.NewFlagSet("list", flag.ContinueOnError)
		customer := fs.String("customer", "", "show the networks of this customer ID")
		if err := fs.Parse(args); err != nil {
			return err
		}
		q := url.Values{}
		if *customer != "" {
			q.Set("customerId", *customer)
		}
		rows, err := client.All[map[string]any](ctx, a.api, "/networks", q)
		if err != nil {
			return err
		}
		if a.json {
			return a.printJSON(rows)
		}
		for _, n := range rows {
			n["websites"] = len(ids(n["websiteIds"]))
			n["databaseServers"] = len(ids(n["databaseServerIds"]))
			n["default"] = "no"
			if n["isDefault"] == true {
				n["default"] = "yes"
			}
		}
		a.table(rows, []string{"name", "default", "websites", "databaseServers", "customerId", "id"})
		return nil
	case "create":
		if err := need(args, 1, "a name"); err != nil {
			return err
		}
		fs := flag.NewFlagSet("create", flag.ContinueOnError)
		customer := fs.String("customer", "", "the customer ID. With none, it is your customer.")
		websites, databaseServers := memberFlags(fs)
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		w, d, err := a.memberIDs(ctx, *websites, *databaseServers)
		if err != nil {
			return err
		}
		body := map[string]any{"name": strings.ToLower(args[0]), "websiteIds": with(nil, w, true), "databaseServerIds": with(nil, d, true)}
		if *customer != "" {
			body["customerId"] = *customer
		}
		var out map[string]any
		if err := a.api.Do(ctx, http.MethodPost, "/networks", nil, body, &out); err != nil {
			return err
		}
		return a.printNetwork(ctx, out)
	}
	if err := need(args, 1, "a network"); err != nil {
		return err
	}
	n, err := a.findNetwork(ctx, args[0])
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/networks/%v", n["id"])
	rest := args[1:]
	var body map[string]any
	switch sub {
	case "get":
		return a.printNetwork(ctx, n)
	case "rename":
		if err := need(rest, 1, "the new name"); err != nil {
			return err
		}
		body = map[string]any{"name": strings.ToLower(rest[0])}
	case "add", "remove":
		fs := flag.NewFlagSet(sub, flag.ContinueOnError)
		websites, databaseServers := memberFlags(fs)
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if len(*websites)+len(*databaseServers) == 0 {
			return errors.New("give --website or --database-server")
		}
		w, d, err := a.memberIDs(ctx, *websites, *databaseServers)
		if err != nil {
			return err
		}
		body = map[string]any{"websiteIds": with(ids(n["websiteIds"]), w, sub == "add"), "databaseServerIds": with(ids(n["databaseServerIds"]), d, sub == "add")}
	case "delete":
		if err := a.api.Do(ctx, http.MethodDelete, path, nil, nil, nil); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "The network %s is removed. Its members stay.\n", cell(n["name"]))
		return nil
	default:
		return fmt.Errorf("unknown command networks %s", sub)
	}
	var out map[string]any
	if err := a.api.Do(ctx, http.MethodPatch, path, nil, body, &out); err != nil {
		return err
	}
	return a.printNetwork(ctx, out)
}
