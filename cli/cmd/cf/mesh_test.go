package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// meshPanel answers like the panel API for a mesh of three servers with one tunnel between
// web-1 and web-2. It records each change as "METHOD path body". state is the state of the
// tunnel.
func meshPanel(t *testing.T, state string) (*httptest.Server, *[]string) {
	var calls []string
	one := `{"id":"J1","title":"Set the tunnels of web-1","action":"SetMesh","state":"succeeded","steps":["Set tunnels and firewall","Check tunnels"],"step":2,"error":null}`
	two := `{"id":"J2","title":"Set the tunnels of web-2","action":"SetMesh","state":"failed","steps":["Set tunnels and firewall","Check tunnels"],"step":0,"error":"the agent does not answer"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodGet {
			calls = append(calls, strings.TrimSpace(r.Method+" "+r.URL.Path+" "+string(body)))
		}
		mesh := `{"range":"10.207.0.0/16","port":51820,"servers":[` +
			`{"id":"S1","name":"web-1","supported":true,"publicKey":"KEY1","address":"10.207.0.1","tunnelAddress":"203.0.113.10","tunnels":1,"current":true},` +
			`{"id":"S2","name":"web-2","supported":true,"publicKey":"KEY2","address":"10.207.0.2","tunnelAddress":null,"tunnels":1,"current":true},` +
			`{"id":"S3","name":"web-3","supported":false,"publicKey":null,"address":null,"tunnelAddress":null,"tunnels":0,"current":true}],` +
			`"tunnels":[{"id":"T1","state":"` + state + `","roundTripMs":7.55,"pinned":true,"inUse":true,"sides":[` +
			`{"serverId":"S1","server":"web-1","reachable":true,"handshakeAt":"2026-11-14T09:59:20+00:00","roundTripMs":7.4},` +
			`{"serverId":"S2","server":"web-2","reachable":true,"handshakeAt":"2026-11-14T09:59:40+00:00","roundTripMs":7.55}]}]}`
		switch {
		case r.URL.Path == "/api/v1/mesh" && r.Method == http.MethodGet:
			w.Write([]byte(mesh))
		case r.URL.Path == "/api/v1/mesh" && r.Method == http.MethodPatch:
			w.WriteHeader(http.StatusUnprocessableEntity)
			w.Write([]byte(`{"message":"invalid","errors":{"range":["The mesh has tunnels. Remove all tunnels before you change the address range or the port."]}}`))
		case r.URL.Path == "/api/v1/mesh/tunnels" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"tunnel":{"id":"T2"},"jobs":[` + one + `]}`))
		case r.URL.Path == "/api/v1/mesh/tunnels/T1/repair":
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"jobs":[` + one + `]}`))
		case r.URL.Path == "/api/v1/mesh/tunnels/T1" && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"jobs":[` + two + `,` + one + `]}`))
		case r.URL.Path == "/api/v1/mesh/servers/S1/rotate-key":
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"job":{"id":"J3","title":"Make a new tunnel key on web-1","state":"queued"}}`))
		case r.URL.Path == "/api/v1/mesh/servers/S2" && r.Method == http.MethodPatch:
			w.Write([]byte(mesh[:len(mesh)-1] + `,"jobs":[]}`))
		case r.URL.Path == "/api/v1/jobs/J1":
			w.Write([]byte(`{"data":` + one + `}`))
		case r.URL.Path == "/api/v1/jobs/J3":
			w.Write([]byte(`{"data":` + strings.Replace(one, "Set the tunnels of web-1", "Make a new tunnel key on web-1", 1) + `}`))
		case r.URL.Path == "/api/v1/jobs/J2":
			w.Write([]byte(`{"data":` + two + `}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message":"Not found."}`))
		}
	}))
	return srv, &calls
}

func TestMeshShow(t *testing.T) {
	srv, _ := meshPanel(t, "up")
	defer srv.Close()
	a, out := testApp(srv)

	if err := a.dispatch(context.Background(), []string{"mesh", "show"}); err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, l := range strings.Split(out.String(), "\n") {
		lines = append(lines, row(l))
	}
	for _, want := range []string{
		"Address range: 10.207.0.0/16",
		"UDP port: 51820",
		"web-1 10.207.0.1 203.0.113.10 KEY1 1 in the mesh",
		"web-2 10.207.0.2 automatic KEY2 1 in the mesh",
		"web-3 - automatic - 0 update the agent",
		// The most recent handshake of the two servers.
		"web-1 and web-2 up 2026-11-14T09:59:40+00:00 7.5 ms administrator, in use",
	} {
		if !slices.Contains(lines, want) {
			t.Errorf("no line %q in:\n%s", want, out)
		}
	}
}

