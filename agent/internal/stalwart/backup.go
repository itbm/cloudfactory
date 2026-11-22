package stalwart

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// A mailbox backup is a tar stream in the format MailboxFormat:
//
//  1. MailboxIndex: the address, the folders, and the facts of each message: its folders,
//     its keywords (for example "$seen") and the time that it arrived.
//  2. One file for each message ("messages/<id>.eml"), as the mail server keeps it
//     (RFC 5322). A message that the owner deletes during the export has no file.
//  3. MailboxEnd: the number of message files. A stream without it is not complete.
//
// The name, the time and the order of the file of a message do not change between two
// backups. restic thus finds the same data in the stream of the next night, and uploads only
// the new messages.
const (
	MailboxFormat = "cf-mailbox/1"
	MailboxIndex  = "mailbox.json"
	MailboxEnd    = "mailbox.end"
)

// ErrNotBackup is the error of a stream that is not a complete mailbox backup.
var ErrNotBackup = errors.New("the data is not a complete mailbox backup")

// ErrMailboxFull is the error of a restore that stopped because the mailbox is full.
var ErrMailboxFull = errors.New("the mailbox is full. Make the mailbox larger, or delete messages, then restore again")

// MailboxIndexFile is the content of MailboxIndex.
type MailboxIndexFile struct {
	Format   string          `json:"format"`
	Address  string          `json:"address"`
	Folders  []BackupFolder  `json:"folders"`
	Messages []BackupMessage `json:"messages"`
}

// BackupFolder is a folder of a mailbox (a JMAP Mailbox).
type BackupFolder struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ParentID   string `json:"parentId,omitempty"`
	Role       string `json:"role,omitempty"`
	SortOrder  int    `json:"sortOrder,omitempty"`
	Subscribed bool   `json:"subscribed,omitempty"`
}

// BackupMessage is the facts of a message (a JMAP Email). The content is in File.
type BackupMessage struct {
	File       string   `json:"file"`
	Folders    []string `json:"folders"`
	Keywords   []string `json:"keywords,omitempty"`
	ReceivedAt string   `json:"receivedAt"`
	Size       int64    `json:"size"`
	MessageID  string   `json:"messageId,omitempty"`
}

// key identifies a message for a restore: a message with the same Message-ID, time and size
// is in the mailbox already.
func (m BackupMessage) key() string {
	return m.MessageID + "\x00" + m.ReceivedAt + "\x00" + fmt.Sprint(m.Size)
}

// Export is the result of ExportMailbox.
type Export struct {
	Messages int
	Bytes    int64
}

// Import is the result of ImportMailbox.
type Import struct {
	// Added is the number of messages that the restore added.
	Added int
	// Present is the number of messages that the mailbox has already.
	Present int
	// TooLarge is the number of messages that are larger than the upload limit of the mail
	// server. The restore did not add them.
	TooLarge int
	// Failed is the number of other messages that the mail server did not accept.
	Failed int
	// FirstError is the reason of the first failed message.
	FirstError string
}

// page is the number of objects in one JMAP get call (maxObjectsInGet of the server).
const page = 500

// ExportMailbox writes a backup of a mailbox to w. A message that arrives during the export
// can be missing from the backup.
func (c *Client) ExportMailbox(ctx context.Context, domain, localPart string, w io.Writer) (Export, error) {
	account, err := c.mailboxAccount(ctx, domain, localPart)
	if err != nil {
		return Export{}, err
	}
	folders, err := c.folders(ctx, account)
	if err != nil {
		return Export{}, err
	}
	messages, err := c.messages(ctx, account)
	if err != nil {
		return Export{}, err
	}
	index := MailboxIndexFile{Format: MailboxFormat, Address: localPart + "@" + domain, Folders: folders, Messages: make([]BackupMessage, 0, len(messages))}
	for _, m := range messages {
		index.Messages = append(index.Messages, m.BackupMessage)
	}
	raw, err := json.Marshal(index)
	if err != nil {
		return Export{}, err
	}
	tw := tar.NewWriter(w)
	if err := writeFile(tw, MailboxIndex, time.Unix(0, 0), raw); err != nil {
		return Export{}, err
	}
	var out Export
	for _, m := range messages {
		n, err := c.exportMessage(ctx, tw, account, m)
		if err != nil {
			return out, err
		}
		if n > 0 {
			out.Messages++
			out.Bytes += n
		}
	}
	if err := writeFile(tw, MailboxEnd, time.Unix(0, 0), []byte(fmt.Sprintln(out.Messages))); err != nil {
		return out, err
	}
	return out, tw.Close()
}

