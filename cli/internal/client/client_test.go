package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoopbackAddress(t *testing.T) {
	cases := map[string]string{
		"panel.localhost:443":      "localhost:443",
		"Panel.LOCALHOST.:9443":    "localhost:9443",
		"localhost:8080":           "localhost:8080",
		"panel.example.com:443":    "panel.example.com:443",
		"notlocalhost:443":         "notlocalhost:443",
		"localhost.example.com:80": "localhost.example.com:80",
		"[::1]:443":                "[::1]:443",
		"no-port":                  "no-port",
	}
	for in, want := range cases {
		if got := loopbackAddress(in); got != want {
			t.Errorf("loopbackAddress(%q) = %q, want %q", in, got, want)
		}
	}
}

// A panel with the name panel.localhost is on the same computer. The DNS does not have this name.
func TestPanelWithALocalhostName(t *testing.T) {
	var host string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host = r.Host
		w.Write([]byte(`{"name":"web-1"}`))
	}))
	defer srv.Close()
	// A proxy cannot connect to the loopback address of this computer.
	t.Setenv("HTTP_PROXY", "http://192.0.2.1:9")
	t.Setenv("NO_PROXY", "")
	port := srv.URL[strings.LastIndex(srv.URL, ":"):]
	c := New("http://panel.localhost"+port, "token")
	var out struct{ Name string }
	if err := c.Do(context.Background(), http.MethodGet, "/servers/S1", nil, nil, &out); err != nil {
		t.Fatal(err)
	}
	if out.Name != "web-1" || host != "panel.localhost"+port {
		t.Errorf("name = %q, host = %q", out.Name, host)
	}
}
