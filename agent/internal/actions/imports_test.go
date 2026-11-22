package actions

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/migrate"
	"github.com/itbm/cloudfactory/agent/internal/stalwart"
)

const importID = "01JBQ3Z5K8N6T4W2X9Y7R5P3I1"

// importArchive is a small cPanel backup with one website, one database and one mailbox.
func importArchive(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	add := func(name, body string, flag byte, link string) {
		h := &tar.Header{Name: name, Mode: 0o644, Typeflag: flag, Linkname: link, ModTime: time.Unix(1700000000, 0)}
		if flag == tar.TypeReg {
			h.Size = int64(len(body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		io.WriteString(tw, body)
	}
	file := func(name, body string) { add(name, body, tar.TypeReg, "") }
	file("cpmove-alice/cp/alice", "USER=alice\nCONTACTEMAIL=alice@example.org\n")
	file("cpmove-alice/userdata/main", "main_domain: example.com\n")
	file("cpmove-alice/userdata/example.com", "documentroot: /home/alice/public_html\nhomedir: /home/alice\n")
	file("cpmove-alice/homedir/public_html/index.php", "<?php echo 1;")
	file("cpmove-alice/homedir/public_html/wp-config.php", "<?php\ndefine('DB_NAME', 'alice_wp');\ndefine('DB_USER', 'alice_wp');\ndefine('DB_PASSWORD', 'old');\ndefine('DB_HOST', 'localhost');\n")
	add("cpmove-alice/homedir/public_html/up", "", tar.TypeSymlink, "../..")
	file("cpmove-alice/homedir/public_html/up/etc/passwd", "planted")
	file("cpmove-alice/mysql/alice_wp.sql", "USE `alice_wp`;\nCREATE TABLE t (id int);\n")
	file("cpmove-alice/homedir/etc/example.com/passwd", "info:x:1:1::/x:/x\n")
	file("cpmove-alice/homedir/mail/example.com/info/cur/1700000100.M1.host:2,S", "Subject: one\r\n\r\nbody\r\n")
	file("cpmove-alice/homedir/mail/example.com/info/.Sent/cur/1700000200.M2.host:2,S", "Subject: two\r\n\r\nbody\r\n")
	file("cpmove-alice/homedir/mail/example.com/info/.Projects.2024/cur/1700000300.M3.host:2,", "Subject: three\r\n\r\nbody\r\n")
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	zw.Close()
	return buf.Bytes()
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestFetchImportDownloadsAndReadsTheArchive(t *testing.T) {
	d, _, _ := deps(t)
	archive := importArchive(t)
	var requests int
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Write(archive)
	}))
	defer srv.Close()
	d.ImportHTTP = srv.Client()

	job, err := d.FetchImport(&agentv1.FetchImportRequest{JobId: jobID, ImportId: importID, Url: srv.URL + "/cpmove-alice.tar.gz", MaxBytes: 1 << 20, Inspect: true})
	if err := runSteps(t, job, err); err != nil {
		t.Fatal(err)
	}
	result := job.Result()
	if result["sha256"] != sum(archive) || result["bytes"] != int64(len(archive)) {
		t.Fatalf("result = %v", result)
	}
	acc, ok := result["account"].(*migrate.Account)
	if !ok || acc.Source.Panel != "cpanel" || len(acc.Websites) != 1 || acc.Websites[0].Name != "example.com" {
		t.Fatalf("account = %+v", result["account"])
	}
	if _, err := json.Marshal(result); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(d.importArchive(importID)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("the archive: %v %v", fi, err)
	}

	// The same job, or a job with the hash, keeps the archive.
	job, err = d.FetchImport(&agentv1.FetchImportRequest{JobId: jobID2, ImportId: importID, Url: srv.URL + "/x", Sha256: sum(archive), MaxBytes: 1 << 20})
	if err := runSteps(t, job, err); err != nil || requests != 1 {
		t.Fatalf("again: %v, %d requests", err, requests)
	}
	// An archive that changed after the inspection is refused.
	archive = append(archive, 0)
	job, err = d.FetchImport(&agentv1.FetchImportRequest{JobId: jobID2, ImportId: importID, Url: srv.URL + "/x", Sha256: strings.Repeat("a", 64), MaxBytes: 1 << 20})
	if err := runSteps(t, job, err); err == nil || !strings.Contains(err.Error(), "changed after the inspection") {
		t.Fatalf("a changed archive: %v", err)
	}
}

func TestFetchImportFromThePanel(t *testing.T) {
	d, _, _ := deps(t)
	archive := importArchive(t)
	var path string
	d.PanelDownload = func(_ context.Context, p string, w io.Writer) (int64, error) {
		path = p
		n, err := w.Write(archive)
		return int64(n), err
	}
	job, err := d.FetchImport(&agentv1.FetchImportRequest{JobId: jobID, ImportId: importID, MaxBytes: 1 << 20})
	if err := runSteps(t, job, err); err != nil {
		t.Fatal(err)
	}
	if path != "/api/agent/v1/imports/"+importID+"/archive" {
		t.Errorf("path = %s", path)
	}
	// The size limit.
	job, err = d.FetchImport(&agentv1.FetchImportRequest{JobId: jobID, ImportId: "01JBQ3Z5K8N6T4W2X9Y7R5P3I2", MaxBytes: 10})
	if err := runSteps(t, job, err); err == nil || !strings.Contains(err.Error(), "larger than the limit") {
		t.Errorf("the size limit: %v", err)
	}
}

func TestFetchImportRefusesBadRequests(t *testing.T) {
	d, _, _ := deps(t)
	for _, req := range []*agentv1.FetchImportRequest{
		{JobId: jobID, ImportId: importID, Url: "http://example.com/a.tar", MaxBytes: 1},
		{JobId: jobID, ImportId: importID, Url: "https://user:pass@example.com/a.tar", MaxBytes: 1},
		{JobId: jobID, ImportId: importID, Url: "file:///etc/passwd", MaxBytes: 1},
		{JobId: jobID, ImportId: importID, Url: "https://example.com/a.tar"},
		{JobId: jobID, ImportId: "../x", MaxBytes: 1},
		{JobId: jobID, ImportId: importID, Sha256: "abc", MaxBytes: 1},
	} {
		if _, err := d.FetchImport(req); err == nil {
			t.Errorf("accepted %+v", req)
		}
	}
}

func TestImportDownloadsOnlyFromPublicAddresses(t *testing.T) {
	for addr, want := range map[string]bool{
		"93.184.216.34": true, "2606:2800:220:1::": true,
		"127.0.0.1": false, "10.1.2.3": false, "192.168.1.1": false, "172.16.0.1": false, "169.254.169.254": false,
		"169.254.77.1": false, "100.64.0.1": false, "::1": false, "fd00::1": false, "fe80::1": false,
		"::ffff:127.0.0.1": false, "0.0.0.0": false, "224.0.0.1": false, "64:ff9b::7f00:1": false,
	} {
		if got := publicAddress(netip.MustParseAddr(addr)); got != want {
			t.Errorf("publicAddress(%s) = %v", addr, got)
		}
	}
	// The client refuses a URL that resolves to a loopback address.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	d, _, _ := deps(t)
	err := d.downloadURL(context.Background(), srv.URL, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "not a public address") {
		t.Errorf("a loopback address: %v", err)
	}
}

// fetched downloads the test archive for an import.
func fetched(t *testing.T, d *Deps) {
	t.Helper()
	archive := importArchive(t)
	d.PanelDownload = func(_ context.Context, _ string, w io.Writer) (int64, error) {
		n, err := w.Write(archive)
		return int64(n), err
	}
	if err := run(t)(d.FetchImport(&agentv1.FetchImportRequest{JobId: jobID, ImportId: importID, MaxBytes: 1 << 20})); err != nil {
		t.Fatal(err)
	}
}

func TestImportFilesWritesAsTheWebsiteUser(t *testing.T) {
	d, f, _ := deps(t)
	fetched(t, d)
	if err := d.State.Put(&Site{ID: websiteID, UnixUser: "acme_x7k2", Website: website("acme.example")}); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{"DB_NAME": "acme_x7k2_wp", "DB_USER": "acme_x7k2_wp", "DB_PASSWORD": "Secret123", "DB_HOST": "mariadb.internal"}
	job, err := d.ImportFiles(&agentv1.ImportFilesRequest{JobId: jobID2, ImportId: importID, WebsiteId: websiteID, UnixUser: "acme_x7k2",
		Path: "cpmove-alice/homedir/public_html", Replace: true, WordpressConfig: "wp-config.php", WordpressValues: values})
	if err := runSteps(t, job, err); err != nil {
		t.Fatal(err)
	}
	app := d.Host.AppDir("acme_x7k2")
	if !f.Called("runuser -u acme_x7k2 -- find " + app + " -mindepth 1 -delete") {
		t.Errorf("the old files stay: %v", f.Calls)
	}
	extract := "runuser -u acme_x7k2 -- tar --extract --file=- --directory=" + app + " --no-same-owner --preserve-permissions"
	i := slices.Index(f.Calls, extract)
	if i < 0 {
		t.Fatalf("no tar process of the website user: %v", f.Calls)
	}
	tr := tar.NewReader(strings.NewReader(f.Inputs[i]))
	files := map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		files[h.Name] = string(b)
	}
	if _, ok := files["up/etc/passwd"]; ok || len(files) != 3 {
		t.Errorf("files = %v", files)
	}
	if !strings.Contains(files["wp-config.php"], "define( 'DB_PASSWORD', 'Secret123' )") {
		t.Errorf("wp-config.php = %s", files["wp-config.php"])
	}
	result := job.Result()
	if result["files"] != 2 || result["skipped"] != 1 || !slices.Equal(result["rewritten"].([]string), []string{"wp-config.php"}) {
		t.Errorf("result = %v", result)
	}
}

