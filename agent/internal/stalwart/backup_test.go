package stalwart

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestTheFoldersOfABackupHaveTheirParentsFirst(t *testing.T) {
	folders := parentsFirst([]Folder{
		{ID: "c", Name: "Client A", ParentID: "p"},
		{ID: "x", Name: "Lost", ParentID: "gone"},
		{ID: "p", Name: "Projects"},
		{ID: "a", Name: "Inbox", Role: "inbox"},
		{ID: "l", Name: "Loop", ParentID: "l"},
	})
	got := []string{}
	for _, f := range folders {
		got = append(got, f.ID+":"+f.ParentID)
	}
	if want := "a: l:l p: x: c:p"; strings.Join(got, " ") != want {
		t.Fatalf("got %q, want %q", strings.Join(got, " "), want)
	}
	if p := folderPath(folders, folders[4]); p != "Projects\x00Client A" {
		t.Fatalf("path %q", p)
	}
}

// testMailboxBackup runs in TestRealServer, on a domain with the mailbox "info".
func testMailboxBackup(ctx context.Context, t *testing.T, c *Client, domain string) {
	t.Helper()
	account, err := c.MailAccount(ctx, domain, "info")
	if err != nil {
		t.Fatal(err)
	}
	current, err := c.Folders(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	role := map[string]string{}
	for _, f := range current {
		role[f.Role] = f.ID
	}
	made, err := c.mailCall(ctx, "Mailbox/set", object{"accountId": account, "create": object{"p": object{"name": "Projects", "parentId": nil}, "c": object{"name": "Client A", "parentId": "#p"}}})
	if err != nil {
		t.Fatal(err)
	}
	created, _ := made["created"].(object)
	clientA, _ := created["c"].(object)["id"].(string)
	type message struct {
		folder   string
		keywords object
	}
	messages := []message{{role["inbox"], object{"$seen": true}}, {clientA, object{"$flagged": true}}, {role["sent"], object{"$seen": true}}, {role["inbox"], object{}}}
	ids := []string{}
	for i, m := range messages {
		blob, err := c.Upload(ctx, account, strings.NewReader(testMessage(i)), int64(len(testMessage(i))))
		if err != nil {
			t.Fatal(err)
		}
		r, err := c.mailCall(ctx, "Email/import", object{"accountId": account, "emails": object{"m": object{"blobId": blob, "mailboxIds": object{m.folder: true}, "keywords": m.keywords, "receivedAt": fmt.Sprintf("2026-03-0%dT10:00:00Z", i+1)}}})
		if err != nil {
			t.Fatal(err)
		}
		created, _ := r["created"].(object)
		id, _ := created["m"].(object)["id"].(string)
		ids = append(ids, id)
	}

	var backup bytes.Buffer
	export, err := c.ExportMailbox(ctx, domain, "info", &backup)
	if err != nil || export.Messages != 4 {
		t.Fatalf("export: %+v, err %v", export, err)
	}
	var again bytes.Buffer
	if _, err := c.ExportMailbox(ctx, domain, "info", &again); err != nil || !bytes.Equal(backup.Bytes(), again.Bytes()) {
		t.Fatalf("a second export of the same mailbox is not the same, err %v", err)
	}
	names := tarNames(t, backup.Bytes())
	if len(names) != 6 || names[0] != MailboxIndex || names[5] != MailboxEnd || names[1] != "messages/"+ids[0]+".eml" {
		t.Fatalf("files %v", names)
	}

	// The owner deletes two messages and the folder "Projects".
	if _, err := c.mailCall(ctx, "Email/set", object{"accountId": account, "destroy": []string{ids[1], ids[3]}}); err != nil {
		t.Fatal(err)
	}
	projects, _ := created["p"].(object)["id"].(string)
	if _, err := c.mailCall(ctx, "Mailbox/set", object{"accountId": account, "destroy": []string{clientA, projects}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ImportMailbox(ctx, domain, "other", bytes.NewReader(backup.Bytes())); !errors.Is(err, ErrNoMailbox) {
		t.Fatalf("a restore into an address with no mailbox: %v", err)
	}
	got, err := c.ImportMailbox(ctx, domain, "info", bytes.NewReader(backup.Bytes()))
	if err != nil || got != (Import{Added: 2, Present: 2}) {
		t.Fatalf("restore: %+v, err %v", got, err)
	}
	if got, err := c.ImportMailbox(ctx, domain, "info", bytes.NewReader(backup.Bytes())); err != nil || got != (Import{Present: 4}) {
		t.Fatalf("a second restore: %+v, err %v", got, err)
	}
	listed, err := c.messages(ctx, account)
	if err != nil || len(listed) != 4 {
		t.Fatalf("after the restore: %d messages, err %v", len(listed), err)
	}
	folders, err := c.Folders(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range listed {
		if m.MessageID != "m1@example.org" {
			continue
		}
		var path string
		for _, f := range folders {
			if len(m.Folders) == 1 && f.ID == m.Folders[0] {
				path = folderPath(folders, f)
			}
		}
		if path != "Projects\x00Client A" || strings.Join(m.Keywords, ",") != "$flagged,$hasnoattachment" {
			t.Fatalf("the restored message: %+v in %q", m, path)
		}
	}

	// A stream that stops before its end adds nothing that is not complete.
	cut := backup.Bytes()[:backup.Len()-2048]
	if _, err := c.ImportMailbox(ctx, domain, "info", bytes.NewReader(cut)); !errors.Is(err, ErrNotBackup) {
		t.Fatalf("a backup that is not complete: %v", err)
	}
	if _, err := c.ImportMailbox(ctx, domain, "info", strings.NewReader("not a tar stream")); !errors.Is(err, ErrNotBackup) {
		t.Fatalf("data that is not a backup: %v", err)
	}
}

func testMessage(i int) string {
	return fmt.Sprintf("From: sender%d@example.org\r\nTo: info@example.com\r\nSubject: Test %d\r\nMessage-ID: <m%d@example.org>\r\nDate: Sun, 1 Mar 2026 10:00:00 +0000\r\n\r\nMessage %d, café.\r\n", i, i, i, i)
}

func tarNames(t *testing.T, b []byte) []string {
	t.Helper()
	tr := tar.NewReader(bytes.NewReader(b))
	names := []string{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return names
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, hdr.Name)
	}
}
