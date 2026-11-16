package stalwart

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// KeyFolder and KeyFile name the file with the private keys of a mailbox, in the files of the
// mailbox. The PGP plugin of the webmail reads the file with the password of the mailbox, and
// adds the keys to the browser: the user does not upload the keys. Each key in the file is
// protected with the password of the mailbox.
const (
	KeyFolder = "OpenPGP"
	KeyFile   = "private-key.asc"
)

// SetKeyFile writes the private keys of a mailbox (ASCII armour) to KeyFolder/KeyFile in the
// files of the mailbox. The file is not changed when it has the same keys. Empty keys remove
// the file.
func (c *Client) SetKeyFile(ctx context.Context, domain, localPart, privateKeys string) error {
	domainID, err := c.existingDomain(ctx, domain)
	if err != nil {
		return err
	}
	account, err := c.accountID(ctx, domainID, localPart)
	if err != nil {
		return err
	}
	if account == "" {
		return fmt.Errorf("the mail server does not have the mailbox %s@%s", localPart, domain)
	}
	folder, err := c.fileNode(ctx, account, KeyFolder, "", "Directory")
	if err != nil {
		return err
	}
	var file object
	if folder != nil {
		if file, err = c.fileNode(ctx, account, KeyFile, folder["id"].(string), "File"); err != nil {
			return err
		}
	}
	if file != nil {
		if blobID, _ := file["blobId"].(string); privateKeys != "" && blobID != "" {
			old, err := c.download(ctx, account, blobID)
			if err != nil {
				return err
			}
			if old == privateKeys {
				return nil
			}
		}
	}
	// The new text goes to the server before the old file goes. If the upload fails, the
	// mailbox keeps its old key file (adversarial review, finding G8-10).
	blobID := ""
	if privateKeys != "" {
		var err error
		if blobID, err = c.upload(ctx, account, privateKeys); err != nil {
			return err
		}
	}
	if file != nil {
		if _, err := c.fileSet(ctx, account, object{"destroy": []string{file["id"].(string)}}); err != nil {
			return err
		}
	}
	if privateKeys == "" {
		return nil
	}
	create := object{}
	parent := ""
	if folder == nil {
		create["folder"] = object{"@type": "Directory", "name": KeyFolder}
		parent = "#folder"
	} else {
		parent = folder["id"].(string)
	}
	create["file"] = object{"@type": "File", "parentId": parent, "name": KeyFile, "blobId": blobID, "mediaType": "application/pgp-keys"}
	result, err := c.fileSet(ctx, account, object{"create": create})
	if err != nil {
		return err
	}
	if failed, _ := result["notCreated"].(object); len(failed) > 0 {
		for _, key := range []string{"folder", "file"} {
			if failed[key] != nil {
				return setError("FileNode/set", failed[key])
			}
		}
	}
	return nil
}

// fileNode gives the node with this name and type in a folder (parent), or in the top level
// for an empty parent. It gives nil when there is no such node.
func (c *Client) fileNode(ctx context.Context, account, name, parent, kind string) (object, error) {
	found, err := c.call(ctx, "FileNode/query", object{"accountId": account, "filter": object{"name": name}})
	if err != nil {
		return nil, err
	}
	ids, _ := found["ids"].([]any)
	if len(ids) == 0 {
		return nil, nil
	}
	got, err := c.call(ctx, "FileNode/get", object{"accountId": account, "ids": ids, "properties": []string{"id", "parentId", "name", "blobId", "@type"}})
	if err != nil {
		return nil, err
	}
	list, _ := got["list"].([]any)
	for _, item := range list {
		o, ok := item.(object)
		if !ok || o["name"] != name || o["@type"] != kind {
			continue
		}
		if p, _ := o["parentId"].(string); p == parent {
			return o, nil
		}
	}
	return nil, nil
}

func (c *Client) fileSet(ctx context.Context, account string, args object) (object, error) {
	args["accountId"] = account
	result, err := c.call(ctx, "FileNode/set", args)
	if err != nil {
		return nil, err
	}
	if failed, _ := result["notDestroyed"].(object); len(failed) > 0 {
		for _, v := range failed {
			if e := setError("FileNode/set", v); e.Type != "notFound" {
				return nil, e
			}
		}
	}
	return result, nil
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// upload puts a blob in the account and gives its ID.
func (c *Client) upload(ctx context.Context, account, content string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.URL, "/")+"/jmap/upload/"+url.PathEscape(account)+"/", bytes.NewReader([]byte(content)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/pgp-keys")
	req.SetBasicAuth(AdminUser, c.Password)
	resp, err := c.httpClient().Do(req)
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

// download gives the content of a blob of the account.
func (c *Client) download(ctx context.Context, account, blobID string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.URL, "/")+"/jmap/download/"+url.PathEscape(account)+"/"+url.PathEscape(blobID)+"/"+KeyFile, nil)
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(AdminUser, c.Password)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("mail server: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("mail server: download: status %d: %s", resp.StatusCode, short(raw))
	}
	return string(raw), nil
}
