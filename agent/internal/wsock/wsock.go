// Package wsock is a small WebSocket implementation (RFC 6455) for the terminal of a website.
// The terminal relay of the panel accepts the connections of the browser and of the agent.
// The agent dials the relay. Only the parts that the terminal needs are here: text and
// binary messages, ping and pong, and the close handshake. There are no extensions.
package wsock

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// The types of a message.
const (
	Text   = 1
	Binary = 2
)

// The operation codes of a frame.
const (
	opContinue = 0
	opClose    = 8
	opPing     = 9
	opPong     = 10
)

// MaxMessage is the largest message that a connection accepts.
const MaxMessage = 1 << 20

// The close codes that the terminal uses.
const (
	CloseNormal    = 1000
	CloseGoingAway = 1001
	CloseProtocol  = 1002
	CloseTooLarge  = 1009
	ClosePolicy    = 1008
)

// guid is the value that RFC 6455 adds to the key of the client.
const guid = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// ErrProtocol is a frame that RFC 6455 does not permit.
var ErrProtocol = errors.New("wsock: protocol error")

// CloseError is the close frame of the other side.
type CloseError struct {
	Code   int
	Reason string
}

func (e *CloseError) Error() string {
	if e.Reason == "" {
		return fmt.Sprintf("wsock: closed with code %d", e.Code)
	}
	return fmt.Sprintf("wsock: closed with code %d: %s", e.Code, e.Reason)
}

// Conn is one WebSocket connection. One goroutine can read while other goroutines write.
type Conn struct {
	conn net.Conn
	br   *bufio.Reader
	// client is true on the side that dialled. A client masks its frames, and a server does
	// not (RFC 6455, section 5.1).
	client bool

	wmu       sync.Mutex
	closeSent bool
}

// accept gives the value of Sec-WebSocket-Accept for a key.
func accept(key string) string {
	h := sha1.Sum([]byte(key + guid))
	return base64.StdEncoding.EncodeToString(h[:])
}

// hasToken reports whether a header has a token in its comma-separated list, with no
// difference between capital and small letters.
func hasToken(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for _, t := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(t), token) {
				return true
			}
		}
	}
	return false
}

// Upgrade changes an HTTP request to a WebSocket connection. On an error, it writes the
// answer to the request.
func Upgrade(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	key := r.Header.Get("Sec-WebSocket-Key")
	raw, err := base64.StdEncoding.DecodeString(key)
	switch {
	case r.Method != http.MethodGet:
		err = errors.New("wsock: the method is not GET")
	case !hasToken(r.Header, "Connection", "upgrade") || !hasToken(r.Header, "Upgrade", "websocket"):
		err = errors.New("wsock: the request is not a WebSocket upgrade")
	case r.Header.Get("Sec-WebSocket-Version") != "13":
		w.Header().Set("Sec-WebSocket-Version", "13")
		err = errors.New("wsock: the WebSocket version is not 13")
	case err != nil || len(raw) != 16:
		err = errors.New("wsock: the WebSocket key is not valid")
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return nil, err
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "wsock: the server cannot change the connection", http.StatusInternalServerError)
		return nil, errors.New("wsock: the response writer cannot hijack")
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return nil, err
	}
	answer := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + accept(key) + "\r\n\r\n"
	if _, err := conn.Write([]byte(answer)); err != nil {
		conn.Close()
		return nil, err
	}
	return &Conn{conn: conn, br: rw.Reader}, nil
}

// Options are the options of Dial. All can be empty.
type Options struct {
	// Header has more headers for the upgrade request.
	Header http.Header
	// TLS is the TLS configuration for wss and https.
	TLS *tls.Config
	// DialContext opens the TCP connection. The default is net.Dialer.
	DialContext func(ctx context.Context, network, address string) (net.Conn, error)
}

