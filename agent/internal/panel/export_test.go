package panel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// exportPanel is the export route of a panel. It keeps a part as the panel does: a piece at
// the offset 0 starts the part again, and a piece at a different offset must start at the end.
type exportPanel struct {
	mu       sync.Mutex
	part     []byte
	requests int
	// failFirst gives status 502 to the first try of each piece.
	failFirst map[int64]bool
	status    int
}

func (p *exportPanel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests++
	if r.Method != http.MethodPut || r.URL.Path != "/api/agent/v1/export-parts/01JBQ3Z5K8N6T4W2X9Y7R5P3E1" || r.Header.Get("Authorization") != "Bearer cfa_x" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if p.status != 0 {
		w.WriteHeader(p.status)
		fmt.Fprint(w, `{"message":"The panel does not have enough free disk space for the export."}`)
		return
	}
	offset, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	body, _ := io.ReadAll(r.Body)
	if offset == 0 {
		p.part = nil
	}
	if offset != int64(len(p.part)) {
		w.WriteHeader(http.StatusConflict)
		fmt.Fprintf(w, `{"sizeBytes":%d}`, len(p.part))
		return
	}
	p.part = append(p.part, body...)
	if p.failFirst[offset] {
		// The panel keeps the piece, but the answer does not arrive.
		delete(p.failFirst, offset)
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	fmt.Fprintf(w, `{"sizeBytes":%d}`, len(p.part))
}

func TestUploadExportPartSendsPieces(t *testing.T) {
	p := &exportPanel{failFirst: map[int64]bool{ExportChunk: true}}
	srv := httptest.NewServer(p)
	defer srv.Close()
	c := New(srv.URL, "cfa_x", nil, "", "")
	c.Backoff = time.Millisecond
	data := bytes.Repeat([]byte("0123456789"), ExportChunk/10*2+7)
	size, sum, err := c.UploadExportPart(context.Background(), "01JBQ3Z5K8N6T4W2X9Y7R5P3E1", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(data)
	if size != int64(len(data)) || sum != hex.EncodeToString(want[:]) {
		t.Errorf("size %d, sum %s", size, sum)
	}
	// The second piece arrived two times. The panel kept it one time.
	if !bytes.Equal(p.part, data) {
		t.Errorf("the panel has %d bytes, want %d", len(p.part), len(data))
	}
	if p.requests != 4 {
		t.Errorf("%d requests, want 4: three pieces and one try again", p.requests)
	}
}

func TestUploadExportPartSendsAnEmptyPart(t *testing.T) {
	p := &exportPanel{}
	srv := httptest.NewServer(p)
	defer srv.Close()
	size, _, err := New(srv.URL, "cfa_x", nil, "", "").UploadExportPart(context.Background(), "01JBQ3Z5K8N6T4W2X9Y7R5P3E1", strings.NewReader(""))
	if err != nil || size != 0 || p.requests != 1 {
		t.Fatalf("size %d, err %v, %d requests", size, err, p.requests)
	}
}

// The panel refuses a piece when its disk is full. The agent does not try again.
func TestUploadExportPartStopsWhenThePanelHasNoSpace(t *testing.T) {
	p := &exportPanel{status: http.StatusInsufficientStorage}
	srv := httptest.NewServer(p)
	defer srv.Close()
	_, _, err := New(srv.URL, "cfa_x", nil, "", "").UploadExportPart(context.Background(), "01JBQ3Z5K8N6T4W2X9Y7R5P3E1", strings.NewReader("data"))
	if err == nil || !strings.Contains(err.Error(), "free disk space") || p.requests != 1 {
		t.Fatalf("err %v, %d requests", err, p.requests)
	}
}

// An error of the data stops the upload with that error.
func TestUploadExportPartGivesTheErrorOfTheData(t *testing.T) {
	srv := httptest.NewServer(&exportPanel{})
	defer srv.Close()
	pr, pw := io.Pipe()
	go func() {
		pw.Write([]byte("start"))
		pw.CloseWithError(fmt.Errorf("the dump failed"))
	}()
	if _, _, err := New(srv.URL, "cfa_x", nil, "", "").UploadExportPart(context.Background(), "01JBQ3Z5K8N6T4W2X9Y7R5P3E1", pr); err == nil || !strings.Contains(err.Error(), "the dump failed") {
		t.Fatalf("err %v", err)
	}
}