func TestImportFilesRefusesBadRequests(t *testing.T) {
	d, _, _ := deps(t)
	ok := &agentv1.ImportFilesRequest{JobId: jobID, ImportId: importID, WebsiteId: websiteID, UnixUser: "acme_x7k2", Path: "a/b"}
	for _, change := range []func(r *agentv1.ImportFilesRequest){
		func(r *agentv1.ImportFilesRequest) { r.Path = "../etc" },
		func(r *agentv1.ImportFilesRequest) { r.Path = "/abs" },
		func(r *agentv1.ImportFilesRequest) { r.UnixUser = "root" },
		func(r *agentv1.ImportFilesRequest) { r.Exclude = []string{"a/../../b"} },
		func(r *agentv1.ImportFilesRequest) {
			r.WordpressConfig = "wp-config.php"
			r.WordpressValues = map[string]string{"DB_NAME": "x"}
		},
	} {
		req := &agentv1.ImportFilesRequest{JobId: ok.JobId, ImportId: ok.ImportId, WebsiteId: ok.WebsiteId, UnixUser: ok.UnixUser, Path: ok.Path}
		change(req)
		if _, err := d.ImportFiles(req); err == nil {
			t.Errorf("accepted %+v", req)
		}
	}
	// A server with no archive.
	if err := d.State.Put(&Site{ID: websiteID, UnixUser: "acme_x7k2", Website: website("acme.example")}); err != nil {
		t.Fatal(err)
	}
	err := run(t)(d.ImportFiles(ok))
	if err == nil || !strings.Contains(err.Error(), "does not have the archive") {
		t.Errorf("no archive: %v", err)
	}
}

