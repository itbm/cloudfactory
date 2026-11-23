package actions

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/migrate"
	"github.com/itbm/cloudfactory/agent/internal/stalwart"
)

// mailCopy copies the messages of an import into mailboxes. For each mailbox, a file in the
// directory of the import records the messages that the mail server has. A job that runs
// again does not copy them again.
type mailCopy struct {
	d        *Deps
	c        *stalwart.Client
	a        *migrate.Archive
	importID string
}

// pending is a message that the mail server has as a blob, before Email/import.
type pending struct {
	name  string
	email stalwart.Email
}

// mailbox copies the messages of one Maildir. It gives the number of messages that it copied
// and the number that the mail server refused.
func (m *mailCopy) mailbox(ctx context.Context, b *agentv1.ImportMailbox) (copied, failed int, err error) {
	account, err := m.c.MailAccount(ctx, b.GetDomain(), b.GetLocalPart())
	if err != nil {
		return 0, 0, err
	}
	folders, err := m.c.NewFolderMap(ctx, account)
	if err != nil {
		return 0, 0, err
	}
	record := filepath.Join(m.d.importDir(m.importID), "mail", b.GetDomain(), b.GetLocalPart()+".done")
	done, err := readDone(record)
	if err != nil {
		return 0, 0, err
	}
	if err := os.MkdirAll(filepath.Dir(record), 0o700); err != nil {
		return 0, 0, err
	}
	f, err := os.OpenFile(record, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	var batch []pending
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		emails := make([]stalwart.Email, len(batch))
		for i, p := range batch {
			emails[i] = p.email
		}
		refused, err := m.c.ImportEmails(ctx, account, emails)
		if err != nil {
			return err
		}
		var names bytes.Buffer
		for i, p := range batch {
			if refused[i] != nil {
				failed++
				continue
			}
			copied++
			names.WriteString(p.name + "\n")
		}
		batch = batch[:0]
		if _, err := f.Write(names.Bytes()); err != nil {
			return err
		}
		return f.Sync()
	}
	err = migrate.WalkMaildir(ctx, m.a, b.GetPath(), func(msg migrate.Message, r io.Reader) error {
		if done[msg.Name] || strings.ContainsAny(msg.Name, "\r\n") {
			return nil
		}
		if msg.Size > maxImportMessage {
			failed++
			return nil
		}
		plain, release, err := migrate.Decompress(r)
		if err != nil {
			failed++
			return nil
		}
		content, err := io.ReadAll(io.LimitReader(plain, maxImportMessage+1))
		release()
		if err != nil {
			return fmt.Errorf("%s: %w", msg.Name, err)
		}
		if len(content) > maxImportMessage || len(content) == 0 {
			failed++
			return nil
		}
		folder, err := folders.ID(ctx, msg.Folder)
		if err != nil {
			return err
		}
		blob, err := m.c.Upload(ctx, account, bytes.NewReader(content), int64(len(content)))
		if err != nil {
			return err
		}
		batch = append(batch, pending{name: msg.Name, email: stalwart.Email{BlobID: blob, Folders: []string{folder}, Keywords: msg.Keywords, Received: msg.Received}})
		if len(batch) >= importBatch {
			return flush()
		}
		return nil
	})
	if err != nil {
		return copied, failed, err
	}
	return copied, failed, flush()
}

// readDone reads the record of the messages that an earlier run copied.
func readDone(path string) (map[string]bool, error) {
	done := map[string]bool{}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return done, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if line := sc.Text(); line != "" {
			done[line] = true
		}
	}
	return done, sc.Err()
}
