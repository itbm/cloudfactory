package actions

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/execx"
)

// The IDs of the export parts of the tests.
const (
	filesPartID = "01JBQ3Z5K8N6T4W2X9Y7R5P3E1"
	mariaPartID = "01JBQ3Z5K8N6T4W2X9Y7R5P3E2"
	pgPartID    = "01JBQ3Z5K8N6T4W2X9Y7R5P3E3"
)

// fakeExports is the panel of the export tests. It keeps each part that the agent sends.
type fakeExports struct {
	mu    sync.Mutex
	parts map[string][]byte
	// fail makes the upload of each part fail after the first bytes.
	fail error
}

func (f *fakeExports) UploadExportPart(_ context.Context, partID string, r io.Reader) (int64, string, error) {
	if f.fail != nil {
		_, _ = io.ReadFull(r, make([]byte, 1))
		return 0, "", f.fail
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return 0, "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.parts == nil {
		f.parts = map[string][]byte{}
	}
	f.parts[partID] = data
	sum := sha256.Sum256(data)
	return int64(len(data)), hex.EncodeToString(sum[:]), nil
}

// exportDeps gives the Deps of an export test: the website of the tests, with a disk limit, and
// a MariaDB and a PostgreSQL database server of its customer.
func exportDeps(t *testing.T) (*Deps, *execx.Fake, *fakeExports) {
	t.Helper()
	d, f, _ := deps(t)
	customerServers(t, d, "mariadb", "postgresql")
	w := website(runtimeImage)
	w.Limits.DiskBytes = 5 << 30
	if err := d.State.Put(&Site{ID: websiteID, UnixUser: "acme_x7k2", Port: 20000, Website: w}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(d.Host.AppDir("acme_x7k2"), 0o755); err != nil {
		t.Fatal(err)
	}
	up := &fakeExports{}
	d.Exports = up
	return d, f, up
}

func exportRequest() *agentv1.ExportWebsiteRequest {
	return &agentv1.ExportWebsiteRequest{JobId: jobID, WebsiteId: websiteID, UnixUser: "acme_x7k2", FilesPart: filesPartID, Databases: []*agentv1.ExportDatabase{
		{Engine: "mariadb", Name: "acme_x7k2_shop", DatabaseServer: mariaServerID, Part: mariaPartID},
		{Engine: "postgresql", Name: "acme_x7k2_app", DatabaseServer: pgServerID, Part: pgPartID},
	}}
}

func gunzip(t *testing.T, data []byte) string {
	t.Helper()
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestExportWebsiteSendsTheFilesAndTheDumps(t *testing.T) {
	d, f, up := exportDeps(t)
	archive := d.agentBinary() + " site-archive " + d.Host.AppDir("acme_x7k2") + " " + "5368709120"
	f.Rules = []execx.Rule{
		{Prefix: podmanOf(mariaServerKey) + "exec cf-mariadb-" + mariaServerKey + " mariadb-dump --user=root --single-transaction --routines --events acme_x7k2_shop",
			Output: "CREATE TABLE t (id int);\n/*!50013 DEFINER=`root`@`%` SQL SECURITY DEFINER */\nINSERT INTO t VALUES (1);\n"},
		{Prefix: podmanOf(pgServerKey) + "exec cf-postgresql-" + pgServerKey + " pg_dump --username=postgres --format=custom --no-owner --no-privileges acme_x7k2_app", Output: "PGDMP-custom"},
		{Prefix: "runuser -u acme_x7k2 -- " + archive, Output: "tar-gz-bytes"},
	}
	job, err := d.ExportWebsite(exportRequest())
	if err := run(t)(job, err); err != nil {
		t.Fatal(err)
	}
	if job.Key != websiteID || !job.Long {
		t.Errorf("key %q, long %v: an export must not run at the same time as a backup of the website", job.Key, job.Long)
	}
	// The MariaDB dump has no DEFINER clause, and it is compressed.
	if got := gunzip(t, up.parts[mariaPartID]); strings.Contains(got, "DEFINER=") || !strings.Contains(got, "INSERT INTO t VALUES (1);") {
		t.Errorf("the MariaDB dump is %q", got)
	}
	// The PostgreSQL dump has the custom format, which is compressed already.
	if got := string(up.parts[pgPartID]); got != "PGDMP-custom" {
		t.Errorf("the PostgreSQL dump is %q", got)
	}
	if got := string(up.parts[filesPartID]); got != "tar-gz-bytes" {
		t.Errorf("the files are %q", got)
	}
	parts := job.Result()["parts"].(map[string]any)
	sum := sha256.Sum256([]byte("tar-gz-bytes"))
	if p := parts[filesPartID].(map[string]any); p["sizeBytes"] != int64(len("tar-gz-bytes")) || p["sha256"] != hex.EncodeToString(sum[:]) {
		t.Errorf("the result of the files is %v", p)
	}
	if len(parts) != 3 {
		t.Errorf("the result has %d parts", len(parts))
	}
}

// A job for the databases of a website of a different server sends no files.
func TestExportWebsiteOfTheDatabasesOnly(t *testing.T) {
	d, f, up := exportDeps(t)
	f.Rules = []execx.Rule{{Prefix: podmanOf(pgServerKey) + "exec cf-postgresql-" + pgServerKey + " pg_dump", Output: "PGDMP"}}
	req := &agentv1.ExportWebsiteRequest{JobId: jobID, WebsiteId: otherWebsiteID, UnixUser: "acme_x7k2", DatabasesOnly: true, Customer: customerID,
		Databases: []*agentv1.ExportDatabase{{Engine: "postgresql", Name: "acme_x7k2_app", DatabaseServer: pgServerID, Part: pgPartID}}}
	job, err := d.ExportWebsite(req)
	if err := run(t)(job, err); err != nil {
		t.Fatal(err)
	}
	if len(up.parts) != 1 || string(up.parts[pgPartID]) != "PGDMP" {
		t.Errorf("parts = %v", up.parts)
	}
	if f.Called("runuser -u acme_x7k2") {
		t.Error("a job for the databases only read the files")
	}
}

func TestExportWebsiteFailsWhenTheDumpFails(t *testing.T) {
	d, f, _ := exportDeps(t)
	f.Rules = []execx.Rule{{Prefix: podmanOf(mariaServerKey) + "exec cf-mariadb-" + mariaServerKey + " mariadb-dump", Err: errors.New("exit status 2")}}
	req := exportRequest()
	req.Databases = req.Databases[:1]
	job, err := d.ExportWebsite(req)
	if err := run(t)(job, err); err == nil || !strings.Contains(err.Error(), "exit status 2") {
		t.Fatalf("the job gave %v", err)
	}
}

func TestExportWebsiteFailsWhenTheUploadFails(t *testing.T) {
	d, f, up := exportDeps(t)
	f.Rules = []execx.Rule{{Prefix: "runuser -u acme_x7k2 -- ", Output: "tar-gz-bytes"}}
	up.fail = errors.New("panel: status 507: The panel does not have enough free disk space")
	req := exportRequest()
	req.Databases = nil
	job, err := d.ExportWebsite(req)
	if err := run(t)(job, err); err == nil || !strings.Contains(err.Error(), "free disk space") {
		t.Fatalf("the job gave %v", err)
	}
}

func TestExportWebsiteRefusesBadRequests(t *testing.T) {
	d, _, _ := exportDeps(t)
	cases := map[string]func(r *agentv1.ExportWebsiteRequest){
		"a bad website ID":                  func(r *agentv1.ExportWebsiteRequest) { r.WebsiteId = "../x" },
		"a different Unix user":             func(r *agentv1.ExportWebsiteRequest) { r.UnixUser = "bolt_k3m9" },
		"an unknown website":                func(r *agentv1.ExportWebsiteRequest) { r.WebsiteId = otherWebsiteID },
		"a bad part ID":                     func(r *agentv1.ExportWebsiteRequest) { r.FilesPart = "../../etc" },
		"one part two times":                func(r *agentv1.ExportWebsiteRequest) { r.Databases[0].Part = filesPartID },
		"a database of a different website": func(r *agentv1.ExportWebsiteRequest) { r.Databases[0].Name = "bolt_k3m9_shop" },
		"a Valkey database":                 func(r *agentv1.ExportWebsiteRequest) { r.Databases[0].Engine = "valkey" },
		"no database server":                func(r *agentv1.ExportWebsiteRequest) { r.Databases[0].DatabaseServer = "" },
		"no part":                           func(r *agentv1.ExportWebsiteRequest) { r.FilesPart, r.Databases = "", nil },
		"files with databases only":         func(r *agentv1.ExportWebsiteRequest) { r.DatabasesOnly, r.Customer = true, customerID },
		"databases only with no customer": func(r *agentv1.ExportWebsiteRequest) {
			r.DatabasesOnly, r.FilesPart = true, ""
		},
		"databases only with no database": func(r *agentv1.ExportWebsiteRequest) {
			r.DatabasesOnly, r.FilesPart, r.Customer, r.Databases = true, "", customerID, nil
		},
	}
	for name, change := range cases {
		req := exportRequest()
		change(req)
		if _, err := d.ExportWebsite(req); err == nil {
			t.Errorf("%s: the agent accepted the job", name)
		}
	}
	d.Exports = nil
	if _, err := d.ExportWebsite(exportRequest()); err == nil {
		t.Error("an agent with no connection to the panel accepted the job")
	}
}