func TestImportDumpRemovesTheDatabaseStatements(t *testing.T) {
	in := "-- MySQL dump\nUSE `old_db`;\n/*!40000 USE old */;\nCREATE DATABASE /*!32312 IF NOT EXISTS*/ `old_db`;\n/*!40101 CREATE DATABASE x */;\nCREATE TABLE `users` (id int);\nINSERT INTO t VALUES ('USE x');\n"
	out, err := io.ReadAll(importDump(strings.NewReader(in)))
	if err != nil {
		t.Fatal(err)
	}
	want := "-- MySQL dump\n-- USE `old_db`;\n-- /*!40000 USE old */;\n-- CREATE DATABASE /*!32312 IF NOT EXISTS*/ `old_db`;\n-- /*!40101 CREATE DATABASE x */;\nCREATE TABLE `users` (id int);\nINSERT INTO t VALUES ('USE x');\n"
	if string(out) != want {
		t.Errorf("dump =\n%s", out)
	}
}

func TestImportDatabaseRefusesBadRequests(t *testing.T) {
	d, _, _ := deps(t)
	for _, req := range []*agentv1.ImportDatabaseRequest{
		{JobId: jobID, ImportId: importID, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: "mariadb", Name: "other_db", Path: "a.sql", DatabaseServer: customerID},
		{JobId: jobID, ImportId: importID, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: "postgresql", Name: "acme_x7k2_db", Path: "a.sql", DatabaseServer: customerID},
		{JobId: jobID, ImportId: importID, WebsiteId: websiteID, UnixUser: "acme_x7k2", Engine: "mariadb", Name: "acme_x7k2_db", Path: "../a.sql", DatabaseServer: customerID},
	} {
		if _, err := d.ImportDatabase(req); err == nil {
			t.Errorf("accepted %+v", req)
		}
	}
}

