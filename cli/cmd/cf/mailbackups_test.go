package main

import (
	"context"
	"strings"
	"testing"
)

func TestTheBackupsOfAMailbox(t *testing.T) {
	srv, calls := mailPanel(t)
	a, out := testApp(srv)
	ctx := context.Background()
	if err := a.dispatch(ctx, []string{"mail", "mailboxes", "backups", "info@acme.example.com"}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || row(lines[1]) != "2026-11-22T14:00:00+00:00 manual 120 1.5 MB - BK2" ||
		row(lines[2]) != "2026-11-22T02:05:00+00:00 scheduled 100 1 MB 2026-11-22T15:00:00+00:00: 3 added, 96 present, 1 too large BK1" {
		t.Errorf("output:\n%s", out)
	}
	if len(*calls) != 0 {
		t.Errorf("a list changed something: %v", *calls)
	}

	out.Reset()
	if err := a.dispatch(ctx, []string{"mail", "mailboxes", "backup", "info@acme.example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := a.dispatch(ctx, []string{"mail", "mailboxes", "restore", "info@acme.example.com", "BK1"}); err != nil {
		t.Fatal(err)
	}
	// After the restore, the command shows what it added.
	for _, want := range []string{"Messages: 3 added, 96 present, 1 too large.", "It stays in the backup."} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output does not have %q:\n%s", want, out)
		}
	}
	want := []string{"POST /api/v1/mail/mailboxes/MB1/backups", "POST /api/v1/mail/mailboxes/MB1/backups/BK1/restore"}
	if strings.Join(*calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s", strings.Join(*calls, "\n"))
	}

	for _, args := range [][]string{
		{"mail", "mailboxes", "backups"},
		{"mail", "mailboxes", "restore", "info@acme.example.com"},
		{"mail", "mailboxes", "restore", "info@acme.example.com", "NONE"},
		{"mail", "mailboxes", "backup", "nobody@acme.example.com"},
	} {
		if err := a.dispatch(ctx, args); err == nil {
			t.Errorf("%v must be an error", args)
		}
	}
}

func TestTheTextOfAMailboxRestore(t *testing.T) {
	for r, want := range map[mailRestore]string{
		{Added: 2, Present: 10}:                        "2 added, 10 present",
		{Added: 0, Present: 3, TooLarge: 1, Failed: 2}: "0 added, 3 present, 1 too large, 2 failed",
	} {
		if got := r.text(); got != want {
			t.Errorf("text() = %q; want %q", got, want)
		}
	}
}
