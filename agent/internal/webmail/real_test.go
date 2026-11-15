package webmail_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/itbm/cloudfactory/agent/internal/caddy"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
	"github.com/itbm/cloudfactory/agent/internal/stalwart"
	"github.com/itbm/cloudfactory/agent/internal/webmail"
)

// TestRealWebmail starts a mail server, the edge proxy and the webmail with Docker, with the
// routes and the settings of this package. It needs the three images:
//
//	podman build -t cf-webmail:test images/webmail
//	CF_TEST_WEBMAIL=1 CF_TEST_MAIL_IMAGE=cf-mail:test CF_TEST_EDGE_IMAGE=caddy:2.11-alpine \
//	CF_TEST_WEBMAIL_IMAGE=cf-webmail:test \
//	go test ./internal/webmail/ -run TestRealWebmail -v -timeout 10m
//
// With CF_TEST_WEBMAIL_KEEP, the containers stay after the test, for a browser: the edge
// proxy listens on 127.0.0.1:18443 for the host name mail.cf.test.
func TestRealWebmail(t *testing.T) {
	if os.Getenv("CF_TEST_WEBMAIL") == "" {
		t.Skip("CF_TEST_WEBMAIL is not set")
	}
	const (
		network  = "cf-webmail-test"
		hostname = "mail.cf.test"
		admin    = "secret123"
		domain   = "webmail-test.example.com"
		// password and hash are a mailbox password and its bcrypt hash, as the panel sends it.
		password, hash = "Testpass-123456", "$2y$12$Wlrsi0RSciVGEgjvW9gO5Od8pb68WpSyRgIWT2idZjFTm7JeR4AW2"
	)
	docker := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	remove := func() {
		for _, c := range []string{"cfwm-webmail", "cfwm-mail", "cfwm-edge"} {
			_ = exec.Command("docker", "rm", "-f", "-v", c).Run()
		}
		_ = exec.Command("docker", "network", "rm", network).Run()
	}
	remove()
	if os.Getenv("CF_TEST_WEBMAIL_KEEP") == "" {
		t.Cleanup(remove)
	}
	docker("network", "create", "--subnet", "10.77.26.0/24", network)
	dir, err := os.MkdirTemp("", "cfwm")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"mail", "certs", "admin", "webmail"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o777); err != nil {
			t.Fatal(err)
		}
		_ = os.Chmod(filepath.Join(dir, d), 0o777)
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	waitFor := func(what string, limit time.Duration, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(limit)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatalf("timeout: %s", what)
			}
			time.Sleep(2 * time.Second)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	// The edge proxy. The test uses the address of the mail container in place of the private
	// service address, and the ports of the containers in place of the ports on that address.
	docker("run", "-d", "--name", "cfwm-edge", "--network", network, "--ip", "10.77.26.2",
		"-v", filepath.Join(dir, "admin")+":/run/caddy-admin", "-p", "127.0.0.1:18443:8443",
		os.Getenv("CF_TEST_EDGE_IMAGE"), "caddy", "run", "--resume=false", "--config", "/dev/null")
	edge := caddy.New("unix://" + filepath.Join(dir, "admin", "admin.sock"))
	edge.Upstream = "10.77.26.3"
	// Caddy with no configuration listens for its admin API on localhost:2019. Load the
	// base configuration through that port in the container, then use the socket.
	waitFor("the edge proxy answers", time.Minute, func() bool {
		base, _ := json.Marshal(caddy.BaseConfig())
		cmd := exec.Command("docker", "exec", "-i", "cfwm-edge", "wget", "-q", "-O-", "--header", "Content-Type: application/json", "--post-file", "/dev/stdin", "http://localhost:2019/load")
		cmd.Stdin = strings.NewReader(string(base))
		return cmd.Run() == nil
	})
	waitFor("the admin socket", time.Minute, func() bool { return edge.EnsureBase(ctx) == nil })
	// On a server, the firewall sends port 443 to port 8443, also for a connection from a
	// container on the server to the public address. Here the edge proxy listens on both.
	listen, _ := http.NewRequestWithContext(ctx, http.MethodPatch, edge.Admin+"/config/apps/http/servers/edge/listen", strings.NewReader(`[":8443", ":443"]`))
	listen.Header.Set("Content-Type", "application/json")
	if res, err := edge.HTTP.Do(listen); err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("listen on 443: %v %v", res, err)
	}

	// The mail server, first in the recovery mode to apply the settings.
	write("mail/config.json", quadlet.MailConfig)
	if out, err := exec.Command("openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256", "-nodes", "-days", "1", "-subj", "/CN="+hostname,
		"-keyout", filepath.Join(dir, "certs", "tls.key"), "-out", filepath.Join(dir, "certs", "tls.crt")).CombinedOutput(); err != nil {
		t.Fatalf("openssl: %v\n%s", err, out)
	}
	for _, f := range []string{"tls.key", "tls.crt"} {
		_ = os.Chmod(filepath.Join(dir, "certs", f), 0o644)
	}
	mail := func(recovery bool) {
		_ = exec.Command("docker", "rm", "-f", "cfwm-mail").Run()
		args := []string{"run", "-d", "--name", "cfwm-mail", "--network", network, "--ip", "10.77.26.3",
			"--add-host", hostname + ":10.77.26.2", "-p", "127.0.0.1:18025:8080",
			"-v", filepath.Join(dir, "mail", "config.json") + ":/etc/stalwart/config.json:ro",
			"-v", "cfwm-data:/var/lib/stalwart", "-v", filepath.Join(dir, "certs") + ":/etc/stalwart-certs:ro",
			"-e", "STALWART_RECOVERY_ADMIN=" + stalwart.AdminUser + ":" + admin}
		if recovery {
			args = append(args, "-e", "STALWART_RECOVERY_MODE=1")
		}
		docker(append(args, os.Getenv("CF_TEST_MAIL_IMAGE"))...)
	}
	_ = exec.Command("docker", "volume", "rm", "cfwm-data").Run()
	mail(true)
	client := &stalwart.Client{URL: "http://127.0.0.1:18025", Password: admin}
	waitFor("the mail server answers", 2*time.Minute, func() bool { return client.Ready(ctx) == nil })
	settings := stalwart.Settings{Hostname: hostname, Ports: stalwart.Ports{SMTP: 2025, Submissions: 2465, Submission: 2587, IMAPS: 2993, HTTP: 8080}, CertFile: quadlet.MailCertFile, KeyFile: quadlet.MailKeyFile}
	if _, err := client.Configure(ctx, settings); err != nil {
		t.Fatal(err)
	}
	mail(false)
	waitFor("the mail server answers again", 2*time.Minute, func() bool { return client.Ready(ctx) == nil })
	if _, err := client.SetDomain(ctx, domain); err != nil {
		t.Fatal(err)
	}
	if err := client.SetMailbox(ctx, domain, "info", 1<<30, hash); err != nil {
		t.Fatal(err)
	}
	// With CF_TEST_WEBMAIL_PUBLIC_KEY, the mail server encrypts the new mail of the mailbox
	// with this OpenPGP key, for a check of the private key in the webmail.
	if file := os.Getenv("CF_TEST_WEBMAIL_PUBLIC_KEY"); file != "" {
		key, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.SetEncryption(ctx, domain, "info", string(key)); err != nil {
			t.Fatal(err)
		}
	}

	// The routes of the host name. The edge proxy signs the certificate of a local name with
	// its own CA.
	if err := edge.Apply(ctx, webmail.Routes(hostname, webmail.ContainerPort, quadlet.MailHTTPPort)); err != nil {
		t.Fatal(err)
	}
	https := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true, ServerName: hostname}, //nolint:gosec // the CA of the test edge proxy.
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, "127.0.0.1:18443")
		},
	}}
	var session struct {
		APIURL          string            `json:"apiUrl"`
		DownloadURL     string            `json:"downloadUrl"`
		PrimaryAccounts map[string]string `json:"primaryAccounts"`
	}
	waitFor("JMAP through the edge proxy", 2*time.Minute, func() bool {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+hostname+"/.well-known/jmap", nil)
		req.SetBasicAuth("info@"+domain, password)
		res, err := https.Do(req)
		if err != nil {
			t.Log(err)
			return false
		}
		defer res.Body.Close()
		return res.StatusCode == http.StatusOK && json.NewDecoder(res.Body).Decode(&session) == nil
	})
	t.Logf("session: api %s, download %s", session.APIURL, session.DownloadURL)
	if !strings.HasPrefix(session.APIURL, "https://"+hostname+"/jmap") {
		t.Errorf("the API URL %q is not on the host name of the webmail", session.APIURL)
	}
	// The management API is not on the public host name.
	res, err := https.Get("https://" + hostname + "/api/principal")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadGateway && res.StatusCode != http.StatusNotFound && res.StatusCode != http.StatusServiceUnavailable {
		// Without the webmail, the edge proxy cannot connect: 502. The webmail answers 404.
		t.Logf("GET /api/principal before the webmail: %d", res.StatusCode)
	}

	// The webmail. It trusts the CA of the edge proxy, as on a server with a local host name.
	root := docker("exec", "cfwm-edge", "sh", "-c", "cat /data/caddy/pki/authorities/*/root.crt")
	if pool := x509.NewCertPool(); !pool.AppendCertsFromPEM([]byte(root)) {
		t.Fatalf("the edge proxy has no root certificate: %s", root)
	}
	write("webmail/ca.crt", root+"\n")
	env, err := webmail.Env(hostname, "0123456789abcdef0123456789abcdef0123456789abcdef", "fedcba9876543210fedcba9876543210fedcba9876543210", "/etc/cf-webmail/ca.crt")
	if err != nil {
		t.Fatal(err)
	}
	write("webmail/webmail.env", env)
	docker("run", "-d", "--name", "cfwm-webmail", "--network", "container:cfwm-mail",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--read-only", "--tmpfs", "/tmp",
		// The data of the webmail stays when the container starts again, as in the directory
		// of the data on a server.
		"-v", webmail.DataDir,
		"-v", filepath.Join(dir, "webmail", "ca.crt")+":/etc/cf-webmail/ca.crt:ro",
		"--env-file", filepath.Join(dir, "webmail", "webmail.env"),
		os.Getenv("CF_TEST_WEBMAIL_IMAGE"))
	var page string
	waitFor("the webmail through the edge proxy", 3*time.Minute, func() bool {
		res, err := https.Get("https://" + hostname + "/")
		if err != nil {
			return false
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		page = string(b)
		return res.StatusCode < 500 && strings.Contains(page, "<html")
	})
	// The management API stays off the public host name: the webmail answers that path.
	res, err = https.Get("https://" + hostname + "/api/principal")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if strings.Contains(string(body), "principal") && res.StatusCode == http.StatusOK {
		t.Errorf("the management API is on the public host name: %s", body)
	}
	// The webmail examines a sign-in on the mail server through the edge proxy.
	ready, err := https.Get("https://" + hostname + "/api/ready")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(ready.Body)
	ready.Body.Close()
	t.Logf("ready: %d %s", ready.StatusCode, b)
	if ready.StatusCode != http.StatusOK {
		t.Errorf("the webmail is not ready: %d %s\n%s", ready.StatusCode, b, docker("logs", "--tail", "40", "cfwm-webmail"))
	}
	// The start script of the image installs the PGP plugin through the admin dashboard.
	waitFor("the PGP plugin is installed", 2*time.Minute, func() bool {
		return strings.Contains(docker("logs", "cfwm-webmail"), "cf-webmail: installed the PGP plugin")
	})
	// The edge proxy refuses the admin dashboard and the setup of the webmail.
	for _, path := range []string{"/admin", "/admin/plugins", "/api/admin/auth", "/API/Admin/plugins", "/api/admin/plugins", "/setup", "/api/setup/finish", "/api/admin/plugin-approvals"} {
		res, err := https.Get("https://" + hostname + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s through the edge proxy: %d, not 404", path, res.StatusCode)
		}
	}
	// The users get the code of the plugin. A request with a different method is refused.
	bundle := "https://" + hostname + "/api/admin/plugins/pgp-true-end-to-end/bundle"
	res, err = https.Get(bundle)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		t.Errorf("GET the code of the plugin through the edge proxy: %d", res.StatusCode)
	}
	if res, err = https.Post(bundle, "text/plain", strings.NewReader("x")); err != nil || res.StatusCode != http.StatusNotFound {
		t.Errorf("POST to the code of the plugin through the edge proxy: %v %v", res.StatusCode, err)
	} else {
		res.Body.Close()
	}
	// A new start keeps the plugin, and does not install it again.
	docker("restart", "cfwm-webmail")
	waitFor("the webmail starts again", 2*time.Minute, func() bool {
		return strings.Contains(docker("logs", "--since", "2m", "cfwm-webmail"), "cf-webmail: the PGP plugin 2.0.2 is installed")
	})
	fmt.Fprintln(os.Stderr, "webmail answers at https://"+hostname+" (127.0.0.1:18443)")
}