// fakeJMAP is a mail server with one domain and one mailbox for the import of messages.
type fakeJMAP struct {
	mu      sync.Mutex
	folders []stalwart.Folder
	emails  []map[string]any
	uploads int
	// refuse makes Email/import refuse the message with this blob.
	refuse string
}

func (s *fakeJMAP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u, p, ok := r.BasicAuth(); !ok || u != stalwart.AdminUser || p != "pw" {
		http.Error(w, "no", http.StatusUnauthorized)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/jmap/upload/acc1/") {
		b, _ := io.ReadAll(r.Body)
		s.uploads++
		json.NewEncoder(w).Encode(map[string]any{"blobId": "blob-" + strings.TrimSpace(strings.SplitN(string(b), "\r\n", 2)[0][len("Subject: "):]), "size": len(b)})
		return
	}
	var req struct {
		MethodCalls [][]json.RawMessage `json:"methodCalls"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	var method string
	json.Unmarshal(req.MethodCalls[0][0], &method)
	var args map[string]any
	json.Unmarshal(req.MethodCalls[0][1], &args)
	var result any
	switch method {
	case "x:Domain/query":
		result = map[string]any{"ids": []string{"dom1"}}
	case "x:Domain/get":
		result = map[string]any{"list": []any{map[string]any{"id": "dom1", "name": "example.com"}}}
	case "x:Account/query":
		result = map[string]any{"ids": []string{"acc1"}}
	case "x:Account/get":
		result = map[string]any{"list": []any{map[string]any{"id": "acc1", "name": "info", "domainId": "dom1"}}}
	case "Mailbox/get":
		result = map[string]any{"list": s.folders}
	case "Mailbox/set":
		create := args["create"].(map[string]any)["f"].(map[string]any)
		id := "f" + string(rune('a'+len(s.folders)))
		parent, _ := create["parentId"].(string)
		s.folders = append(s.folders, stalwart.Folder{ID: id, Name: create["name"].(string), ParentID: parent})
		result = map[string]any{"created": map[string]any{"f": map[string]any{"id": id}}}
	case "Email/import":
		created, notCreated := map[string]any{}, map[string]any{}
		for k, v := range args["emails"].(map[string]any) {
			e := v.(map[string]any)
			if e["blobId"] == s.refuse {
				notCreated[k] = map[string]any{"type": "invalidEmail", "description": "bad"}
				continue
			}
			s.emails = append(s.emails, e)
			created[k] = map[string]any{"id": "m" + k}
		}
		result = map[string]any{"created": created, "notCreated": notCreated}
	default:
		http.Error(w, "unknown method "+method, http.StatusBadRequest)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"methodResponses": []any{[]any{method, result, "0"}}})
}

func TestImportMailCopiesEachMessageOneTime(t *testing.T) {
	d, _, _ := deps(t)
	fetched(t, d)
	fake := &fakeJMAP{folders: []stalwart.Folder{{ID: "in", Name: "Inbox", Role: "inbox"}, {ID: "sent", Name: "Sent Items", Role: "sent"}}, refuse: "blob-three"}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c := &stalwart.Client{URL: srv.URL, Password: "pw", HTTP: srv.Client()}
	a, err := d.archive(importID)
	if err != nil {
		t.Fatal(err)
	}
	mc := &mailCopy{d: d, c: c, a: a, importID: importID}
	box := &agentv1.ImportMailbox{Domain: "example.com", LocalPart: "info", Path: "cpmove-alice/homedir/mail/example.com/info"}
	copied, failed, err := mc.mailbox(context.Background(), box)
	if err != nil || copied != 2 || failed != 1 {
		t.Fatalf("copied %d, failed %d, %v", copied, failed, err)
	}
	folders := map[string]string{}
	for _, e := range fake.emails {
		for id := range e["mailboxIds"].(map[string]any) {
			folders[e["blobId"].(string)] = id
		}
		if e["receivedAt"] == nil {
			t.Errorf("no receivedAt: %v", e)
		}
	}
	if folders["blob-one"] != "in" || folders["blob-two"] != "sent" {
		t.Errorf("folders = %v", folders)
	}
	// "Projects.2024" is a folder in a new folder "Projects".
	var projects, year stalwart.Folder
	for _, f := range fake.folders {
		switch f.Name {
		case "Projects":
			projects = f
		case "2024":
			year = f
		}
	}
	if projects.ID == "" || year.ParentID != projects.ID {
		t.Errorf("folders = %+v", fake.folders)
	}
	if kw := fake.emails[0]["keywords"].(map[string]any); kw["$seen"] != true {
		t.Errorf("keywords = %v", kw)
	}

	// A second run copies only the message that the mail server refused.
	uploads := fake.uploads
	copied, failed, err = mc.mailbox(context.Background(), box)
	if err != nil || copied != 0 || failed != 1 || fake.uploads != uploads+1 {
		t.Fatalf("again: copied %d, failed %d, uploads %d, %v", copied, failed, fake.uploads-uploads, err)
	}
}

func TestImportMailRefusesBadRequests(t *testing.T) {
	d, _, _ := deps(t)
	for _, req := range []*agentv1.ImportMailRequest{
		{JobId: jobID, ImportId: importID},
		{JobId: jobID, ImportId: importID, Mailboxes: []*agentv1.ImportMailbox{{Domain: "example.com", LocalPart: "a b", Path: "x"}}},
		{JobId: jobID, ImportId: importID, Mailboxes: []*agentv1.ImportMailbox{{Domain: "example.com", LocalPart: "info", Path: "../x"}}},
	} {
		if _, err := d.ImportMail(req); err == nil {
			t.Errorf("accepted %+v", req)
		}
	}
}

func TestDeleteImportRemovesTheArchive(t *testing.T) {
	d, _, _ := deps(t)
	fetched(t, d)
	if err := run(t)(d.DeleteImport(&agentv1.DeleteImportRequest{JobId: jobID2, ImportId: importID})); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(d.importDir(importID)); !os.IsNotExist(err) {
		t.Errorf("the archive stays: %v", err)
	}
}
