package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"strings"
	"text/tabwriter"

	"github.com/itbm/cloudfactory/cli/internal/client"
)

// The mesh: the encrypted tunnels between the servers of the panel. Only an administrator
// can use these commands.

type meshServer struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Supported     bool    `json:"supported"`
	PublicKey     *string `json:"publicKey"`
	Address       *string `json:"address"`
	TunnelAddress *string `json:"tunnelAddress"`
	Tunnels       int     `json:"tunnels"`
	Current       bool    `json:"current"`
}

type meshSide struct {
	ServerID    string   `json:"serverId"`
	Server      *string  `json:"server"`
	Reachable   *bool    `json:"reachable"`
	HandshakeAt *string  `json:"handshakeAt"`
	RoundTripMs *float64 `json:"roundTripMs"`
}

type meshTunnel struct {
	ID          string     `json:"id"`
	State       string     `json:"state"`
	RoundTripMs *float64   `json:"roundTripMs"`
	Sides       []meshSide `json:"sides"`
}

type meshState struct {
	Range   string       `json:"range"`
	Port    int          `json:"port"`
	Servers []meshServer `json:"servers"`
	Tunnels []meshTunnel `json:"tunnels"`
}

func text(s *string, empty string) string {
	if s == nil || *s == "" {
		return empty
	}
	return *s
}

// server gets a server of the mesh by its ID or by its name.
func (m meshState) server(ref string) (meshServer, error) {
	for _, s := range m.Servers {
		if s.ID == ref || s.Name == ref {
			return s, nil
		}
	}
	return meshServer{}, fmt.Errorf("there is no server %s", ref)
}

// tunnel gets the tunnel between two servers.
func (m meshState) tunnel(one, two meshServer) (meshTunnel, error) {
	for _, t := range m.Tunnels {
		if len(t.Sides) == 2 && ((t.Sides[0].ServerID == one.ID && t.Sides[1].ServerID == two.ID) || (t.Sides[0].ServerID == two.ID && t.Sides[1].ServerID == one.ID)) {
			return t, nil
		}
	}
	return meshTunnel{}, fmt.Errorf("there is no tunnel between %s and %s", one.Name, two.Name)
}

// names gives the names of the two servers of a tunnel.
func (t meshTunnel) names() string {
	names := make([]string, len(t.Sides))
	for i, s := range t.Sides {
		names[i] = text(s.Server, s.ServerID)
	}
	return strings.Join(names, " and ")
}

// handshake gives the time of the most recent handshake that the two servers reported.
func (t meshTunnel) handshake() string {
	last := ""
	for _, s := range t.Sides {
		// The times are in the same format and time zone, so the text sorts by time.
		if at := text(s.HandshakeAt, ""); at > last {
			last = at
		}
	}
	if last == "" {
		return "never"
	}
	return last
}

