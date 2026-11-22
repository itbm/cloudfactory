package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// importPanel answers like the panel API for one import. The first part of the upload gets
// a conflict: the panel has it already, for example after a connection error.
func importPanel(t *testing.T, size int64) (*httptest.Server, *[]string, *map[string]any) {
	var mu sync.Mutex
	var calls []string
	var start map[string]any
	status, received, reads := "uploading", int64(0), 0
	conflict := size > importChunk
	const plan = `{"websites":[{"name":"bakery.example","phpVersion":"7.4","imageId":"IMG84","imageName":"Apache + PHP-FPM · PHP 8.4","diskBytes":1048576,"databases":[{"name":"bakery_wp","bytes":10}],"wordpress":true,"conflict":null},` +
		`{"name":"taken.example","phpVersion":"8.2","imageId":"IMG84","imageName":"Apache + PHP-FPM · PHP 8.4","diskBytes":1,"databases":[],"wordpress":false,"conflict":"A website with this name exists."}],` +
		`"mailDomains":[{"name":"bakery.example","mailboxes":2,"quotaBytes":1073741824,"aliases":1,"messages":12,"newPasswords":1,"conflict":null}],"mailSpace":{"mailboxes":10,"bytes":5368709120},` +
		`"dnsZones":[{"name":"bakery.example","exists":true,"conflict":null}],"images":[{"id":"IMG84","name":"Apache + PHP-FPM · PHP 8.4"},{"id":"IMG83","name":"Apache + PHP-FPM · PHP 8.3"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		path := strings.TrimPrefix(r.URL.Path, "/api/v1")
		switch {
		case path == "/customers":
			w.Write([]byte(`{"data":[{"id":"C1","name":"Corner Bakery"}]}`))
		case path == "/servers":
			w.Write([]byte(`{"data":[{"id":"S2","name":"web-2"}]}`))
		case path == "/imports" && r.Method == http.MethodPost:
			calls = append(calls, "POST /imports "+string(body))
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"import":{"id":"I1","status":"uploading"},"job":null}`))
		case path == "/imports/I1/archive" && r.Method == http.MethodPut:
			offset, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
			calls = append(calls, "PUT offset="+strconv.FormatInt(offset, 10)+" bytes="+strconv.Itoa(len(body)))
			if r.Header.Get("Content-Type") != "application/octet-stream" {
				t.Errorf("content type %q", r.Header.Get("Content-Type"))
			}
			if conflict {
				conflict = false
				received = importChunk
				w.WriteHeader(http.StatusConflict)
				w.Write([]byte(`{"message":"Send the part from that offset.","receivedBytes":` + strconv.FormatInt(received, 10) + `}`))
				return
			}
			received = offset + int64(len(body))
			if received == size {
				status = "fetching"
			}
			w.Write([]byte(`{"receivedBytes":` + strconv.FormatInt(received, 10) + `,"job":null}`))
		case path == "/imports/I1" && r.Method == http.MethodGet:
			reads++
			// The agent reads the archive while the client asks two times.
			if status == "fetching" && reads > 2 {
				status = "ready"
			}
			if status == "starting" {
				status = "running"
			} else if status == "running" {
				status = "done"
			}
			p, items, progress := "null", "[]", `{"total":0,"done":0,"failed":0}`
			if status == "ready" {
				p = plan
			}
			if status == "running" || status == "done" {
				state := map[string]string{"running": "running", "done": "done"}[status]
				items = `[{"id":"i1","kind":"website","name":"bakery.example","state":"done","error":null,"detail":null,"jobId":"J1"},` +
					`{"id":"i2","kind":"files","name":"bakery.example","state":"` + state + `","error":null,"detail":"10 files copied","jobId":"J2"}]`
				progress = `{"total":2,"done":1,"failed":0}`
				if status == "done" {
					progress = `{"total":2,"done":2,"failed":0}`
				}
			}
			w.Write([]byte(`{"id":"I1","status":"` + status + `","source":"upload","fileName":"cpmove-bakery.tar.gz","sizeBytes":` + strconv.FormatInt(size, 10) +
				`,"receivedBytes":` + strconv.FormatInt(received, 10) + `,"panel":"cpanel","panelName":"cPanel","account":"bakery","customerName":"Corner Bakery","serverName":"web-2","error":null,` +
				`"progress":` + progress + `,"warnings":[{"item":"ftp","message":"The panel has no FTP accounts."}],"plan":` + p + `,"items":` + items + `}`))
		case path == "/imports/I1/start" && r.Method == http.MethodPost:
			json.Unmarshal(body, &start)
			status = "starting"
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"id":"I1","status":"starting"}`))
		case path == "/imports/I1" && r.Method == http.MethodDelete:
			calls = append(calls, "DELETE /imports/I1")
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"jobs":[]}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message":"Not found."}`))
		}
	}))
	return srv, &calls, &start
}

