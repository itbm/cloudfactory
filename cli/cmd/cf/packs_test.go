package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// packPanel answers like the panel API for the packs. It records each change as
// "METHOD path body".
func packPanel(t *testing.T) (*httptest.Server, *[]string) {
	var calls []string
	const golang = `{"id":"PK1","slug":"golang","name":"Go","version":"1.0.0","publisher":"Example Packs Ltd","signed":true,` +
		`"signedBy":{"name":"Example Packs Ltd","fingerprint":"ab12","release":false},` +
		`"runtime":{"contract":"cf-runtime/1","type":"app","defaultCommand":"./server","defaultInstall":"go build -o server ."},` +
		`"images":[{"id":"I1","version":"1.27","reference":"ghcr.io/example/cf-golang","tag":"1.27","digest":"sha256:127","availableDigest":null,"supportEndsAt":"2027-08-31","websites":2},` +
		`{"id":"I2","version":"1.26","reference":"ghcr.io/example/cf-golang","tag":"1.26","digest":"sha256:126","availableDigest":"sha256:126b","supportEndsAt":null,"websites":0}],` +
		`"servers":[{"id":"S1","name":"web-1","version":"1.0.0","state":"installed"},{"id":"S2","name":"web-2","version":null,"state":"failing"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodGet {
			calls = append(calls, strings.TrimSpace(r.Method+" "+r.URL.Path+" "+string(body)))
		}
		switch path := strings.TrimPrefix(r.URL.Path, "/api/v1"); {
		case path == "/packs" && r.Method == http.MethodGet:
			w.Write([]byte(`{"data":[` + golang + `]}`))
		case path == "/packs/preview":
			w.Write([]byte(`{"manifest":{"slug":"golang","name":"Go","version":"1.1.0","publisher":"Example Packs Ltd","runtime":{"contract":"cf-runtime/1","type":"app","defaultCommand":"./server","defaultInstall":""},` +
				`"images":[{"version":"1.27","ref":"ghcr.io/example/cf-golang:1.27","digest":"sha256:127"}]},"signedBy":null,"installed":{"id":"PK1","version":"1.0.0"},"developerMode":false}`))
		case path == "/packs" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(golang))
		case path == "/packs/PK1" && r.Method == http.MethodPut:
			w.Write([]byte(strings.Replace(golang, `"version":"1.0.0","publisher"`, `"version":"1.1.0","publisher"`, 1)))
		case path == "/packs/PK1" && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case path == "/packs/sync":
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"servers":3}`))
		case path == "/pack-settings":
			w.Write([]byte(`{"developerMode":true}`))
		case path == "/trusted-keys" && r.Method == http.MethodGet:
			w.Write([]byte(`{"data":[{"id":"K1","name":"Example Packs Ltd","fingerprint":"ab12"}]}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message":"Not found."}`))
		}
	}))
	return srv, &calls
}

// The CLI sends the exact bytes of the manifest, with the trailing new line: the signature is
// for these bytes.
func TestPacksInstallSendsTheExactManifest(t *testing.T) {
	srv, calls := packPanel(t)
	defer srv.Close()
	a, out := testApp(srv)
	ctx := context.Background()
	t.Setenv("CF_PASSWORD", "secret")
	manifest := filepath.Join(t.TempDir(), "pack.json")
	os.WriteFile(manifest, []byte("{\"contract\":\"cf-pack/1\"}\n"), 0o600)
	os.WriteFile(manifest+".sig", []byte("c2lnbmF0dXJl\n"), 0o600)

	if err := a.dispatch(ctx, []string{"packs", "install", "--manifest", manifest, "--totp", "123456"}); err != nil {
		t.Fatal(err)
	}
	want := `POST /api/v1/packs {"manifest":"{\"contract\":\"cf-pack/1\"}\n","password":"secret","signature":"c2lnbmF0dXJl","totp":"123456"}`
	if len(*calls) != 1 || (*calls)[0] != want {
		t.Errorf("calls:\n%v\nwant:\n%s", *calls, want)
	}
	for _, s := range []string{
		"The pack Go 1.0.0 is installed.",
		"Publisher:     Example Packs Ltd, signed by Example Packs Ltd (SHA-256 ab12)",
		"Runtime:       cf-runtime/1, type app",
		"Image:         1.27  ghcr.io/example/cf-golang:1.27@sha256:127  websites 2  support ends 2027-08-31",
		"Image:         1.26  ghcr.io/example/cf-golang:1.26@sha256:126  websites 0  new digest waits for a rollout",
		"Server:        web-2 (failing)",
	} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("no %q in:\n%s", s, out)
		}
	}
}

