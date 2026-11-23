package actions

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/execx"
	"github.com/itbm/cloudfactory/agent/internal/stalwart"
)

// fakeStore is a mail server with the mailbox info@acme.example.com and its messages.
type fakeStore struct {
	mu       sync.Mutex
	messages map[string]map[string]any
	blobs    map[string]string
	// broken makes each download fail.
	broken bool
}

func newFakeStore() *fakeStore {
	f := &fakeStore{messages: map[string]map[string]any{}, blobs: map[string]string{}}
	for i, folder := range []string{"in", "sent"} {
		id, blob := fmt.Sprintf("e%d", i), fmt.Sprintf("b%d", i)
		f.blobs[blob] = fmt.Sprintf("Subject: Test %d\r\nMessage-ID: <m%d@example.org>\r\n\r\nHello\r\n", i, i)
		f.messages[id] = map[string]any{"id": id, "blobId": blob, "mailboxIds": map[string]any{folder: true}, "keywords": map[string]any{"$seen": true},
			"receivedAt": fmt.Sprintf("2026-03-0%dT10:00:00Z", i+1), "size": len(f.blobs[blob]), "messageId": []string{fmt.Sprintf("m%d@example.org", i)}}
	}
	return f
}

func (f *fakeStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.URL.Path == "/jmap/session":
		_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": map[string]any{"urn:ietf:params:jmap:core": map[string]any{"maxSizeUpload": 1 << 20}}})
		return
	case strings.HasPrefix(r.URL.Path, "/jmap/download/a1/"):
		blob, ok := f.blobs[strings.Split(strings.TrimPrefix(r.URL.Path, "/jmap/download/a1/"), "/")[0]]
		if f.broken {
			http.Error(w, "store failure", http.StatusInternalServerError)
			return
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, blob)
		return
	case r.URL.Path == "/jmap/upload/a1/":
		b, _ := io.ReadAll(r.Body)
		id := fmt.Sprintf("b%d", len(f.blobs))
		f.blobs[id] = string(b)
		_ = json.NewEncoder(w).Encode(map[string]any{"blobId": id, "size": len(b)})
		return
	}
	var req struct {
		MethodCalls [][]json.RawMessage `json:"methodCalls"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var method string
	var args map[string]any
	_ = json.Unmarshal(req.MethodCalls[0][0], &method)
	_ = json.Unmarshal(req.MethodCalls[0][1], &args)
	var result map[string]any
	switch method {
	case "x:Domain/query":
		result = map[string]any{"ids": []string{"d1"}}
	case "x:Domain/get":
		result = map[string]any{"list": []any{map[string]any{"id": "d1", "name": "acme.example.com"}}}
	case "x:Account/query":
		filter, _ := args["filter"].(map[string]any)
		ids := []string{}
		if filter["name"] == "info" {
			ids = append(ids, "a1")
		}
		result = map[string]any{"ids": ids}
	case "x:Account/get":
		result = map[string]any{"list": []any{map[string]any{"id": "a1", "name": "info", "domainId": "d1"}}}
	case "Mailbox/get":
		result = map[string]any{"list": []any{
			map[string]any{"id": "in", "name": "Inbox", "role": "inbox", "parentId": nil, "isSubscribed": true},
			map[string]any{"id": "sent", "name": "Sent Items", "role": "sent", "parentId": nil, "isSubscribed": true},
		}}
	case "Email/query":
		ids := []string{}
		if args["position"].(float64) == 0 {
			for id := range f.messages {
				ids = append(ids, id)
			}
		}
		result = map[string]any{"ids": ids}
	case "Email/get":
		list := []any{}
		for _, id := range args["ids"].([]any) {
			if m, ok := f.messages[id.(string)]; ok {
				list = append(list, m)
			}
		}
		result = map[string]any{"list": list}
	case "Email/import":
		created := map[string]any{}
		for key, v := range args["emails"].(map[string]any) {
			email := v.(map[string]any)
			id := fmt.Sprintf("e%d", len(f.messages)+10)
			blob := email["blobId"].(string)
			email["id"], email["size"] = id, len(f.blobs[blob])
			email["messageId"] = []string{strings.TrimSuffix(strings.TrimPrefix(strings.Split(f.blobs[blob], "\r\n")[1], "Message-ID: <"), ">")}
			f.messages[id] = email
			created[key] = map[string]any{"id": id}
		}
		result = map[string]any{"created": created}
	default:
		result = map[string]any{"type": "unknownMethod"}
		method = "error"
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"methodResponses": []any{[]any{method, result, "0"}}})
}

func TestTheBackupAndTheRestoreOfAMailbox(t *testing.T) {
	d := mailDeps(t)
	f := rootOf(d).Run.(*execx.Fake)
	ctx := context.Background()
	if _, err := d.prepareMail(ctx, mailHost); err != nil {
		t.Fatal(err)
	}
	store := newFakeStore()
	srv := httptest.NewServer(store)
	defer srv.Close()
	both(d, func(d *Deps) { d.MailURL = srv.URL })
	const head = "restic --repo s3:b/c --password-file  --json --retry-lock 30m "
	f.Rules = []execx.Rule{
		{Prefix: head + "backup --stdin", Output: "{\"message_type\":\"status\"}\n{\"message_type\":\"summary\",\"snapshot_id\":\"aa11aa11\",\"total_bytes_processed\":2048}\n"},
		{Prefix: head + "forget", Output: `[{"remove":[{"id":"0011"}]}]`},
	}
	creds := map[string]string{"AWS_ACCESS_KEY_ID": "AKIA1", "AWS_SECRET_ACCESS_KEY": "s3cret"}
	job, err := d.BackupMailbox(&agentv1.BackupMailboxRequest{JobId: jobID, MailboxId: websiteID, Domain: "acme.example.com", LocalPart: "info", Kind: "scheduled",
		Target: "s3:b/c", Credentials: creds, Retention: &agentv1.Retention{Daily: 7}})
	if err := run(t)(job, err); err != nil {
		t.Fatal(err)
	}
	if job.Key != "mailbox-backup-"+websiteID {
		t.Errorf("key %q", job.Key)
	}
	var backup string
	for i, c := range f.Calls {
		if c == head+"backup --stdin --stdin-filename mailbox.tar --tag mailbox="+websiteID+" --tag kind=scheduled" {
			backup = f.Inputs[i]
			if strings.Join(f.Envs[i], " ") != "AWS_ACCESS_KEY_ID=AKIA1 AWS_SECRET_ACCESS_KEY=s3cret" {
				t.Errorf("the environment of the backup: %v", f.Envs[i])
			}
		}
	}
	if !strings.Contains(backup, `"address":"info@acme.example.com"`) || !strings.Contains(backup, "Subject: Test 1") {
		t.Fatalf("the stream of the backup: %q, calls %v", backup, f.Calls)
	}
	if !f.Called(head + "forget --tag mailbox=" + websiteID + ",kind=scheduled --group-by  --keep-daily 7") {
		t.Errorf("no retention: %v", f.Calls)
	}
	result := job.Result()
	if result["snapshotId"] != "aa11aa11" || result["sizeBytes"] != int64(2048) || result["messages"] != 2 || len(result["forgotten"].([]string)) != 1 {
		t.Errorf("result %v", result)
	}

	// The owner deletes a message. The restore adds it again, and keeps the other one.
	store.mu.Lock()
	delete(store.messages, "e1")
	store.mu.Unlock()
	f.Rules = append(f.Rules, execx.Rule{Prefix: head + "dump aa11aa11 /mailbox.tar", Output: backup})
	job, err = d.RestoreMailbox(&agentv1.RestoreMailboxRequest{JobId: jobID, MailboxId: websiteID, Domain: "acme.example.com", LocalPart: "info", SnapshotId: "aa11aa11", Target: "s3:b/c", Credentials: creds})
	if err := run(t)(job, err); err != nil {
		t.Fatal(err)
	}
	if got := job.Result(); got["added"] != 1 || got["present"] != 1 || got["failed"] != 0 {
		t.Errorf("restore result %v", got)
	}
	if len(store.messages) != 2 {
		t.Errorf("messages after the restore: %v", store.messages)
	}

	// A restore of a stream that is not complete fails, and the error tells why.
	f.Rules = append([]execx.Rule{{Prefix: head + "dump bb22bb22 /mailbox.tar", Output: backup[:len(backup)-2048]}}, f.Rules...)
	job, err = d.RestoreMailbox(&agentv1.RestoreMailboxRequest{JobId: jobID, MailboxId: websiteID, Domain: "acme.example.com", LocalPart: "info", SnapshotId: "bb22bb22", Target: "s3:b/c"})
	if err := run(t)(job, err); !errors.Is(err, stalwart.ErrNotBackup) {
		t.Errorf("a stream that is not complete: %v", err)
	}
	// A backup of a different mailbox.
	other := tarOf(stalwart.MailboxIndex, `{"format":"cf-mailbox/1","address":"ann@acme.example.com"}`)
	f.Rules = append([]execx.Rule{{Prefix: head + "dump dd44dd44 /mailbox.tar", Output: other}}, f.Rules...)
	job, err = d.RestoreMailbox(&agentv1.RestoreMailboxRequest{JobId: jobID, MailboxId: websiteID, Domain: "acme.example.com", LocalPart: "info", SnapshotId: "dd44dd44", Target: "s3:b/c"})
	if err := run(t)(job, err); err == nil || !strings.Contains(err.Error(), "ann@acme.example.com") {
		t.Errorf("a backup of a different mailbox: %v", err)
	}
	// restic fails first: the error is the error of restic.
	f.Rules = append([]execx.Rule{{Prefix: head + "dump cc33cc33", Err: errors.New("restic: Fatal: no matching ID found")}}, f.Rules...)
	job, err = d.RestoreMailbox(&agentv1.RestoreMailboxRequest{JobId: jobID, MailboxId: websiteID, Domain: "acme.example.com", LocalPart: "info", SnapshotId: "cc33cc33", Target: "s3:b/c"})
	if err := run(t)(job, err); err == nil || !strings.Contains(err.Error(), "no matching ID") {
		t.Errorf("restic failed: %v", err)
	}
	// The mail server fails during the export: the error is the error of the mail server.
	store.broken = true
	job, err = d.BackupMailbox(&agentv1.BackupMailboxRequest{JobId: jobID, MailboxId: websiteID, Domain: "acme.example.com", LocalPart: "info", Kind: "manual", Target: "s3:b/c"})
	if err := run(t)(job, err); err == nil || !strings.Contains(err.Error(), "store failure") {
		t.Errorf("the export failed: %v", err)
	}
}

func TestTheBackupOfAMailboxRefusesBadValues(t *testing.T) {
	d := mailDeps(t)
	ok := &agentv1.BackupMailboxRequest{JobId: jobID, MailboxId: websiteID, Domain: "acme.example.com", LocalPart: "info", Kind: "manual", Target: "s3:b/c"}
	for _, change := range []func(r *agentv1.BackupMailboxRequest){
		func(r *agentv1.BackupMailboxRequest) { r.MailboxId = "x,kind=manual" },
		func(r *agentv1.BackupMailboxRequest) { r.Domain = "a b" },
		func(r *agentv1.BackupMailboxRequest) { r.LocalPart = "info@x" },
		func(r *agentv1.BackupMailboxRequest) { r.Kind = "manual-databases" },
		func(r *agentv1.BackupMailboxRequest) { r.Target = "/etc" },
		func(r *agentv1.BackupMailboxRequest) { r.Retention = &agentv1.Retention{Daily: -1} },
		func(r *agentv1.BackupMailboxRequest) {
			r.Credentials = map[string]string{"RESTIC_PASSWORD_COMMAND": "sh"}
		},
	} {
		c := agentv1.BackupMailboxRequest{JobId: ok.JobId, MailboxId: ok.MailboxId, Domain: ok.Domain, LocalPart: ok.LocalPart, Kind: ok.Kind, Target: ok.Target}
		change(&c)
		if _, err := d.BackupMailbox(&c); err == nil {
			t.Errorf("accepted %+v", &c)
		}
	}
	for _, id := range []string{"latest", "aa11:/etc", ""} {
		if _, err := d.RestoreMailbox(&agentv1.RestoreMailboxRequest{JobId: jobID, MailboxId: websiteID, Domain: "acme.example.com", LocalPart: "info", SnapshotId: id, Target: "s3:b/c"}); err == nil {
			t.Errorf("the snapshot %q was accepted", id)
		}
	}
}

// The weekly job removes all snapshots of the mailboxes that the panel deleted.
func TestPruneRemovesTheSnapshotsOfDeletedMailboxes(t *testing.T) {
	d, f, _ := deps(t)
	f.Rules = []execx.Rule{
		{Prefix: "restic --repo b2:bucket:cf --password-file  --json --retry-lock 30m snapshots --tag mailbox=" + websiteID, Output: `[{"id":"00aa11bb22cc"}]`},
	}
	if err := run(t)(d.PruneBackups(&agentv1.PruneBackupsRequest{JobId: jobID, Target: "b2:bucket:cf", ForgetMailboxIds: []string{websiteID}})); err != nil {
		t.Fatal(err)
	}
	if !f.Called("restic --repo b2:bucket:cf --password-file  --json --retry-lock 30m forget 00aa11bb22cc") {
		t.Errorf("calls %v", f.Calls)
	}
	if _, err := d.PruneBackups(&agentv1.PruneBackupsRequest{JobId: jobID, Target: "b2:bucket:cf", ForgetMailboxIds: []string{"x,kind=manual"}}); err == nil {
		t.Error("a mailbox ID that is not valid was accepted")
	}
}

// tarOf is a tar stream with one file, for the tests that need a stream that is not a backup.
func tarOf(name, content string) string {
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(content))})
	_, _ = tw.Write([]byte(content))
	_ = tw.Close()
	return b.String()
}