func (a *app) printMesh(m meshState) {
	fmt.Fprintf(a.out, "Address range: %s\nUDP port:      %d\n\n", m.Range, m.Port)
	tw := tabwriter.NewWriter(a.out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "SERVER\tMESH ADDRESS\tTUNNEL ADDRESS\tPUBLIC KEY\tTUNNELS\tSTATUS")
	for _, s := range m.Servers {
		status := "in the mesh"
		switch {
		case !s.Supported:
			status = "update the agent"
		case !s.Current:
			status = "gets the new state"
		case s.Tunnels == 0:
			status = "no tunnels"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\n", s.Name, text(s.Address, "-"), text(s.TunnelAddress, "automatic"), text(s.PublicKey, "-"), s.Tunnels, status)
	}
	tw.Flush()
	fmt.Fprintln(a.out)
	if len(m.Tunnels) == 0 {
		fmt.Fprintln(a.out, "No tunnels. To make one: cf mesh connect SERVER SERVER")
		return
	}
	tw = tabwriter.NewWriter(a.out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TUNNEL\tSTATE\tLAST HANDSHAKE\tROUND TRIP")
	for _, t := range m.Tunnels {
		trip := "-"
		if t.RoundTripMs != nil {
			trip = fmt.Sprintf("%.1f ms", *t.RoundTripMs)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", t.names(), t.State, t.handshake(), trip)
	}
	tw.Flush()
}

// meshJobs sends a change to the mesh that gives one job for each server, then waits for the jobs.
func (a *app) meshJobs(ctx context.Context, method, path string, body any) error {
	var out map[string]any
	if err := a.api.Do(ctx, method, path, nil, body, &out); err != nil {
		return err
	}
	if a.json {
		return a.printJSON(out)
	}
	var jobs []client.Job
	if list, ok := out["jobs"].([]any); ok {
		for _, j := range list {
			if m, ok := j.(map[string]any); ok {
				id, _ := m["id"].(string)
				title, _ := m["title"].(string)
				jobs = append(jobs, client.Job{ID: id, Title: title})
			}
		}
	}
	if len(jobs) == 0 {
		fmt.Fprintln(a.out, "Done.")
		return nil
	}
	var failed []error
	for _, job := range jobs {
		if a.noWait {
			fmt.Fprintf(a.out, "Job %s is in the queue: %s\n", job.ID, job.Title)
			continue
		}
		// One job for each server. A failure on one server does not stop the wait for the other.
		if err := a.wait(ctx, job.ID); err != nil {
			failed = append(failed, err)
		}
	}
	return errors.Join(failed...)
}

func (a *app) mesh(ctx context.Context, sub string, args []string) error {
	var m meshState
	if err := a.api.Do(ctx, http.MethodGet, "/mesh", nil, nil, &m); err != nil {
		return err
	}
	// two gets the two servers of a command such as "cf mesh connect web-1 web-2".
	two := func() (meshServer, meshServer, error) {
		if len(args) < 2 {
			return meshServer{}, meshServer{}, errors.New("give two servers")
		}
		one, err := m.server(args[0])
		if err != nil {
			return one, one, err
		}
		other, err := m.server(args[1])
		return one, other, err
	}
	switch sub {
	case "show", "list", "status":
		if a.json {
			return a.printJSON(m)
		}
		a.printMesh(m)
		return nil
	case "set":
		fs := flag.NewFlagSet(sub, flag.ContinueOnError)
		network := fs.String("range", "", "the address range of the mesh, for example 10.207.0.0/16")
		port := fs.Int("port", 0, "the UDP port of the tunnels")
		if err := fs.Parse(args); err != nil {
			return err
		}
		body := map[string]any{}
		if *network != "" {
			body["range"] = *network
		}
		if *port != 0 {
			body["port"] = *port
		}
		if len(body) == 0 {
			return errors.New("give --range or --port")
		}
		if err := a.api.Do(ctx, http.MethodPatch, "/mesh", nil, body, &m); err != nil {
			return err
		}
		if a.json {
			return a.printJSON(m)
		}
		fmt.Fprintf(a.out, "The mesh uses the address range %s and the UDP port %d.\n", m.Range, m.Port)
		return nil
	case "connect":
		one, other, err := two()
		if err != nil {
			return err
		}
		return a.meshJobs(ctx, http.MethodPost, "/mesh/tunnels", map[string]any{"servers": []string{one.ID, other.ID}})
	case "repair", "disconnect":
		one, other, err := two()
		if err != nil {
			return err
		}
		t, err := m.tunnel(one, other)
		if err != nil {
			return err
		}
		if sub == "repair" {
			// The panel sends the state to the two servers again. Each server then tests the tunnel.
			if err := a.meshJobs(ctx, http.MethodPost, "/mesh/tunnels/"+t.ID+"/repair", nil); err != nil || a.json || a.noWait {
				return err
			}
			// The result of each job has the state of the tunnel. The panel has it now.
			if err := a.api.Do(ctx, http.MethodGet, "/mesh", nil, nil, &m); err != nil {
				return err
			}
			if t, err = m.tunnel(one, other); err != nil {
				return err
			}
			if t.State != "up" {
				return fmt.Errorf("the tunnel between %s is %s; make sure that each server accepts UDP port %d from the other server", t.names(), t.State, m.Port)
			}
			fmt.Fprintf(a.out, "The tunnel between %s is up.\n", t.names())
			return nil
		}
		return a.meshJobs(ctx, http.MethodDelete, "/mesh/tunnels/"+t.ID, nil)
	case "rotate-key":
		if err := need(args, 1, "a server"); err != nil {
			return err
		}
		s, err := m.server(args[0])
		if err != nil {
			return err
		}
		// The tunnels of the server do not operate until its peers have the new public key.
		// The panel sends the key to the peers when this job is complete.
		return a.change(ctx, http.MethodPost, "/mesh/servers/"+s.ID+"/rotate-key", nil)
	case "set-address":
		if err := need(args, 2, "a server and a public IP address, or auto"); err != nil {
			return err
		}
		s, err := m.server(args[0])
		if err != nil {
			return err
		}
		// With "auto", the panel uses the address that it connects to the agent with.
		var address any = args[1]
		if args[1] == "auto" {
			address = nil
		}
		return a.meshJobs(ctx, http.MethodPatch, "/mesh/servers/"+s.ID, map[string]any{"tunnelAddress": address})
	}
	return fmt.Errorf("unknown command mesh %s", sub)
}
