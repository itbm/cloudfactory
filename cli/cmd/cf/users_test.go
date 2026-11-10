package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/itbm/cloudfactory/cli/internal/client"
)

// usersPanel answers like the panel API for users, customers, sign-in and restore. Each call
// is "METHOD path body".
func usersPanel(t *testing.T) (*httptest.Server, *[]string) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, strings.TrimSpace(r.Method+" "+strings.TrimPrefix(r.URL.Path, "/api/v1")+" "+string(body)))
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + strings.TrimPrefix(r.URL.Path, "/api/v1") {
		case "POST /auth/login":
			w.Write([]byte(`{"token":"session-token","expiresAt":"2026-11-11T08:00:00+00:00"}`))
		case "GET /me":
			w.Write([]byte(`{"id":7,"email":"new@acme.example","mustChangePassword":true}`))
		case "POST /auth/logout":
			if r.Header.Get("Authorization") != "Bearer session-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case "GET /users":
			w.Write([]byte(`{"data":[{"id":1,"name":"Admin","email":"admin@panel.example","role":"administrator","customerId":null,"totpEnabled":true,"mustChangePassword":false},{"id":7,"name":"New","email":"new@acme.example","role":"owner","customerId":"C1","totpEnabled":false,"mustChangePassword":true}]}`))
		case "POST /users":
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":8,"name":"Kim","email":"kim@acme.example","role":"owner","customerId":"C1","temporaryPassword":"TempPassword12345678"}`))
		case "POST /users/7/reset-password":
			w.Write([]byte(`{"id":7,"email":"new@acme.example","temporaryPassword":"ResetPassword1234567"}`))
		case "DELETE /users/7":
			w.WriteHeader(http.StatusNoContent)
		case "POST /customers":
			var in map[string]any
			json.Unmarshal(body, &in)
			w.WriteHeader(http.StatusCreated)
			if in["createUser"] == true {
				w.Write([]byte(`{"id":"C2","name":"Oak Lane","user":{"id":9,"email":"owner@oaklane.example","temporaryPassword":"FirstPassword1234567"}}`))
				return
			}
			w.Write([]byte(`{"id":"C2","name":"Oak Lane"}`))
		case "GET /websites/W1":
			w.Write([]byte(`{"data":{"id":"W1","name":"acme.example"}}`))
		case "POST /websites/W1/snapshots/abc123/restore":
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"job":null}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message":"Not found."}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func usersApp(srv *httptest.Server) (*app, *bytes.Buffer) {
	var out bytes.Buffer
	return &app{out: &out, err: &out, api: client.New(srv.URL, "cft_test"), poll: time.Millisecond}, &out
}

func last(calls *[]string) string { return (*calls)[len(*calls)-1] }

// Audit finding B5: the CLI makes users and shows the temporary password one time.
func TestUsersCreateShowsTheTemporaryPassword(t *testing.T) {
	srv, calls := usersPanel(t)
	a, out := usersApp(srv)
	ctx := context.Background()
	if err := a.dispatch(ctx, []string{"users", "create", "--name", "Kim", "--email", "kim@acme.example", "--customer", "C1"}); err != nil {
		t.Fatal(err)
	}
	if got := last(calls); got != `POST /users {"customerId":"C1","email":"kim@acme.example","name":"Kim"}` {
		t.Errorf("call = %s", got)
	}
	if !strings.Contains(out.String(), "Temporary password for kim@acme.example: TempPassword12345678") {
		t.Errorf("output:\n%s", out)
	}
	// An administrator has no customer. The API needs the field with the value null.
	if err := a.dispatch(ctx, []string{"users", "create", "--name", "Root", "--email", "root@panel.example", "--administrator"}); err != nil {
		t.Fatal(err)
	}
	if got := last(calls); got != `POST /users {"customerId":null,"email":"root@panel.example","name":"Root"}` {
		t.Errorf("call = %s", got)
	}
	n := len(*calls)
	for _, args := range [][]string{
		{"users", "create", "--name", "X", "--email", "x@acme.example"},
		{"users", "create", "--name", "X", "--email", "x@acme.example", "--customer", "C1", "--administrator"},
	} {
		if err := a.dispatch(ctx, args); err == nil {
			t.Errorf("%v was accepted", args)
		}
	}
	if len(*calls) != n {
		t.Errorf("an incorrect command sent a request: %v", (*calls)[n:])
	}
}

