package sitelogs

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// The limits of the receiver.
const (
	// maxCaddyLine is the largest entry of Caddy that the receiver reads. An entry has the
	// headers of the request, so it can be large. The receiver ignores a larger entry.
	maxCaddyLine = 1 << 20
	// queueLines is the number of entries that wait for the store. When the store is slow,
	// the receiver drops new entries: Caddy writes each entry while it answers a request, and
	// must never wait for the agent.
	queueLines = 20000
	// maxConnections is the largest number of connections at one time. Caddy opens one.
	maxConnections = 4
	// flushBytes is the size of the entries that the receiver keeps before it writes them.
	flushBytes = 1 << 20
)

// Receiver receives the access log of the edge proxy, and writes the entries of each website
// to the store. Caddy connects to Addr and sends each entry as one line of JSON (the "net"
// log writer).
//
// The host firewall lets only root and the edge proxy connect to the private service
// address (install.sh). Thus a website cannot send entries.
type Receiver struct {
	Addr  string
	Store *Store
	// Flush is the time between two writes to the store. 0 means one second.
	Flush time.Duration
	// Prune is the time between two removals of old files. 0 means one hour.
	Prune time.Duration
	Now   func() time.Time

	queue    chan queued
	dropped  atomic.Int64
	once     sync.Once
	listener net.Listener
	ready    chan struct{}
}

type queued struct {
	site string
	line []byte
}

func (r *Receiver) init() {
	r.once.Do(func() {
		r.queue = make(chan queued, queueLines)
		r.ready = make(chan struct{})
	})
}

func (r *Receiver) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Dropped gives the number of entries that the receiver dropped because the store was slow.
func (r *Receiver) Dropped() int64 { return r.dropped.Load() }

// Listening is closed when the receiver listens. Address then gives the address.
func (r *Receiver) Listening() <-chan struct{} {
	r.init()
	return r.ready
}

// Address gives the address on which the receiver listens.
func (r *Receiver) Address() string { return r.listener.Addr().String() }

// Run receives entries until ctx ends. When the address is not available, for example
// before the installer added the private service address, it tries again every 30 seconds.
func (r *Receiver) Run(ctx context.Context) {
	r.init()
	var l net.Listener
	for {
		var err error
		if l, err = (&net.ListenConfig{}).Listen(ctx, "tcp", r.Addr); err == nil {
			break
		}
		slog.Warn("the agent cannot receive the access log of the edge proxy; it tries again in 30 seconds", "address", r.Addr, "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
	}
	r.listener = l
	close(r.ready)
	go func() {
		<-ctx.Done()
		l.Close()
	}()
	done := make(chan struct{})
	go func() {
		r.write(ctx)
		close(done)
	}()
	slots := make(chan struct{}, maxConnections)
	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			slog.Warn("accept a connection of the edge proxy", "err", err)
			time.Sleep(time.Second)
			continue
		}
		select {
		case slots <- struct{}{}:
			go func() {
				defer func() { <-slots }()
				r.serve(ctx, conn)
			}()
		default:
			conn.Close()
		}
	}
	<-done
}

// serve reads the entries of one connection.
func (r *Receiver) serve(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	br := bufio.NewReaderSize(conn, 64<<10)
	for {
		line, err := readLine(br, maxCaddyLine)
		if len(line) > 0 {
			r.Receive(line)
		}
		if err != nil {
			return
		}
	}
}

// readLine reads one line with no line end. It drops a line that is longer than max, and
// gives the next line.
func readLine(br *bufio.Reader, max int) ([]byte, error) {
	var buf []byte
	tooLong := false
	for {
		part, err := br.ReadSlice('\n')
		if !tooLong {
			if len(buf)+len(part) > max+1 {
				tooLong, buf = true, nil
			} else {
				buf = append(buf, part...)
			}
		}
		switch {
		case err == nil:
			if tooLong {
				tooLong = false
				continue
			}
			return bytes.TrimSuffix(buf, []byte("\n")), nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			// An entry with no line end is not complete.
			return nil, err
		default:
			return nil, err
		}
	}
}

// Receive reads one entry of Caddy and puts it in the queue of the store.
func (r *Receiver) Receive(line []byte) {
	r.init()
	site, a, err := ParseCaddy(line, r.now())
	if err != nil {
		return
	}
	select {
	case r.queue <- queued{site: site, line: a.Line()}:
	default:
		r.dropped.Add(1)
	}
}

// write writes the entries of the queue to the store, and removes the old files.
func (r *Receiver) write(ctx context.Context) {
	flush := time.NewTicker(cmpOr(r.Flush, time.Second))
	defer flush.Stop()
	prune := time.NewTicker(cmpOr(r.Prune, time.Hour))
	defer prune.Stop()
	pending := map[string][]byte{}
	size := 0
	lastWarn := time.Time{}
	save := func() {
		for site, data := range pending {
			err := r.Store.Append(site, data)
			// A website that the agent removed has no directory: its last entries go.
			if err != nil && !errors.Is(err, ErrUnknownLog) && time.Since(lastWarn) > time.Minute {
				lastWarn = time.Now()
				slog.Warn("write the access log of a website", "website", site, "err", err)
			}
		}
		clear(pending)
		size = 0
	}
	if err := r.Store.Prune(); err != nil {
		slog.Warn("remove the old access logs", "err", err)
	}
	for {
		select {
		case <-ctx.Done():
			// The entries of the queue go to the store before the agent stops.
			for {
				select {
				case q := <-r.queue:
					pending[q.site] = append(pending[q.site], q.line...)
				default:
					save()
					return
				}
			}
		case q := <-r.queue:
			pending[q.site] = append(pending[q.site], q.line...)
			if size += len(q.line); size >= flushBytes {
				save()
			}
		case <-flush.C:
			save()
		case <-prune.C:
			if err := r.Store.Prune(); err != nil {
				slog.Warn("remove the old access logs", "err", err)
			}
		}
	}
}