// Dial opens a WebSocket connection. The address has the scheme ws, wss, http or https.
func Dial(ctx context.Context, address string, opts Options) (*Conn, error) {
	header, tlsConfig := opts.Header, opts.TLS
	u, err := url.Parse(address)
	if err != nil {
		return nil, err
	}
	secure := false
	switch u.Scheme {
	case "ws", "http":
	case "wss", "https":
		secure = true
	default:
		return nil, fmt.Errorf("wsock: the scheme %q is not valid", u.Scheme)
	}
	host := u.Host
	if u.Port() == "" {
		port := "80"
		if secure {
			port = "443"
		}
		host = net.JoinHostPort(u.Hostname(), port)
	}
	dial := opts.DialContext
	if dial == nil {
		dial = (&net.Dialer{Timeout: 15 * time.Second}).DialContext
	}
	conn, err := dial(ctx, "tcp", host)
	if err != nil {
		return nil, err
	}
	// The handshake must end before the context ends.
	stop := context.AfterFunc(ctx, func() { conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()
	if secure {
		cfg := &tls.Config{MinVersion: tls.VersionTLS12}
		if tlsConfig != nil {
			cfg = tlsConfig.Clone()
		}
		if cfg.ServerName == "" {
			cfg.ServerName = u.Hostname()
		}
		// A WebSocket upgrade needs HTTP/1.1.
		cfg.NextProtos = []string{"http/1.1"}
		tc := tls.Client(conn, cfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, err
		}
		conn = tc
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		conn.Close()
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(nonce)
	req := &http.Request{Method: http.MethodGet, URL: &url.URL{Path: u.Path, RawPath: u.RawPath, RawQuery: u.RawQuery}, Header: http.Header{}, Host: u.Host, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1}
	if req.URL.Path == "" {
		req.URL.Path = "/"
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Key", key)
	req.Header.Set("Sec-WebSocket-Version", "13")
	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		resp.Body.Close()
		conn.Close()
		return nil, &HandshakeError{Status: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	if !hasToken(resp.Header, "Upgrade", "websocket") || resp.Header.Get("Sec-WebSocket-Accept") != accept(key) {
		conn.Close()
		return nil, errors.New("wsock: the server did not accept the WebSocket upgrade")
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		conn.Close()
		return nil, err
	}
	return &Conn{conn: conn, br: br, client: true}, nil
}

// HandshakeError is an answer to the upgrade request that is not 101.
type HandshakeError struct {
	Status int
	Body   string
}

func (e *HandshakeError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("wsock: the server answered with status %d", e.Status)
	}
	return fmt.Sprintf("wsock: the server answered with status %d: %s", e.Status, e.Body)
}

// SetReadDeadline sets the time at which ReadMessage stops.
func (c *Conn) SetReadDeadline(t time.Time) error { return c.conn.SetReadDeadline(t) }

// RemoteAddr gives the address of the other side.
func (c *Conn) RemoteAddr() net.Addr { return c.conn.RemoteAddr() }

// ReadMessage gives the next text or binary message. It answers a ping, and it gives a
// *CloseError when the other side closes the connection.
func (c *Conn) ReadMessage() (int, []byte, error) {
	var (
		kind int
		msg  []byte
	)
	for {
		fin, op, payload, err := c.readFrame()
		if err != nil {
			return 0, nil, err
		}
		switch op {
		case opPing:
			if err := c.writeFrame(opPong, payload); err != nil {
				return 0, nil, err
			}
			continue
		case opPong:
			continue
		case opClose:
			ce := &CloseError{Code: 1005}
			if len(payload) >= 2 {
				ce.Code = int(binary.BigEndian.Uint16(payload))
				ce.Reason = string(payload[2:])
			}
			_ = c.WriteClose(CloseNormal, "")
			return 0, nil, ce
		case Text, Binary:
			if kind != 0 {
				return 0, nil, c.fail(CloseProtocol, ErrProtocol)
			}
			kind = op
		case opContinue:
			if kind == 0 {
				return 0, nil, c.fail(CloseProtocol, ErrProtocol)
			}
		default:
			return 0, nil, c.fail(CloseProtocol, ErrProtocol)
		}
		if len(msg)+len(payload) > MaxMessage {
			return 0, nil, c.fail(CloseTooLarge, errors.New("wsock: the message is too large"))
		}
		msg = append(msg, payload...)
		if fin {
			return kind, msg, nil
		}
	}
}

// fail sends a close frame and closes the connection.
func (c *Conn) fail(code int, err error) error {
	_ = c.WriteClose(code, "")
	c.conn.Close()
	return err
}

func (c *Conn) readFrame() (fin bool, op int, payload []byte, err error) {
	var head [2]byte
	if _, err = io.ReadFull(c.br, head[:]); err != nil {
		return
	}
	fin = head[0]&0x80 != 0
	if head[0]&0x70 != 0 {
		// No extension is in use, so the reserved bits must be 0.
		return false, 0, nil, c.fail(CloseProtocol, ErrProtocol)
	}
	op = int(head[0] & 0x0f)
	masked := head[1]&0x80 != 0
	// A client masks each frame. A server masks none.
	if masked == c.client {
		return false, 0, nil, c.fail(CloseProtocol, ErrProtocol)
	}
	n := uint64(head[1] & 0x7f)
	switch n {
	case 126:
		var b [2]byte
		if _, err = io.ReadFull(c.br, b[:]); err != nil {
			return
		}
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err = io.ReadFull(c.br, b[:]); err != nil {
			return
		}
		n = binary.BigEndian.Uint64(b[:])
	}
	control := op >= 8
	if control && (n > 125 || !fin) {
		return false, 0, nil, c.fail(CloseProtocol, ErrProtocol)
	}
	if n > MaxMessage {
		return false, 0, nil, c.fail(CloseTooLarge, errors.New("wsock: the frame is too large"))
	}
	var mask [4]byte
	if masked {
		if _, err = io.ReadFull(c.br, mask[:]); err != nil {
			return
		}
	}
	payload = make([]byte, n)
	if _, err = io.ReadFull(c.br, payload); err != nil {
		return
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return fin, op, payload, nil
}

// WriteMessage sends one text or binary message.
func (c *Conn) WriteMessage(kind int, data []byte) error {
	if kind != Text && kind != Binary {
		return fmt.Errorf("wsock: the message type %d is not valid", kind)
	}
	return c.writeFrame(kind, data)
}

// WriteClose sends a close frame. It sends only the first close frame.
func (c *Conn) WriteClose(code int, reason string) error {
	if len(reason) > 120 {
		reason = reason[:120]
	}
	payload := make([]byte, 2, 2+len(reason))
	binary.BigEndian.PutUint16(payload, uint16(code))
	payload = append(payload, reason...)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closeSent {
		return nil
	}
	c.closeSent = true
	_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return c.writeLocked(opClose, payload)
}

// Ping sends a ping. The other side answers with a pong, which ReadMessage reads.
func (c *Conn) Ping() error { return c.writeFrame(opPing, nil) }

func (c *Conn) writeFrame(op int, data []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closeSent {
		return net.ErrClosed
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	return c.writeLocked(op, data)
}

func (c *Conn) writeLocked(op int, data []byte) error {
	head := make([]byte, 0, 14+len(data))
	head = append(head, 0x80|byte(op))
	mask := byte(0)
	if c.client {
		mask = 0x80
	}
	switch n := len(data); {
	case n <= 125:
		head = append(head, mask|byte(n))
	case n <= 0xffff:
		head = append(head, mask|126, byte(n>>8), byte(n))
	default:
		head = append(head, mask|127)
		head = binary.BigEndian.AppendUint64(head, uint64(n))
	}
	if c.client {
		var key [4]byte
		if _, err := rand.Read(key[:]); err != nil {
			return err
		}
		head = append(head, key[:]...)
		start := len(head)
		head = append(head, data...)
		for i := range data {
			head[start+i] ^= key[i%4]
		}
	} else {
		head = append(head, data...)
	}
	_, err := c.conn.Write(head)
	return err
}

// Close closes the connection with no close handshake. Send WriteClose first for a clean end.
func (c *Conn) Close() error { return c.conn.Close() }