func TestUsersResetAndDeleteFindTheUserByEmail(t *testing.T) {
	srv, calls := usersPanel(t)
	a, out := usersApp(srv)
	ctx := context.Background()
	if err := a.dispatch(ctx, []string{"users", "reset-password", "NEW@acme.example", "--remove-totp"}); err != nil {
		t.Fatal(err)
	}
	if got := last(calls); got != `POST /users/7/reset-password {"removeTotp":true}` {
		t.Errorf("call = %s", got)
	}
	if !strings.Contains(out.String(), "ResetPassword1234567") {
		t.Errorf("output:\n%s", out)
	}
	if err := a.dispatch(ctx, []string{"users", "reset-password", "7"}); err != nil {
		t.Fatal(err)
	}
	if got := last(calls); got != `POST /users/7/reset-password {}` {
		t.Errorf("call = %s", got)
	}
	if err := a.dispatch(ctx, []string{"users", "delete", "new@acme.example"}); err != nil {
		t.Fatal(err)
	}
	if got := last(calls); got != "DELETE /users/7" {
		t.Errorf("call = %s", got)
	}
	if err := a.dispatch(ctx, []string{"users", "delete", "nobody@acme.example"}); err == nil || !strings.Contains(err.Error(), "no user") {
		t.Errorf("err = %v", err)
	}
	if err := a.dispatch(ctx, []string{"users", "delete", "../customers/C1"}); err == nil {
		t.Error("a path in place of a user ID was accepted")
	}
	out.Reset()
	if err := a.dispatch(ctx, []string{"users", "list"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "new@acme.example") || !strings.Contains(out.String(), "MUSTCHANGEPASSWORD") {
		t.Errorf("output:\n%s", out)
	}
}

func TestCustomersCreateWithUser(t *testing.T) {
	srv, calls := usersPanel(t)
	a, out := usersApp(srv)
	ctx := context.Background()
	if err := a.dispatch(ctx, []string{"customers", "create", "--name", "Oak Lane", "--email", "owner@oaklane.example", "--plan", "P1", "--with-user"}); err != nil {
		t.Fatal(err)
	}
	if got := last(calls); !strings.Contains(got, `"createUser":true`) {
		t.Errorf("call = %s", got)
	}
	if !strings.Contains(out.String(), "Temporary password for owner@oaklane.example: FirstPassword1234567") {
		t.Errorf("output:\n%s", out)
	}
	out.Reset()
	if err := a.dispatch(ctx, []string{"customers", "create", "--name", "Oak Lane", "--email", "owner@oaklane.example", "--plan", "P1"}); err != nil {
		t.Fatal(err)
	}
	if got := last(calls); strings.Contains(got, "createUser") {
		t.Errorf("call = %s", got)
	}
	if !strings.Contains(out.String(), "cannot sign in") {
		t.Errorf("output:\n%s", out)
	}
}

// Audit finding R8: a full restore can remove the files that are not in the backup.
func TestRestoreCanRemoveNewFiles(t *testing.T) {
	srv, calls := usersPanel(t)
	a, _ := usersApp(srv)
	ctx := context.Background()
	if err := a.dispatch(ctx, []string{"backups", "restore", "W1", "abc123", "--remove-new-files"}); err != nil {
		t.Fatal(err)
	}
	if got := last(calls); got != `POST /websites/W1/snapshots/abc123/restore {"removeNewFiles":true,"scope":"website"}` {
		t.Errorf("call = %s", got)
	}
	if err := a.dispatch(ctx, []string{"backups", "restore", "W1", "abc123", "--scope", "file", "--item", "/index.php", "--remove-new-files"}); err == nil {
		t.Error("--remove-new-files was accepted for one file")
	}
	if err := a.dispatch(ctx, []string{"backups", "restore", "W1", "abc123"}); err != nil {
		t.Fatal(err)
	}
	if got := last(calls); got != `POST /websites/W1/snapshots/abc123/restore {"scope":"website"}` {
		t.Errorf("call = %s", got)
	}
}

// Audit finding S12: the CLI does not send a token to a panel address with no encryption.
func TestThePanelAddressNeedsHTTPS(t *testing.T) {
	for address, ok := range map[string]bool{
		"https://panel.example.com":  true,
		"https://panel.example.com/": true,
		"http://localhost:8000":      true,
		"http://127.0.0.1:8000":      true,
		"http://[::1]:8000":          true,
		"http://panel.example.com":   false,
		"http://192.0.2.10":          false,
		"http://localhost.evil.test": false,
		"panel.example.com":          false,
		"ftp://panel.example.com":    false,
		"":                           false,
	} {
		if err := checkPanel(address); (err == nil) != ok {
			t.Errorf("checkPanel(%q) = %v, want accepted = %v", address, err, ok)
		}
	}

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CF_PASSWORD", "secret")
	t.Setenv("CF_TOKEN", "")
	var out bytes.Buffer
	a := &app{out: &out, err: &out}
	ctx := context.Background()
	if err := a.dispatch(ctx, []string{"login", "--panel", "http://panel.example.com", "--email", "a@example.com"}); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Errorf("login: err = %v", err)
	}
	if err := a.dispatch(ctx, []string{"use", "--panel", "http://panel.example.com", "--token", "t"}); err == nil {
		t.Error("use accepted an HTTP address")
	}
	if _, err := os.Stat(settingsPath()); !os.IsNotExist(err) {
		t.Errorf("the settings were saved: %v", err)
	}
	// A saved or CF_PANEL address is checked before each command.
	t.Setenv("CF_PANEL", "http://panel.example.com")
	t.Setenv("CF_TOKEN", "token")
	if err := a.dispatch(ctx, []string{"websites", "list"}); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Errorf("websites list: err = %v", err)
	}
}

