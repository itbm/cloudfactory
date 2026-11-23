package stalwart

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The functions in this file use the JMAP mail methods (RFC 8621) of a mailbox account as the
// administrator: the agent never has the password of a mailbox. The imports (docs/imports.md)
// and the backups of the mailboxes (backup.go) use them.

// mailUsing are the capabilities of the mail method calls.
var mailUsing = []string{"urn:ietf:params:jmap:core", "urn:ietf:params:jmap:mail"}

// mailCall sends one mail method call and gives its result.
func (c *Client) mailCall(ctx context.Context, method string, args object) (object, error) {
	return c.callUsing(ctx, mailUsing, method, args)
}

// ErrNoMailbox is the error for an address that has no mailbox on the mail server.
var ErrNoMailbox = errors.New("the mail server does not have this mailbox")

// MailAccount gives the JMAP account ID of a mailbox.
func (c *Client) MailAccount(ctx context.Context, domain, localPart string) (string, error) {
	domainID, err := c.domainID(ctx, domain)
	if err != nil {
		return "", err
	}
	if domainID == "" {
		return "", fmt.Errorf("%w: %s@%s", ErrNoMailbox, localPart, domain)
	}
	id, err := c.accountID(ctx, domainID, localPart)
	if err == nil && id == "" {
		err = fmt.Errorf("%w: %s@%s", ErrNoMailbox, localPart, domain)
	}
	return id, err
}

// Folder is a folder (a JMAP Mailbox) of a mailbox account. A backup keeps the folders of a
// mailbox in this form.
type Folder struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ParentID   string `json:"parentId,omitempty"`
	Role       string `json:"role,omitempty"`
	SortOrder  int    `json:"sortOrder,omitempty"`
	Subscribed bool   `json:"isSubscribed,omitempty"`
}

// Folders gives the folders of a mailbox account.
func (c *Client) Folders(ctx context.Context, accountID string) ([]Folder, error) {
	out, err := c.mailCall(ctx, "Mailbox/get", object{"accountId": accountID, "ids": nil, "properties": []string{"id", "name", "parentId", "role", "sortOrder", "isSubscribed"}})
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(out["list"])
	if err != nil {
		return nil, err
	}
	var list []Folder
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("mail server: Mailbox/get: %w", err)
	}
	return slices.DeleteFunc(list, func(f Folder) bool { return f.ID == "" }), nil
}

// CreateFolder makes the folder f and gives its ID. An empty f.ParentID is the top level. The
// role and the ID of f are not used.
func (c *Client) CreateFolder(ctx context.Context, accountID string, f Folder) (string, error) {
	value := object{"name": f.Name, "parentId": nullIfEmpty(f.ParentID), "sortOrder": f.SortOrder, "isSubscribed": f.Subscribed}
	out, err := c.mailCall(ctx, "Mailbox/set", object{"accountId": accountID, "create": object{"f": value}})
	if err != nil {
		return "", err
	}
	if failed, ok := out["notCreated"].(object); ok && failed["f"] != nil {
		return "", setError("Mailbox/set", failed["f"])
	}
	created, _ := out["created"].(object)
	made, _ := created["f"].(object)
	id, _ := made["id"].(string)
	if id == "" {
		return "", errors.New("mail server: Mailbox/set: the answer has no folder ID")
	}
	return id, nil
}

// roleNames are the names of the folders of other mail servers for each role of RFC 8621.
var roleNames = map[string][]string{
	"sent":    {"sent", "sent items", "sent messages", "sent mail"},
	"drafts":  {"drafts", "draft"},
	"trash":   {"trash", "deleted items", "deleted messages", "bin"},
	"junk":    {"junk", "spam", "junk mail", "junk e-mail", "bulk mail"},
	"archive": {"archive", "archives"},
}

// RoleOf gives the role of a folder at the top of an account from its name, or "".
func RoleOf(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	for role, names := range roleNames {
		for _, v := range names {
			if n == v {
				return role
			}
		}
	}
	return ""
}

// FolderMap finds or makes the folders of an account for the folder paths of an import.
type FolderMap struct {
	c         *Client
	accountID string
	byPath    map[string]string
	folders   []Folder
}

// NewFolderMap reads the folders of an account.
func (c *Client) NewFolderMap(ctx context.Context, accountID string) (*FolderMap, error) {
	folders, err := c.Folders(ctx, accountID)
	if err != nil {
		return nil, err
	}
	return &FolderMap{c: c, accountID: accountID, byPath: map[string]string{}, folders: folders}, nil
}

// ID gives the ID of the folder with the path, and makes the folders that are not there. An
// empty path is the inbox. A folder at the top with the name of a role, for example "Sent",
// is the folder of the account with that role.
func (m *FolderMap) ID(ctx context.Context, path []string) (string, error) {
	key := strings.Join(path, "\x00")
	if id, ok := m.byPath[key]; ok {
		return id, nil
	}
	role := "inbox"
	if len(path) > 0 {
		role = ""
		if len(path) == 1 {
			role = RoleOf(path[0])
		}
	}
	if role != "" {
		for _, f := range m.folders {
			if f.Role == role {
				m.byPath[key] = f.ID
				return f.ID, nil
			}
		}
		if len(path) == 0 {
			return "", errors.New("the mailbox has no inbox")
		}
	}
	parent := ""
	if len(path) > 1 {
		id, err := m.ID(ctx, path[:len(path)-1])
		if err != nil {
			return "", err
		}
		parent = id
	}
	name := path[len(path)-1]
	for _, f := range m.folders {
		if f.ParentID == parent && strings.EqualFold(f.Name, name) {
			m.byPath[key] = f.ID
			return f.ID, nil
		}
	}
	id, err := m.c.CreateFolder(ctx, m.accountID, Folder{Name: name, ParentID: parent})
	if err != nil {
		return "", fmt.Errorf("make the folder %s: %w", strings.Join(path, "/"), err)
	}
	m.folders = append(m.folders, Folder{ID: id, Name: name, ParentID: parent})
	m.byPath[key] = id
	return id, nil
}

// Upload sends one message (size bytes from r) to the account and gives its blob ID.
func (c *Client) Upload(ctx context.Context, accountID string, r io.Reader, size int64) (string, error) {
	return c.upload(ctx, accountID, "message/rfc822", r, size)
}

// Email is one message that ImportEmails adds to its folders.
type Email struct {
	BlobID   string
	Folders  []string
	Keywords []string
	Received time.Time
}

// ImportEmails adds uploaded messages to their folders. It gives the reason for each message
// that the mail server refused, by its index in emails.
func (c *Client) ImportEmails(ctx context.Context, accountID string, emails []Email) (map[int]*SetError, error) {
	create := object{}
	for i, e := range emails {
		v := object{"blobId": e.BlobID, "mailboxIds": set(e.Folders...), "keywords": set(e.Keywords...)}
		if !e.Received.IsZero() {
			v["receivedAt"] = e.Received.UTC().Format(time.RFC3339)
		}
		create[strconv.Itoa(i)] = v
	}
	out, err := c.mailCall(ctx, "Email/import", object{"accountId": accountID, "emails": create})
	if err != nil {
		return nil, err
	}
	refused := map[int]*SetError{}
	notCreated, _ := out["notCreated"].(object)
	for k, v := range notCreated {
		if i, err := strconv.Atoi(k); err == nil && i >= 0 && i < len(emails) {
			refused[i] = setError("Email/import", v)
		}
	}
	return refused, nil
}
