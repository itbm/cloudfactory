package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// exportPanel answers like the panel API for one account export. The export is ready after
// the second read. The download sends a range when the request has one.
func exportPanel(t *testing.T, archive string) (*httptest.Server, *[]string) {
	var calls []string
	reads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, strings.TrimSpace(r.Method+" "+r.URL.Path+" "+string(body)))
		path := strings.TrimPrefix(r.URL.Path, "/api/v1")
		w.Header().Set("Content-Type", "application/json")
		running := `{"id":"E1","customerId":"C1","customerName":"Acme Ltd","state":"running","parts":{"complete":1,"total":3},"receivedBytes":2048,"sizeBytes":null,"warnings":[],"error":null}`
		ready := `{"id":"E1","customerId":"C1","customerName":"Acme Ltd","state":"ready","parts":{"complete":3,"total":3},"receivedBytes":4096,"sizeBytes":` + strconv.Itoa(len(archive)) + `,"warnings":[{"item":"mail","message":"No messages."}],"error":null}`
		switch {
		case path == "/exports" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(running))
		case path == "/exports" && r.Method == http.MethodGet:
			w.Write([]byte(`{"data":[` + ready + `]}`))
		case path == "/exports/E1" && r.Method == http.MethodGet:
			reads++
			if reads < 2 {
				w.Write([]byte(running))
			} else {
				w.Write([]byte(ready))
			}
		case path == "/exports/E1/download-link":
			w.Write([]byte(`{"url":"/api/v1/export-downloads/E1?expires=1&signature=abc","fileName":"acme-ltd-export.tar","sizeBytes":` + strconv.Itoa(len(archive)) + `,"expiresAt":"2026-11-22T21:10:00Z"}`))
		case path == "/export-downloads/E1":
			if r.Header.Get("Authorization") != "" {
				t.Error("the download sent the token")
			}
			if r.URL.Query().Get("signature") != "abc" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/x-tar")
			if rg := r.Header.Get("Range"); rg != "" {
				from, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(rg, "bytes="), "-"))
				calls = append(calls, "RANGE "+rg)
				w.WriteHeader(http.StatusPartialContent)
				w.Write([]byte(archive[from:]))
				return
			}
			w.Write([]byte(archive))
		case path == "/exports/E1" && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return srv, &calls
}

func TestExportsCreateWaitsUntilReady(t *testing.T) {
	srv, calls := exportPanel(t, "tar")
	defer srv.Close()
	a, out := testApp(srv)
	if err := a.dispatch(context.Background(), []string{"exports", "create", "--customer", "C1", "--no-files"}); err != nil {
		t.Fatal(err)
	}
	if (*calls)[0] != `POST /api/v1/exports {"customerId":"C1","databases":true,"files":false}` {
		t.Errorf("calls: %v", *calls)
	}
	if !strings.Contains(out.String(), "1 of 3 parts complete, 2.0 KiB received") || !strings.Contains(out.String(), "The export E1 is ready (3 bytes)") {
		t.Errorf("output:\n%s", out)
	}
}

func TestExportsListGetAndDelete(t *testing.T) {
	srv, _ := exportPanel(t, "tar")
	defer srv.Close()
	a, out := testApp(srv)
	ctx := context.Background()
	if err := a.dispatch(ctx, []string{"exports", "list"}); err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(strings.TrimSpace(out.String()), "\n"); len(lines) != 2 || !strings.HasPrefix(row(lines[1]), "E1 Acme Ltd ready 3 bytes") {
		t.Errorf("output:\n%s", out)
	}
	out.Reset()
	if err := a.dispatch(ctx, []string{"exports", "get", "E1"}); err != nil {
		t.Fatal(err)
	}
	if err := a.dispatch(ctx, []string{"exports", "get", "E1"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Not in the archive: mail: No messages.") {
		t.Errorf("output:\n%s", out)
	}
	if err := a.dispatch(ctx, []string{"exports", "delete", "E1"}); err != nil {
		t.Fatal(err)
	}
}

func TestExportsDownloadContinuesAPartialFile(t *testing.T) {
	archive := strings.Repeat("0123456789", 100)
	srv, calls := exportPanel(t, archive)
	defer srv.Close()
	a, out := testApp(srv)
	dir := t.TempDir()
	file := filepath.Join(dir, "acme.tar")
	if err := os.WriteFile(file+".partial", []byte(archive[:300]), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.dispatch(context.Background(), []string{"exports", "download", "E1", "--output", file}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(file)
	if err != nil || string(got) != archive {
		t.Fatalf("the file has %d bytes, err %v", len(got), err)
	}
	if _, err := os.Stat(file + ".partial"); !os.IsNotExist(err) {
		t.Error("the partial file stays")
	}
	if !strings.Contains(strings.Join(*calls, "\n"), "RANGE bytes=300-") || !strings.Contains(out.String(), "Continuing the download at 300 bytes") {
		t.Errorf("calls: %v\noutput:\n%s", *calls, out)
	}
}