func TestPacksPreviewChangesNothing(t *testing.T) {
	srv, calls := packPanel(t)
	defer srv.Close()
	a, out := testApp(srv)
	manifest := filepath.Join(t.TempDir(), "pack.json")
	os.WriteFile(manifest, []byte(`{}`), 0o600)

	if err := a.dispatch(context.Background(), []string{"packs", "preview", "--manifest", manifest}); err != nil {
		t.Fatal(err)
	}
	// The preview needs no password.
	if len(*calls) != 1 || (*calls)[0] != `POST /api/v1/packs/preview {"manifest":"{}"}` {
		t.Errorf("calls: %v", *calls)
	}
	for _, s := range []string{"Pack:          Go 1.1.0 (golang)", "no signature (developer mode)", "Installed:     version 1.0.0. Use cf packs upgrade golang.", "only in the developer mode for packs"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("no %q in:\n%s", s, out)
		}
	}
}

func TestPacksListUpgradeRemoveAndSync(t *testing.T) {
	srv, calls := packPanel(t)
	defer srv.Close()
	a, out := testApp(srv)
	ctx := context.Background()
	t.Setenv("CF_PASSWORD", "secret")
	manifest := filepath.Join(t.TempDir(), "pack.json")
	os.WriteFile(manifest, []byte(`{"version":"1.1.0"}`), 0o600)

	if err := a.dispatch(ctx, []string{"packs", "list"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "golang") || !strings.Contains(out.String(), "1.27, 1.26") {
		t.Errorf("output:\n%s", out)
	}
	if err := a.dispatch(ctx, []string{"packs", "upgrade", "Golang", "--manifest", manifest}); err != nil {
		t.Fatal(err)
	}
	if err := a.dispatch(ctx, []string{"packs", "remove", "PK1"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := a.dispatch(ctx, []string{"packs", "sync"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "to 3 web servers again") {
		t.Errorf("output:\n%s", out)
	}
	want := []string{
		`PUT /api/v1/packs/PK1 {"manifest":"{\"version\":\"1.1.0\"}","password":"secret"}`,
		"DELETE /api/v1/packs/PK1",
		"POST /api/v1/packs/sync",
	}
	if strings.Join(*calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s", strings.Join(*calls, "\n"))
	}
	if err := a.dispatch(ctx, []string{"packs", "get", "python"}); err == nil || !strings.Contains(err.Error(), "there is no pack python") {
		t.Errorf("error: %v", err)
	}
}

func TestPacksDeveloperModeAndKeys(t *testing.T) {
	srv, calls := packPanel(t)
	defer srv.Close()
	a, out := testApp(srv)
	ctx := context.Background()
	t.Setenv("CF_PASSWORD", "secret")

	if err := a.dispatch(ctx, []string{"packs", "developer-mode", "on"}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0] != `PUT /api/v1/pack-settings {"developerMode":true,"password":"secret"}` {
		t.Errorf("calls: %v", *calls)
	}
	if err := a.dispatch(ctx, []string{"packs", "developer-mode", "maybe"}); err == nil {
		t.Error("no error for maybe")
	}
	out.Reset()
	if err := a.dispatch(ctx, []string{"packs", "keys", "list"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Example Packs Ltd") {
		t.Errorf("output:\n%s", out)
	}
}

// A plugin cannot add a command with the name of the pack commands.
func TestPacksIsABuiltInCommand(t *testing.T) {
	for _, name := range []string{"packs", "pack"} {
		if !builtin[name] {
			t.Errorf("%s is not in the built-in commands", name)
		}
	}
}