func writeFile(tw *tar.Writer, name string, at time.Time, content []byte) error {
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o600, Size: int64(len(content)), ModTime: at}); err != nil {
		return err
	}
	_, err := tw.Write(content)
	return err
}

// exportMessage writes the file of one message. It gives 0 for a message that the mail server
// does not have now.
func (c *Client) exportMessage(ctx context.Context, tw *tar.Writer, account string, m listedMessage) (int64, error) {
	body, err := c.openBlob(ctx, account, m.blobID)
	if errors.Is(err, errNoBlob) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer body.Close()
	at, _ := time.Parse(time.RFC3339, m.ReceivedAt)
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: m.File, Mode: 0o600, Size: m.Size, ModTime: at.UTC()}); err != nil {
		return 0, err
	}
	n, err := io.Copy(tw, body)
	if err != nil {
		return n, fmt.Errorf("mail server: the message %s: %w", m.File, err)
	}
	if n != m.Size {
		return n, fmt.Errorf("mail server: the message %s has %d bytes, not %d", m.File, n, m.Size)
	}
	return n, nil
}

// folders gives the folders of an account, with each parent before its children.
func (c *Client) folders(ctx context.Context, account string) ([]BackupFolder, error) {
	result, err := c.call(ctx, "Mailbox/get", object{"accountId": account, "ids": nil, "properties": []string{"id", "name", "parentId", "role", "sortOrder", "isSubscribed"}})
	if err != nil {
		return nil, err
	}
	list, _ := result["list"].([]any)
	all := make([]BackupFolder, 0, len(list))
	for _, item := range list {
		o, _ := item.(object)
		f := BackupFolder{}
		f.ID, _ = o["id"].(string)
		f.Name, _ = o["name"].(string)
		f.ParentID, _ = o["parentId"].(string)
		f.Role, _ = o["role"].(string)
		order, _ := o["sortOrder"].(float64)
		f.SortOrder = int(order)
		f.Subscribed, _ = o["isSubscribed"].(bool)
		if f.ID != "" {
			all = append(all, f)
		}
	}
	return parentsFirst(all), nil
}

