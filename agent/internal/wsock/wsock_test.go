package wsock

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// echo is a server that sends back each message, and closes after the message "bye".
func echo(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test") != "yes" {
			http.Error(w, "no header", http.StatusForbidden)
			return
		}
		c, err := Upgrade(w, r)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			kind, msg, err := c.ReadMessage()
			if err != nil {
				return
			}
			if string(msg) == "bye" {
				c.WriteClose(CloseNormal, "goodbye")
				c.ReadMessage()
				return
			}
			if err := c.WriteMessage(kind, msg); err != nil {
				return
			}
		}
	}))
}

func TestMessagesGoBothWays(t *testing.T) {
	srv := echo(t)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, strings.Replace(srv.URL, "http", "ws", 1)+"/path?x=1", Options{Header: http.Header{"X-Test": {"yes"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// A short message, a message with a 16-bit length and a message with a 64-bit length.
	for _, msg := range [][]byte{[]byte("hello"), bytes.Repeat([]byte("a"), 300), bytes.Repeat([]byte{0, 1, 2}, 30000)} {
		if err := c.WriteMessage(Binary, msg); err != nil {
			t.Fatal(err)
		}
		kind, got, err := c.ReadMessage()
		if err != nil || kind != Binary || !bytes.Equal(got, msg) {
			t.Fatalf("got %d %d bytes %v, want %d bytes", kind, len(got), err, len(msg))
		}
	}
	if err := c.Ping(); err != nil {
		t.Fatal(err)
	}
	if err := c.WriteMessage(Text, []byte(`{"type":"resize"}`)); err != nil {
		t.Fatal(err)
	}
	if kind, got, err := c.ReadMessage(); err != nil || kind != Text || string(got) != `{"type":"resize"}` {
		t.Fatalf("got %d %q %v", kind, got, err)
	}
	c.WriteMessage(Text, []byte("bye"))
	_, _, err = c.ReadMessage()
	var ce *CloseError
	if !errors.As(err, &ce) || ce.Code != CloseNormal || ce.Reason != "goodbye" {
		t.Fatalf("got %v, want the close frame of the server", err)
	}
}

func TestDialGivesTheAnswerOfARefusedUpgrade(t *testing.T) {
	srv := echo(t)
	defer srv.Close()
	_, err := Dial(context.Background(), srv.URL, Options{})
	var he *HandshakeError
	if !errors.As(err, &he) || he.Status != http.StatusForbidden || he.Body != "no header" {
		t.Fatalf("got %v", err)
	}
	if _, err := Dial(context.Background(), "ftp://example.com", Options{}); err == nil {
		t.Fatal("a scheme that is not ws or http must fail")
	}
}

func TestUpgradeRefusesARequestThatIsNotAWebSocket(t *testing.T) {
	srv := echo(t)
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("X-Test", "yes")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("got status %d", resp.StatusCode)
	}
}

func TestAServerRefusesAFrameWithNoMask(t *testing.T) {
	srv := echo(t)
	defer srv.Close()
	c, err := Dial(context.Background(), srv.URL, Options{Header: http.Header{"X-Test": {"yes"}}})
	if err != nil {
		t.Fatal(err)
	}
	// Send as a server would: with no mask. The server must close the connection.
	c.client = false
	c.WriteMessage(Text, []byte("x"))
	c.client = true
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _, err = c.ReadMessage()
	var ce *CloseError
	if !errors.As(err, &ce) || ce.Code != CloseProtocol {
		t.Fatalf("got %v, want a close with the code 1002", err)
	}
}

func TestAccept(t *testing.T) {
	// The example of RFC 6455, section 1.3.
	if got := accept("dGhlIHNhbXBsZSBub25jZQ=="); got != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatal(got)
	}
}