// Audit findings B5 and S8: login says when the password is temporary, and logout deletes the
// session in the panel.
func TestLoginAndLogout(t *testing.T) {
	srv, calls := usersPanel(t)
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	t.Setenv("CF_PASSWORD", "TemporaryPassword123")
	t.Setenv("CF_TOKEN", "")
	t.Setenv("CF_PANEL", "")
	var out bytes.Buffer
	a := &app{out: &out, err: &out}
	ctx := context.Background()
	if err := a.dispatch(ctx, []string{"login", "--panel", srv.URL, "--email", "new@acme.example"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Your password is temporary") || !strings.Contains(out.String(), "cf account password") {
		t.Errorf("output:\n%s", out.String())
	}
	saved, _ := os.ReadFile(filepath.Join(dir, "cloudfactory", "cli.json"))
	if !strings.Contains(string(saved), "session-token") || !strings.Contains(string(saved), `"session": true`) {
		t.Errorf("settings = %s", saved)
	}
	out.Reset()
	if err := a.dispatch(ctx, []string{"logout"}); err != nil {
		t.Fatal(err)
	}
	if got := last(calls); got != "POST /auth/logout" {
		t.Errorf("call = %s", got)
	}
	saved, _ = os.ReadFile(filepath.Join(dir, "cloudfactory", "cli.json"))
	if strings.Contains(string(saved), "session-token") {
		t.Errorf("the token stays in the settings: %s", saved)
	}
	if !strings.Contains(out.String(), "Signed out.") {
		t.Errorf("output:\n%s", out.String())
	}

	// An API token from "cf use" is not deleted in the panel.
	if err := a.dispatch(ctx, []string{"use", "--panel", srv.URL, "--token", "cft_api"}); err != nil {
		t.Fatal(err)
	}
	n := len(*calls)
	out.Reset()
	if err := a.dispatch(ctx, []string{"logout"}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != n {
		t.Errorf("logout sent a request for an API token: %v", (*calls)[n:])
	}
	if !strings.Contains(out.String(), "cf tokens revoke") {
		t.Errorf("output:\n%s", out.String())
	}
	out.Reset()
	if err := a.dispatch(ctx, []string{"logout"}); err != nil || !strings.Contains(out.String(), "not signed in") {
		t.Errorf("err = %v, output:\n%s", err, out.String())
	}
}
