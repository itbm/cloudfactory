package stalwart

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The functions in this file copy the messages of an import into a mailbox (docs/imports.md).
// They use the JMAP mail methods (RFC 8621) of the mailbox account as the administrator: the
// agent never has the password of a mailbox.

// mailUsing are the capabilities of the mail method calls.
var mailUsing = []string{"urn:ietf:params:jmap:core", "urn:ietf:params:jmap:mail"}

// MailAccount gives the JMAP account ID of a mailbox.
func (c *Client) MailAccount(ctx context.Context, domain, localPart string) (string, error) {
	domainID, err := c.existingDomain(ctx, domain)
	if err != nil {
		return "", err
	}
	id, err := c.accountID(ctx, domainID, localPart)
	if err == nil && id == "" {
		err = fmt.Errorf("the mail server does not have the mailbox %s@%s", localPart, domain)
	}
	return id, err
}

// Folder is a folder (a JMAP mailbox) of a mailbox account.
type Folder struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	ParentID string `json:"parentId"`
	Role     string `json:"role"`
}

// Folders gives the folders of a mailbox account.
func (c *Client) Folders(ctx context.Context, accountID string) ([]Folder, error) {
	out, err := c.callUsing(ctx, mailUsing, "Mailbox/get", object{"accountId": accountID, "ids": nil, "properties": []string{"id", "name", "parentId", "role"}})
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
	return list, nil
}

// CreateFolder makes a folder in parentID ("" for the top) and gives its ID.
func (c *Client) CreateFolder(ctx context.Context, accountID, name, parentID string) (string, error) {
	value := object{"name": name, "parentId": nil}
	if parentID != "" {
		value["parentId"] = parentID
	}
	out, err := c.callUsing(ctx, mailUsing, "Mailbox/set", object{"accountId": accountID, "create": object{"f": value}})
	if err != nil {
		return "", err
	}
	if failed, ok := out["notCreated"].(object); ok && failed["f"] != nil {
		return "", setError("Mailbox/set", failed["f"])
	}
	created, _ := out["created"].(object)
	f, _ := created["f"].(object)
	id, _ := f["id"].(string)
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
	id, err := m.c.CreateFolder(ctx, m.accountID, name, parent)
	if err != nil {
		return "", fmt.Errorf("make the folder %s: %w", strings.Join(path, "/"), err)
	}
	m.folders = append(m.folders, Folder{ID: id, Name: name, ParentID: parent})
	m.byPath[key] = id
	return id, nil
}

// Upload sends the content of one message to the account and gives its blob ID.
func (c *Client) Upload(ctx context.Context, accountID string, r io.Reader) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.URL, "/")+"/jmap/upload/"+accountID+"/", r)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "message/rfc822")
	req.SetBasicAuth(AdminUser, c.Password)
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("mail server: upload: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("mail server: upload: status %d: %s", resp.StatusCode, short(raw))
	}
	var out struct {
		BlobID string `json:"blobId"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.BlobID == "" {
		return "", fmt.Errorf("mail server: upload: the answer has no blob ID: %s", short(raw))
	}
	return out.BlobID, nil
}

// Email is one message that ImportEmails adds to a folder.
type Email struct {
	BlobID   string
	FolderID string
	Keywords []string
	Received time.Time
}

// ImportEmails adds uploaded messages to their folders. It gives an error for each message
// that the mail server refused, by its index in emails.
func (c *Client) ImportEmails(ctx context.Context, accountID string, emails []Email) (map[int]error, error) {
	create := object{}
	for i, e := range emails {
		keywords := object{}
		for _, k := range e.Keywords {
			keywords[k] = true
		}
		v := object{"blobId": e.BlobID, "mailboxIds": object{e.FolderID: true}, "keywords": keywords}
		if !e.Received.IsZero() {
			v["receivedAt"] = e.Received.UTC().Format(time.RFC3339)
		}
		create[strconv.Itoa(i)] = v
	}
	out, err := c.callUsing(ctx, mailUsing, "Email/import", object{"accountId": accountID, "emails": create})
	if err != nil {
		return nil, err
	}
	failed := map[int]error{}
	if notCreated, ok := out["notCreated"].(object); ok {
		keys := make([]string, 0, len(notCreated))
		for k := range notCreated {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			i, err := strconv.Atoi(k)
			if err != nil || i < 0 || i >= len(emails) {
				continue
			}
			e, _ := notCreated[k].(object)
			failed[i] = fmt.Errorf("%v: %v", e["type"], e["description"])
		}
	}
	return failed, nil
}