func TestImportsCreateUploadsInPartsAndWaitsForThePlan(t *testing.T) {
	size := int64(importChunk*2 + 10)
	srv, calls, _ := importPanel(t, size)
	defer srv.Close()
	a, out := testApp(srv)
	file := filepath.Join(t.TempDir(), "cpmove-bakery.tar.gz")
	if err := os.WriteFile(file, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := a.dispatch(context.Background(), []string{"imports", "create", "--customer", "corner bakery", "--server", "web-2", "--file", file}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := []string{
		`POST /imports {"customerId":"C1","fileName":"cpmove-bakery.tar.gz","serverId":"S2","sizeBytes":` + strconv.FormatInt(size, 10) + `}`,
		"PUT offset=0 bytes=" + strconv.Itoa(importChunk),
		// The panel had the first part: the command continues from its offset.
		"PUT offset=" + strconv.Itoa(importChunk) + " bytes=" + strconv.Itoa(importChunk),
		"PUT offset=" + strconv.Itoa(importChunk*2) + " bytes=10",
	}
	if strings.Join(*calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s", strings.Join(*calls, "\n"))
	}
	for _, s := range []string{"Import I1 is made.", "Sent 8 MB of 8 MB.", "Reading the archive.", "Ready to import.", "cf imports start I1 --all"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("no %q in the output:\n%s", s, out)
		}
	}
}

func TestImportsGetShowsThePlan(t *testing.T) {
	srv, _, _ := importPanel(t, 10)
	defer srv.Close()
	a, out := testApp(srv)
	ctx := context.Background()
	if err := a.dispatch(ctx, []string{"imports", "upload", "I1", "--file", writeFile(t, 10)}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := a.dispatch(ctx, []string{"imports", "get", "I1"}); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"Ready to import", "cPanel, account bakery", "cannot import: A website with this name exists.", "1 mailboxes get a new password",
		"space for 10 more mailboxes and 5 GB more mail", "the customer has this zone", "IMG83  Apache + PHP-FPM · PHP 8.3", "The panel has no FTP accounts."} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("no %q in the output:\n%s", s, out)
		}
	}
}

func TestImportsStart(t *testing.T) {
	for _, tc := range []struct {
		args         []string
		want, starts string
	}{
		{[]string{"--all"}, `{"dnsZones":["bakery.example"],"mailDomains":["bakery.example"],"rewriteWordPress":true,"websites":[{"name":"bakery.example"}]}`,
			"The import of 1 website, 1 mail domain and 1 DNS zone starts."},
		{[]string{"--website", "bakery.example=IMG83", "--keep-wp-config"}, `{"dnsZones":[],"mailDomains":[],"rewriteWordPress":false,"websites":[{"imageId":"IMG83","name":"bakery.example"}]}`,
			"The import of 1 website, 0 mail domains and 0 DNS zones starts."},
		{[]string{"--zone", "bakery.example"}, `{"dnsZones":["bakery.example"],"mailDomains":[],"rewriteWordPress":true,"websites":[]}`,
			"The import of 0 websites, 0 mail domains and 1 DNS zone starts."},
	} {
		srv, _, start := importPanel(t, 10)
		a, out := testApp(srv)
		ctx := context.Background()
		if err := a.dispatch(ctx, []string{"imports", "upload", "I1", "--file", writeFile(t, 10)}); err != nil {
			t.Fatal(err)
		}
		out.Reset()
		if err := a.dispatch(ctx, append([]string{"imports", "start", "I1"}, tc.args...)); err != nil {
			t.Fatalf("%v: %v\n%s", tc.args, err, out)
		}
		b, _ := json.Marshal(*start)
		if string(b) != tc.want {
			t.Errorf("%v: start body %s", tc.args, b)
		}
		if !strings.Contains(out.String(), tc.starts) || !strings.Contains(out.String(), "Importing: 1 of 2 parts ended.") || !strings.Contains(out.String(), "Complete: 2 of 2 parts ended.") {
			t.Errorf("%v: output:\n%s", tc.args, out)
		}
		srv.Close()
	}
}

func TestImportsStartNeedsASelection(t *testing.T) {
	srv, _, start := importPanel(t, 10)
	defer srv.Close()
	a, _ := testApp(srv)
	ctx := context.Background()
	if err := a.dispatch(ctx, []string{"imports", "upload", "I1", "--file", writeFile(t, 10)}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ args, want string }{
		{"", "give --all"},
		{"--website taken.example", "cannot make the website taken.example: A website with this name exists."},
		{"--mail other.example", "the backup has no mail domain other.example"},
	} {
		err := a.dispatch(ctx, append([]string{"imports", "start", "I1"}, strings.Fields(tc.args)...))
		if err == nil || !strings.Contains(err.Error(), tc.want) || *start != nil {
			t.Errorf("%s: error %v, start %v", tc.args, err, *start)
		}
	}
}

func TestImportsUploadNeedsTheSameSize(t *testing.T) {
	srv, calls, _ := importPanel(t, 10)
	defer srv.Close()
	a, _ := testApp(srv)
	err := a.dispatch(context.Background(), []string{"imports", "upload", "I1", "--file", writeFile(t, 11)})
	if err == nil || !strings.Contains(err.Error(), "waits for a file of 10 bytes") || len(*calls) != 0 {
		t.Errorf("error %v, calls %v", err, *calls)
	}
}

func TestImportsDelete(t *testing.T) {
	srv, calls, _ := importPanel(t, 10)
	defer srv.Close()
	a, out := testApp(srv)
	if err := a.dispatch(context.Background(), []string{"imports", "delete", "I1"}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0] != "DELETE /imports/I1" || !strings.Contains(out.String(), "The import is removed.") {
		t.Errorf("calls %v, output:\n%s", *calls, out)
	}
}

func writeFile(t *testing.T, size int) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := os.WriteFile(p, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
