package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/itbm/cloudfactory/cli/internal/client"
)

// mailRestore is the result of the last restore of a mailbox backup.
type mailRestore struct {
	At       *string `json:"at"`
	Added    int     `json:"added"`
	Present  int     `json:"present"`
	TooLarge int     `json:"tooLarge"`
	Failed   int     `json:"failed"`
}

// text tells what the restore did, and which messages it did not add.
func (r mailRestore) text() string {
	s := fmt.Sprintf("%d added, %d present", r.Added, r.Present)
	if r.TooLarge > 0 {
		s += fmt.Sprintf(", %d too large", r.TooLarge)
	}
	if r.Failed > 0 {
		s += fmt.Sprintf(", %d failed", r.Failed)
	}
	return s
}

// mailBackups lists the backups of a mailbox, makes a backup, or restores a backup. A restore
// adds the messages of the backup that the mailbox does not have. It does not change or remove
// a message of the mailbox.
func (a *app) mailBackups(ctx context.Context, verb string, args []string) error {
	n, what := 1, "a mailbox"
	if verb == "restore" {
		n, what = 2, "a mailbox and a backup ID"
	}
	if err := need(args, n, what); err != nil {
		return err
	}
	m, err := a.mailRow(ctx, "/mail/mailboxes", "mailbox", args[0])
	if err != nil {
		return err
	}
	base := fmt.Sprintf("/mail/mailboxes/%v/backups", m["id"])
	switch verb {
	case "backups":
		rows, err := client.All[map[string]any](ctx, a.api, base, nil)
		if err != nil {
			return err
		}
		if a.json {
			return a.printJSON(rows)
		}
		for _, r := range rows {
			r["size"] = byteSize(r["sizeBytes"])
			if last, ok := r["lastRestore"].(map[string]any); ok {
				r["restored"] = fmt.Sprintf("%v: %s", last["at"], restoreOf(last).text())
			}
		}
		a.table(rows, []string{"createdAt", "kind", "messages", "size", "restored", "id"})
		return nil
	case "backup":
		return a.change(ctx, http.MethodPost, base, nil)
	}
	id := args[1]
	if err := a.change(ctx, http.MethodPost, base+"/"+url.PathEscape(id)+"/restore", nil); err != nil {
		return err
	}
	if a.json || a.noWait {
		return nil
	}
	// The panel keeps the result with the backup.
	var list struct {
		Data []struct {
			ID          string       `json:"id"`
			LastRestore *mailRestore `json:"lastRestore"`
		} `json:"data"`
	}
	if err := a.api.Do(ctx, http.MethodGet, base, nil, nil, &list); err != nil {
		return err
	}
	for _, b := range list.Data {
		if b.ID == id && b.LastRestore != nil {
			fmt.Fprintf(a.out, "Messages: %s.\n", b.LastRestore.text())
			if b.LastRestore.TooLarge > 0 {
				fmt.Fprintln(a.out, "A message that is too large is larger than the mail server accepts. It stays in the backup.")
			}
		}
	}
	return nil
}

func restoreOf(v map[string]any) mailRestore {
	num := func(k string) int { f, _ := v[k].(float64); return int(f) }
	return mailRestore{Added: num("added"), Present: num("present"), TooLarge: num("tooLarge"), Failed: num("failed")}
}
