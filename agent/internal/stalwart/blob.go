package stalwart

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The functions in this file send and get the content of blobs of an account (RFC 8620,
// section 6): messages, Sieve scripts and files.

// httpClient is the HTTP client for a short request.
func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// blobClient is the HTTP client for the content of a blob. A large message can take more time
// than a method call, so it has no time limit of its own: the job has one.
func (c *Client) blobClient() *http.Client {
	if c.HTTP == nil {
		return &http.Client{}
	}
	client := *c.HTTP
	client.Timeout = 0
	return &client
}

// upload sends size bytes from r to the account, and gives the ID of the blob.
func (c *Client) upload(ctx context.Context, account, contentType string, r io.Reader, size int64) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.URL, "/")+"/jmap/upload/"+url.PathEscape(account)+"/", r)
	if err != nil {
		return "", err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", contentType)
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

var errNoBlob = errors.New("the mail server does not have the blob")

// openBlob gives the content of a blob of an account, with the file name name. The caller
// closes it.
func (c *Client) openBlob(ctx context.Context, account, blobID, name string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.URL, "/")+"/jmap/download/"+url.PathEscape(account)+"/"+url.PathEscape(blobID)+"/"+url.PathEscape(name), nil)
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
