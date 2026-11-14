package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/itbm/cloudfactory/cli/internal/client"
)

// byteSize gives a size in binary units, for example "512 MB" or "1.5 GB".
func byteSize(v any) string {
	n, ok := v.(float64)
	if !ok {
		return "-"
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	i := 0
	for n >= 1024 && i < len(units)-1 {
		n /= 1024
		i++
	}
	return strconv.FormatFloat(float64(int(n*10+0.5))/10, 'f', -1, 64) + " " + units[i]
}

// limitsText gives the limits of a database server, for example "2 vCPU, 1 GB memory, 10 GB disk".
func limitsText(v any) string {
	l, ok := v.(map[string]any)
	if !ok {
		return "-"
	}
	return fmt.Sprintf("%s vCPU, %s memory, %s disk", cell(l["vcpus"]), byteSize(l["memoryBytes"]), byteSize(l["diskBytes"]))
}

// address gives the host name and the port that a website container connects to.
func address(row map[string]any) string {
	if row["host"] == nil || row["port"] == nil {
		return "-"
	}
	return cell(row["host"]) + ":" + cell(row["port"])
}

// findDatabaseServer gets a database server by its ID or by its name. A name is unique only
// for one customer, so a name that more than one customer uses is an error.
func (a *app) findDatabaseServer(ctx context.Context, ref string) (map[string]any, error) {
	rows, err := client.All[map[string]any](ctx, a.api, "/database-servers", nil)
	if err != nil {
		return nil, err
	}
	var found []map[string]any
	for _, s := range rows {
		if s["id"] == ref {
			return s, nil
		}
		if s["name"] == ref {
			found = append(found, s)
		}
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("there is no database server %s", ref)
	case 1:
		return found[0], nil
	}
	return nil, fmt.Errorf("more than one customer has a database server with the name %s. Use the ID: cf database-servers list", ref)
}

// serverID gives the ID of a server from a name or an ID. Only an administrator can list the
// servers: for the other users, the value is the ID.
func (a *app) serverID(ctx context.Context, ref string) (string, error) {
	s, err := a.findServer(ctx, ref)
	if err != nil {
		var apiErr *client.Error
		if errors.As(err, &apiErr) {
			return ref, nil
		}
		return "", err
	}
	return fmt.Sprint(s["id"]), nil
}

// databaseServerLimits reads --vcpus, --memory-mb and --disk-gb. The API needs the three
// limits together: a limit that is not given keeps the value of base.
func databaseServerLimits(base map[string]any, vcpus float64, memoryMB, diskGB int64) (map[string]any, error) {
	limits := map[string]any{}
	for _, k := range []string{"vcpus", "memoryBytes", "diskBytes"} {
		if v, ok := base[k]; ok {
			limits[k] = v
		}
	}
	if vcpus > 0 {
		limits["vcpus"] = vcpus
	}
	if memoryMB > 0 {
		limits["memoryBytes"] = memoryMB << 20
	}
	if diskGB > 0 {
		limits["diskBytes"] = diskGB << 30
	}
	if len(limits) < 3 {
		return nil, errors.New("give --vcpus, --memory-mb and --disk-gb together")
	}
	return limits, nil
}

// databaseServers manages the database servers of customers. A customer makes database
// servers as it makes websites: each one is a container with one engine (MariaDB, PostgreSQL
// or Valkey) and its own limits.
//
//   - The plan of the customer gives the number of its database servers.
//   - The plan of a database server gives its limits and the number of its databases.
//   - All websites of the customer on the same server can connect to the database server.
func (a *app) databaseServers(ctx context.Context, sub string, args []string) error {
	switch sub {
	case "list", "ls":
		fs := flag.NewFlagSet("list", flag.ContinueOnError)
		customer := fs.String("customer", "", "show the database servers of this customer ID")
		server := fs.String("server", "", "show the database servers on this server")
		engine := fs.String("engine", "", "mariadb, postgresql or valkey")
		if err := fs.Parse(args); err != nil {
			return err
		}
		q := url.Values{}
		if *customer != "" {
			q.Set("customerId", *customer)
		}
		if *engine != "" {
			q.Set("engine", *engine)
		}
		if *server != "" {
			id, err := a.serverID(ctx, *server)
			if err != nil {
				return err
			}
			q.Set("serverId", id)
		}
		rows, err := client.All[map[string]any](ctx, a.api, "/database-servers", q)
		if err != nil {
			return err
		}
		if a.json {
			return a.printJSON(rows)
		}
		for _, s := range rows {
			s["address"] = address(s)
			s["limits"] = limitsText(s["limits"])
			s["databases"] = fmt.Sprintf("%s of %s", cell(s["databases"]), cell(s["maxDatabases"]))
		}
		a.table(rows, []string{"name", "engine", "status", "address", "limits", "databases", "customerId", "id"})
		return nil
	case "create":
		fs := flag.NewFlagSet("create", flag.ContinueOnError)
		engine := fs.String("engine", "", "mariadb, postgresql or valkey")
		name := fs.String("name", "", `a name. With no name, the panel gives one, for example "mariadb-2".`)
		customer := fs.String("customer", "", "the customer ID. With none, it is your customer.")
		plan := fs.String("plan", "", "a plan ID. The plan gives the limits and the number of databases. With none, it is the plan of the customer.")
		server := fs.String("server", "", "the server for the container. With none, it is the server with the most websites of the customer.")
		vcpus := fs.Float64("vcpus", 0, "the CPU limit in vCPUs. Only an administrator sets limits.")
		memory := fs.Int64("memory-mb", 0, "the memory limit in MB")
		disk := fs.Int64("disk-gb", 0, "the disk limit in GB")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if *engine == "" {
			return errors.New("give --engine mariadb, postgresql or valkey")
		}
		body := map[string]any{"engine": *engine}
		for k, v := range map[string]string{"name": *name, "customerId": *customer, "planId": *plan} {
			if v != "" {
				body[k] = v
			}
		}
		if *server != "" {
			id, err := a.serverID(ctx, *server)
			if err != nil {
				return err
			}
			body["serverId"] = id
		}
		if *vcpus > 0 || *memory > 0 || *disk > 0 {
			limits, err := databaseServerLimits(nil, *vcpus, *memory, *disk)
			if err != nil {
				return err
			}
			body["limits"] = limits
		}
		return a.change(ctx, http.MethodPost, "/database-servers", body)
	case "get", "set", "retry", "delete", "rm":
		if err := need(args, 1, "a database server"); err != nil {
			return err
		}
		s, err := a.findDatabaseServer(ctx, args[0])
		if err != nil {
			return err
		}
		path := fmt.Sprintf("/database-servers/%v", s["id"])
		switch sub {
		case "get":
			if a.json {
				return a.printJSON(s)
			}
			fmt.Fprintf(a.out, "Name:       %s\nID:         %s\nEngine:     %s\nStatus:     %s\nCustomer:   %s\nServer:     %s\nPlan:       %s\nAddress:    %s\nLimits:     %s\nDatabases:  %s of %s\n",
				cell(s["name"]), cell(s["id"]), cell(s["engine"]), cell(s["status"]), cell(s["customerId"]), cell(s["serverId"]), cell(s["planId"]), address(s), limitsText(s["limits"]), cell(s["databases"]), cell(s["maxDatabases"]))
			// The last report of the server. A new database server has none.
			if u, ok := s["usage"].(map[string]any); ok && u["memoryBytes"] != nil {
				fmt.Fprintf(a.out, "Use:        %s memory, %s of %s disk\n", byteSize(u["memoryBytes"]), byteSize(u["diskBytes"]), byteSize(u["diskSizeBytes"]))
			}
			return nil
		case "retry":
			return a.change(ctx, http.MethodPost, path+"/retry", nil)
		case "delete", "rm":
			return a.change(ctx, http.MethodDelete, path, nil)
		}
		fs := flag.NewFlagSet("set", flag.ContinueOnError)
		name := fs.String("name", "", "a new name")
		plan := fs.String("plan", "", "a plan ID. The database server gets the limits of the plan.")
		vcpus := fs.Float64("vcpus", 0, "the CPU limit in vCPUs. Only an administrator sets limits.")
		memory := fs.Int64("memory-mb", 0, "the memory limit in MB")
		disk := fs.Int64("disk-gb", 0, "the disk limit in GB")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		body := map[string]any{}
		if *name != "" {
			body["name"] = *name
		}
		if *plan != "" {
			body["planId"] = *plan
		}
		if *vcpus > 0 || *memory > 0 || *disk > 0 {
			// A limit that is not given keeps its value. With a new plan, it gets the value of the plan.
			base, _ := s["limits"].(map[string]any)
			if *plan != "" {
				plans, err := client.All[map[string]any](ctx, a.api, "/plans", nil)
				if err != nil {
					return err
				}
				for _, p := range plans {
					if p["id"] == *plan {
						base, _ = p["databaseLimits"].(map[string]any)
					}
				}
			}
			limits, err := databaseServerLimits(base, *vcpus, *memory, *disk)
			if err != nil {
				return err
			}
			body["limits"] = limits
		}
		if len(body) == 0 {
			return errors.New("give --name, --plan or a limit")
		}
		return a.change(ctx, http.MethodPatch, path, body)
	}
	return fmt.Errorf("unknown command database-servers %s", sub)
}
