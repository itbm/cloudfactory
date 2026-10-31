package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/itbm/cloudfactory/agent/internal/actions"
	"github.com/itbm/cloudfactory/agent/internal/caddy"
	"github.com/itbm/cloudfactory/agent/internal/execx"
	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/jobs"
	"github.com/itbm/cloudfactory/agent/internal/panel"
	"github.com/itbm/cloudfactory/agent/internal/pki"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
	"github.com/itbm/cloudfactory/agent/internal/status"
)

type events struct {
	mu  sync.Mutex
	got map[string][]panel.Event
}

func (e *events) JobEvent(_ context.Context, id string, ev panel.Event) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.got[id] = append(e.got[id], ev)
	return nil
}

func cert(t *testing.T, dir, name string) (tls.Certificate, string) {
	crt, key := filepath.Join(dir, name+".crt"), filepath.Join(dir, name+".key")
	if err := pki.Generate(crt, key, []string{"127.0.0.1"}, time.Hour); err != nil {
		t.Fatal(err)
	}
	c, err := tls.LoadX509KeyPair(crt, key)
	if err != nil {
		t.Fatal(err)
	}
	fp, _, _ := pki.FileFingerprint(crt)
	return c, fp
}

// The panel sends a JSON body like this one (api/app/Services/AgentClient.php).
const panelBody = `{"jobId":"01JBQ3Z5K8N6T4W2X9Y7R5P3M2","website":{"id":"01JBQ3Z5K8N6T4W2X9Y7R5P3M1","unixUser":"acme_x7k2",
"image":"ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1","imageDigest":null,"runtime":"frankenphp","phpVersion":"8.4",
"workerMode":false,"extensions":["opcache"],"phpIni":{"memory_limit":"256M"},
"limits":{"vcpus":2,"memoryBytes":2147483648,"diskBytes":21474836480,"diskIoBytesPerSecond":52428800,"processes":100},
"domains":[{"name":"acme.example","kind":"primary"}]},"previousImageId":null,"futureField":1}`

func TestOnlyThePanelCertificateIsAccepted(t *testing.T) {
	dir := t.TempDir()
	agentCert, _ := cert(t, dir, "agent")
	panelCert, panelFP := cert(t, dir, "panel")
	otherCert, _ := cert(t, dir, "other")

	run := &execx.Fake{}
	h := &host.Host{Run: run, SitesDir: filepath.Join(dir, "sites"), UnitRoot: filepath.Join(dir, "units"), ConfigRoot: filepath.Join(dir, "config"), LookupUID: func(string) (int, error) { return 1001, nil }}
	ev := &events{got: map[string][]panel.Event{}}
	runner := &jobs.Runner{Dir: filepath.Join(dir, "jobs"), Reporter: ev}
	deps := &actions.Deps{Host: h, Run: run, Edge: nopEdge{}, State: actions.NewState(dir), Gvisor: quadlet.Runtime{RunscPath: "runsc", Platform: "systrap"},
		Health: func(context.Context, int, string) (int, error) { return 200, nil }, DNSPointsHere: func(context.Context, string) bool { return false }}
	svc := &Service{Deps: deps, Runner: runner, Status: status.Collector{Run: run}}

	ln, err := tls.Listen("tcp", "127.0.0.1:0", TLSConfig(agentCert, panelFP))
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: Handler(svc)}
	go srv.Serve(ln)
	defer srv.Close()
	url := "https://" + ln.Addr().String() + "/cloudfactory.agent.v1.AgentService/CreateWebsite"

	client := func(c *tls.Certificate) *http.Client {
		// The panel trusts only the certificate that the agent sent at join.
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: agentCert.Certificate[0]}))
		cfg := &tls.Config{RootCAs: pool}
		if c != nil {
			cfg.Certificates = []tls.Certificate{*c}
		}
		return &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}, Timeout: 5 * time.Second}
	}
	post := func(c *http.Client) (*http.Response, error) {
		req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(panelBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Connect-Protocol-Version", "1")
		return c.Do(req)
	}

	if _, err := post(client(nil)); err == nil {
		t.Error("a client with no certificate was accepted")
	}
	if _, err := post(client(&otherCert)); err == nil {
		t.Error("a client with a different certificate was accepted")
	}

	resp, err := post(client(&panelCert))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(`"jobId":"01JBQ3Z5K8N6T4W2X9Y7R5P3M2"`)) {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	runner.Wait()
	got := ev.got["01JBQ3Z5K8N6T4W2X9Y7R5P3M2"]
	if len(got) == 0 || got[len(got)-1].State != "succeeded" {
		t.Errorf("events = %+v", got)
	}

	// The same job again is a duplicate and does not run again.
	resp, _ = post(client(&panelCert))
	body, _ = io.ReadAll(resp.Body)
	var accepted struct{ Duplicate bool }
	json.Unmarshal(body, &accepted)
	if !accepted.Duplicate {
		t.Errorf("second call: %s", body)
	}

	// A bad value gets "invalid_argument" and no job.
	bad := strings.Replace(panelBody, `"unixUser":"acme_x7k2"`, `"unixUser":"root"`, 1)
	bad = strings.Replace(bad, "P3M2", "P3M9", 1)
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(bad))
	req.Header.Set("Content-Type", "application/json")
	resp, err = client(&panelCert).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadRequest || !bytes.Contains(body, []byte("invalid_argument")) {
		t.Errorf("status %d: %s", resp.StatusCode, body)
	}
}

type nopEdge struct{}

func (nopEdge) EnsureBase(context.Context) error        { return nil }
func (nopEdge) Apply(context.Context, caddy.Site) error { return nil }
func (nopEdge) Remove(context.Context, string) error    { return nil }
