package panel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// ExportChunk is the largest piece of a part of an account export in one request. The panel
// has the same limit (AccountExports::CHUNK_BYTES). It is less than the limit of the panel for
// the body of a request (post_max_size, 8 MiB).
const ExportChunk = 4 << 20

// exportChunkTimeout is the time limit of one piece. A piece of 4 MiB needs approximately
// 35 seconds at 1 Mbit/s.
const exportChunkTimeout = 5 * time.Minute

// UploadExportPart sends one part of an account export to the panel, in pieces of ExportChunk
// bytes: PUT /api/agent/v1/export-parts/{id}?offset=N. It reads r to the end, and gives the
// number of bytes and their SHA-256 hash. The job result has them, and the panel compares them
// with the part that it has.
//
// Each piece names its offset in the part. The panel keeps a piece only when the offset is the
// size of the part that it has, and offset 0 starts the part again. Thus a piece that the
// agent sends again after an error of the connection does not go into the part two times, and
// a job that runs again after a restart of the agent sends the part again from the start.
func (c *Client) UploadExportPart(ctx context.Context, partID string, r io.Reader) (int64, string, error) {
	h := sha256.New()
	buf := make([]byte, ExportChunk)
	var offset int64
	for {
		n, err := io.ReadFull(r, buf)
		end := errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
		if err != nil && !end {
			return 0, "", fmt.Errorf("make the export part: %w", err)
		}
		// A part with no bytes also needs one request, so that the panel has the part.
		if n > 0 || offset == 0 {
			h.Write(buf[:n])
			if err := c.putExportChunk(ctx, partID, offset, buf[:n]); err != nil {
				return 0, "", err
			}
			offset += int64(n)
		}
		if end {
			return offset, hex.EncodeToString(h.Sum(nil)), nil
		}
	}
}

// putExportChunk sends one piece, and tries again when the panel is not available.
func (c *Client) putExportChunk(ctx context.Context, partID string, offset int64, chunk []byte) error {
	var last error
	for attempt := 0; attempt <= c.Retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(c.Backoff * time.Duration(1<<(attempt-1))):
			}
		}
		retry, err := c.putExportChunkOnce(ctx, partID, offset, chunk)
		if err == nil {
			return nil
		}
		last = err
		if !retry || ctx.Err() != nil {
			return err
		}
	}
	return last
}

func (c *Client) putExportChunkOnce(ctx context.Context, partID string, offset int64, chunk []byte) (retry bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, exportChunkTimeout)
	defer cancel()
	path := "/api/agent/v1/export-parts/" + partID
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.BaseURL+path+"?offset="+strconv.FormatInt(offset, 10), bytes.NewReader(chunk))
	if err != nil {
		return false, err
	}
	req.ContentLength = int64(len(chunk))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	// The client of the reports has a time limit of 30 seconds for each request. A piece can
	// need more time on a slow connection.
	client := *c.HTTP
	client.Timeout = 0
	resp, err := client.Do(req)
	if err != nil {
		return true, fmt.Errorf("panel: %s: %w", path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var answer struct {
		SizeBytes *int64 `json:"sizeBytes"`
		Message   string `json:"message"`
	}
	_ = json.Unmarshal(data, &answer)
	switch {
	case resp.StatusCode == http.StatusOK:
		return false, nil
	case resp.StatusCode == http.StatusConflict && answer.SizeBytes != nil && *answer.SizeBytes == offset+int64(len(chunk)):
		// The panel has this piece already: the answer to an earlier try did not arrive.
		return false, nil
	case resp.StatusCode >= 500 && resp.StatusCode != http.StatusInsufficientStorage, resp.StatusCode == http.StatusTooManyRequests:
		return true, fmt.Errorf("panel: %s: status %d", path, resp.StatusCode)
	}
	if answer.Message == "" {
		answer.Message = http.StatusText(resp.StatusCode)
	}
	return false, fmt.Errorf("panel: %s: status %d: %s", path, resp.StatusCode, answer.Message)
}