// parentsFirst sorts folders by depth, then by ID. A folder whose parent is missing goes to
// the top level.
func parentsFirst(all []BackupFolder) []BackupFolder {
	byID := map[string]BackupFolder{}
	for _, f := range all {
		byID[f.ID] = f
	}
	depth := func(f BackupFolder) int {
		n := 0
		for seen := map[string]bool{f.ID: true}; f.ParentID != ""; n++ {
			parent, ok := byID[f.ParentID]
			if !ok || seen[parent.ID] {
				break
			}
			seen[parent.ID] = true
			f = parent
		}
		return n
	}
	out := make([]BackupFolder, 0, len(all))
	for _, f := range all {
		if _, ok := byID[f.ParentID]; !ok {
			f.ParentID = ""
		}
		out = append(out, f)
	}
	slices.SortStableFunc(out, func(a, b BackupFolder) int {
		if d := depth(a) - depth(b); d != 0 {
			return d
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

type listedMessage struct {
	BackupMessage
	id, blobID string
}

// messages gives all messages of an account, the oldest first.
func (c *Client) messages(ctx context.Context, account string) ([]listedMessage, error) {
	ids, err := c.emailIDs(ctx, account)
	if err != nil {
		return nil, err
	}
	out := make([]listedMessage, 0, len(ids))
	for chunk := range slices.Chunk(ids, page) {
		result, err := c.call(ctx, "Email/get", object{"accountId": account, "ids": chunk, "properties": []string{"id", "blobId", "mailboxIds", "keywords", "receivedAt", "size", "messageId"}})
		if err != nil {
			return nil, err
		}
		list, _ := result["list"].([]any)
		for _, item := range list {
			o, _ := item.(object)
			m := listedMessage{}
			m.id, _ = o["id"].(string)
			m.blobID, _ = o["blobId"].(string)
			if m.id == "" || m.blobID == "" {
				continue
			}
			m.File = "messages/" + m.id + ".eml"
			m.Folders = keys(o["mailboxIds"])
			m.Keywords = keys(o["keywords"])
			m.ReceivedAt, _ = o["receivedAt"].(string)
			size, _ := o["size"].(float64)
			m.Size = int64(size)
			if ids, _ := o["messageId"].([]any); len(ids) > 0 {
				m.MessageID, _ = ids[0].(string)
			}
			out = append(out, m)
		}
	}
	slices.SortStableFunc(out, func(a, b listedMessage) int {
		if c := strings.Compare(a.ReceivedAt, b.ReceivedAt); c != 0 {
			return c
		}
		return strings.Compare(a.id, b.id)
	})
	return out, nil
}

// keys gives the sorted keys of a JMAP set ({"a": true}).
func keys(v any) []string {
	m, _ := v.(object)
	out := make([]string, 0, len(m))
	for k, on := range m {
		if on == true {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// emailIDs gives the IDs of all messages of an account, the oldest first.
func (c *Client) emailIDs(ctx context.Context, account string) ([]string, error) {
	seen := map[string]bool{}
	var ids []string
	for position := 0; ; {
		result, err := c.call(ctx, "Email/query", object{"accountId": account, "sort": []any{object{"property": "receivedAt", "isAscending": true}}, "position": position, "limit": page})
		if err != nil {
			return nil, err
		}
		list, _ := result["ids"].([]any)
		for _, item := range list {
			if id, ok := item.(string); ok && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
		if len(list) == 0 {
			return ids, nil
		}
		position += len(list)
	}
}

var errNoBlob = errors.New("the mail server does not have the blob")

// openBlob gives the content of a blob of an account. The caller closes it.
func (c *Client) openBlob(ctx context.Context, account, blobID string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.URL, "/")+"/jmap/download/"+url.PathEscape(account)+"/"+url.PathEscape(blobID)+"/message.eml", nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(AdminUser, c.Password)
	resp, err := c.blobClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("mail server: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, errNoBlob
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
		resp.Body.Close()
		return nil, fmt.Errorf("mail server: download: status %d: %s", resp.StatusCode, short(raw))
	}
	return resp.Body, nil
}

// blobClient is the HTTP client for the content of messages. A large message can take more
// time than a method call, so it has no time limit of its own: the job has one.
func (c *Client) blobClient() *http.Client {
	if c.HTTP == nil {
		return &http.Client{}
	}
	client := *c.HTTP
	client.Timeout = 0
	return &client
}

// uploadMessage puts a message in an account and gives its blob ID.
func (c *Client) uploadMessage(ctx context.Context, account string, content io.Reader, size int64) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.URL, "/")+"/jmap/upload/"+url.PathEscape(account)+"/", content)
	if err != nil {
		return "", err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "message/rfc822")
	req.SetBasicAuth(AdminUser, c.Password)
	resp, err := c.blobClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("mail server: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		BlobID string `json:"blobId"`
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated || json.Unmarshal(raw, &out) != nil || out.BlobID == "" {
		return "", fmt.Errorf("mail server: upload: status %d: %s", resp.StatusCode, short(raw))
	}
	return out.BlobID, nil
}

// maxUpload gives the largest blob that the mail server accepts (maxSizeUpload).
func (c *Client) maxUpload(ctx context.Context) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.URL, "/")+"/jmap/session", nil)
	if err != nil {
		return 0, err
	}
	req.SetBasicAuth(AdminUser, c.Password)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return 0, fmt.Errorf("mail server: %w", err)
	}
	defer resp.Body.Close()
	var session struct {
		Capabilities map[string]struct {
			MaxSizeUpload int64 `json:"maxSizeUpload"`
		} `json:"capabilities"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&session) != nil {
		return 0, fmt.Errorf("mail server: the JMAP session did not load: status %d", resp.StatusCode)
	}
	if n := session.Capabilities["urn:ietf:params:jmap:core"].MaxSizeUpload; n > 0 {
		return n, nil
	}
	return 0, errors.New("mail server: the JMAP session has no upload limit")
}

// maxIndex is the largest index file that a restore reads: about 2 million messages.
const maxIndex = 512 << 20

// ImportMailbox restores a backup from r into a mailbox. It adds each message that the mailbox
// does not have, in its folders and with its keywords. It makes each folder that the mailbox
// does not have. It does not change or remove a message or a folder of the mailbox.
//
// A second restore of the same backup adds nothing. A restore that stops can thus start
// again.
func (c *Client) ImportMailbox(ctx context.Context, domain, localPart string, r io.Reader) (Import, error) {
	account, err := c.mailboxAccount(ctx, domain, localPart)
	if err != nil {
		return Import{}, err
	}
	tr := tar.NewReader(r)
	hdr, err := tr.Next()
	if err != nil || hdr.Name != MailboxIndex || hdr.Size > maxIndex {
		return Import{}, ErrNotBackup
	}
	var index MailboxIndexFile
	if err := json.NewDecoder(io.LimitReader(tr, maxIndex)).Decode(&index); err != nil || index.Format != MailboxFormat {
		return Import{}, fmt.Errorf("%w: the index file is not in the format %s", ErrNotBackup, MailboxFormat)
	}
	if !strings.EqualFold(index.Address, localPart+"@"+domain) {
		return Import{}, fmt.Errorf("the backup is of the mailbox %s, not of %s@%s", index.Address, localPart, domain)
	}
	folders, err := c.restoreFolders(ctx, account, index.Folders)
	if err != nil {
		return Import{}, err
	}
	present, err := c.messageKeys(ctx, account)
	if err != nil {
		return Import{}, err
	}
	limit, err := c.maxUpload(ctx)
	if err != nil {
		return Import{}, err
	}
	byFile := map[string]BackupMessage{}
	for _, m := range index.Messages {
		byFile[m.File] = m
	}
	var out Import
	files := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return out, fmt.Errorf("%w: the end of the backup is missing", ErrNotBackup)
		}
		if err != nil {
			return out, fmt.Errorf("%w: %v", ErrNotBackup, err)
		}
		if hdr.Name == MailboxEnd {
			var want int
			if _, err := fmt.Fscan(io.LimitReader(tr, 64), &want); err != nil || want != files {
				return out, fmt.Errorf("%w: the backup has %d messages, not %d", ErrNotBackup, files, want)
			}
			return out, nil
		}
		m, ok := byFile[hdr.Name]
		if !ok || hdr.Typeflag != tar.TypeReg {
			continue
		}
		files++
		if present[m.key()] {
			out.Present++
			continue
		}
		if hdr.Size > limit {
			out.TooLarge++
			continue
		}
		if err := c.importMessage(ctx, account, tr, hdr.Size, m, folders); err != nil {
			if errors.Is(err, ErrMailboxFull) {
				return out, err
			}
			var refused *SetError
			if !errors.As(err, &refused) {
				return out, err
			}
			out.Failed++
			if out.FirstError == "" {
				out.FirstError = fmt.Sprintf("%s (%s)", refused.Type, refused.Detail)
			}
			continue
		}
		present[m.key()] = true
		out.Added++
	}
}

// importMessage adds one message to an account.
func (c *Client) importMessage(ctx context.Context, account string, content io.Reader, size int64, m BackupMessage, folders map[string]string) error {
	blobID, err := c.uploadMessage(ctx, account, io.LimitReader(content, size), size)
	if err != nil {
		return err
	}
	in := object{}
	for _, f := range m.Folders {
		if id := folders[f]; id != "" {
			in[id] = true
		}
	}
	if len(in) == 0 {
		in[folders[inboxKey]] = true
	}
	keywords := object{}
	for _, k := range m.Keywords {
		keywords[k] = true
	}
	email := object{"blobId": blobID, "mailboxIds": in, "keywords": keywords}
	if m.ReceivedAt != "" {
		email["receivedAt"] = m.ReceivedAt
	}
	result, err := c.call(ctx, "Email/import", object{"accountId": account, "emails": object{"m": email}})
	if err != nil {
		return err
	}
	if failed, _ := result["notCreated"].(object); failed["m"] != nil {
		e := setError("Email/import", failed["m"])
		if e.Type == "overQuota" {
			return ErrMailboxFull
		}
		return e
	}
	return nil
}

// inboxKey is the key of the inbox in the folder map of a restore. A message whose folders
// are all missing goes to the inbox.
const inboxKey = "\x00inbox"

// restoreFolders gives the folder of the mailbox for each folder of the backup. It makes the
// folders that the mailbox does not have, with no role. A folder with a role (for example
// "sent") is the folder of the mailbox with that role. Other folders agree by their path of
// names.
func (c *Client) restoreFolders(ctx context.Context, account string, backup []BackupFolder) (map[string]string, error) {
	current, err := c.folders(ctx, account)
	if err != nil {
		return nil, err
	}
	byRole, byPath := map[string]string{}, map[string]string{}
	for _, f := range current {
		if f.Role != "" {
			byRole[f.Role] = f.ID
		}
		byPath[folderPath(current, f)] = f.ID
	}
	out := map[string]string{inboxKey: byRole["inbox"]}
	if out[inboxKey] == "" {
		return nil, errors.New("mail server: the mailbox has no inbox")
	}
	for _, f := range parentsFirst(backup) {
		if id := byRole[f.Role]; f.Role != "" && id != "" {
			out[f.ID] = id
			continue
		}
		path := folderPath(backup, f)
		if id := byPath[path]; id != "" {
			out[f.ID] = id
			continue
		}
		create := object{"name": f.Name, "parentId": nil, "isSubscribed": f.Subscribed, "sortOrder": f.SortOrder}
		if f.ParentID != "" {
			create["parentId"] = out[f.ParentID]
		}
		result, err := c.call(ctx, "Mailbox/set", object{"accountId": account, "create": object{"f": create}})
		if err != nil {
			return nil, err
		}
		created, _ := result["created"].(object)
		made, _ := created["f"].(object)
		id, _ := made["id"].(string)
		if id == "" {
			failed, _ := result["notCreated"].(object)
			return nil, setError("Mailbox/set", failed["f"])
		}
		out[f.ID] = id
		byPath[path] = id
	}
	return out, nil
}

// folderPath gives the names of a folder and its parents, from the top level.
func folderPath(all []BackupFolder, f BackupFolder) string {
	byID := map[string]BackupFolder{}
	for _, x := range all {
		byID[x.ID] = x
	}
	names := []string{f.Name}
	for seen := map[string]bool{f.ID: true}; f.ParentID != ""; {
		parent, ok := byID[f.ParentID]
		if !ok || seen[parent.ID] {
			break
		}
		seen[parent.ID] = true
		names = append(names, parent.Name)
		f = parent
	}
	slices.Reverse(names)
	return strings.Join(names, "\x00")
}

// messageKeys gives the key of each message of an account (BackupMessage.key).
func (c *Client) messageKeys(ctx context.Context, account string) (map[string]bool, error) {
	ids, err := c.emailIDs(ctx, account)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(ids))
	for chunk := range slices.Chunk(ids, page) {
		result, err := c.call(ctx, "Email/get", object{"accountId": account, "ids": chunk, "properties": []string{"receivedAt", "size", "messageId"}})
		if err != nil {
			return nil, err
		}
		list, _ := result["list"].([]any)
		for _, item := range list {
			o, _ := item.(object)
			m := BackupMessage{}
			m.ReceivedAt, _ = o["receivedAt"].(string)
			size, _ := o["size"].(float64)
			m.Size = int64(size)
			if ids, _ := o["messageId"].([]any); len(ids) > 0 {
				m.MessageID, _ = ids[0].(string)
			}
			out[m.key()] = true
		}
	}
	return out, nil
}