func TestMeshChanges(t *testing.T) {
	cases := []struct {
		args []string
		call string
		out  string
	}{
		{[]string{"mesh", "connect", "web-1", "web-3"}, `POST /api/v1/mesh/tunnels {"servers":["S1","S3"]}`, "Set the tunnels of web-1: complete."},
		// The servers of a tunnel can be in the two sequences.
		{[]string{"mesh", "repair", "web-2", "web-1"}, "POST /api/v1/mesh/tunnels/T1/repair", "The tunnel between web-1 and web-2 is up."},
		{[]string{"mesh", "rotate-key", "S1"}, "POST /api/v1/mesh/servers/S1/rotate-key", "Make a new tunnel key on web-1: complete."},
		{[]string{"mesh", "set-address", "web-2", "198.51.100.7"}, `PATCH /api/v1/mesh/servers/S2 {"tunnelAddress":"198.51.100.7"}`, "Done."},
		{[]string{"mesh", "set-address", "web-2", "auto"}, `PATCH /api/v1/mesh/servers/S2 {"tunnelAddress":null}`, "Done."},
	}
	for _, c := range cases {
		srv, calls := meshPanel(t, "up")
		a, out := testApp(srv)
		if err := a.dispatch(context.Background(), c.args); err != nil {
			t.Errorf("%v: %v", c.args, err)
		}
		if !slices.Equal(*calls, []string{c.call}) {
			t.Errorf("%v: calls %q, want %q", c.args, *calls, c.call)
		}
		if !strings.Contains(out.String(), c.out) {
			t.Errorf("%v: no %q in:\n%s", c.args, c.out, out)
		}
		srv.Close()
	}
}

func TestMeshRepairTellsWhenTheTunnelIsDown(t *testing.T) {
	srv, _ := meshPanel(t, "down")
	defer srv.Close()
	a, _ := testApp(srv)

	err := a.dispatch(context.Background(), []string{"mesh", "repair", "web-1", "web-2"})
	if err == nil || !strings.Contains(err.Error(), "the tunnel between web-1 and web-2 is down") || !strings.Contains(err.Error(), "UDP port 51820") {
		t.Errorf("error: %v", err)
	}
}

func TestMeshWaitsForEachServer(t *testing.T) {
	srv, calls := meshPanel(t, "up")
	defer srv.Close()
	a, out := testApp(srv)

	// The job of web-2 fails. The command waits for the job of web-1 also, then gives the failure.
	err := a.dispatch(context.Background(), []string{"mesh", "disconnect", "web-1", "web-2"})
	if err == nil || !strings.Contains(err.Error(), "Set the tunnels of web-2: the agent does not answer") {
		t.Errorf("error: %v", err)
	}
	if !strings.Contains(out.String(), "Set the tunnels of web-1: complete.") {
		t.Errorf("output:\n%s", out)
	}
	if !slices.Equal(*calls, []string{"DELETE /api/v1/mesh/tunnels/T1"}) {
		t.Errorf("calls: %q", *calls)
	}
}

func TestMeshErrors(t *testing.T) {
	srv, calls := meshPanel(t, "up")
	defer srv.Close()
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"mesh", "connect", "web-1"}, "give two servers"},
		{[]string{"mesh", "connect", "web-1", "web-9"}, "there is no server web-9"},
		{[]string{"mesh", "repair", "web-1", "web-3"}, "there is no tunnel between web-1 and web-3"},
		{[]string{"mesh", "set"}, "give --range or --port"},
		{[]string{"mesh", "set", "--range", "10.99.0.0/16"}, "Remove all tunnels before you change the address range or the port"},
		{[]string{"mesh", "start"}, "unknown command mesh start"},
	}
	for _, c := range cases {
		a, _ := testApp(srv)
		if err := a.dispatch(context.Background(), c.args); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: error %v, want %q", c.args, err, c.want)
		}
	}
	if !slices.Equal(*calls, []string{`PATCH /api/v1/mesh {"range":"10.99.0.0/16"}`}) {
		t.Errorf("calls: %q", *calls)
	}
}
