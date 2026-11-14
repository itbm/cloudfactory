package gate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
)

// Client is the side of the worker. Each request opens one connection to the socket of the
// root helper.
type Client struct {
	// Path is the Unix socket of the root helper.
	Path string
}

// DefaultSocket is the socket of the root helper. Only root and the user of the worker can
// open it.
const DefaultSocket = "/run/cloudfactory/gate.sock"

// open connects to the helper and sends the request. When ctx ends, the connection closes,
// and the helper stops the command.
func (c *Client) open(ctx context.Context, req request) (net.Conn, func() bool, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", c.Path)
	if err != nil {
		return nil, nil, fmt.Errorf("connect to the root helper: %w", err)
	}
	b, err := json.Marshal(req)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	if err := writeMessage(conn, msgRequest, b); err != nil {
		stop()
		conn.Close()
		return nil, nil, c.fault(ctx, err)
	}
	return conn, stop, nil
}

// fault gives the error of a broken connection. A request that ctx stopped gives the error
// of ctx.
func (c *Client) fault(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("the connection to the root helper stopped: %w", err)
}

// finish reads the messages of the helper until its response. It writes the output
// messages to output.
func (c *Client) finish(ctx context.Context, conn net.Conn, output io.Writer) (response, error) {
	for {
		kind, content, err := readMessage(conn)
		if err != nil {
			return response{}, c.fault(ctx, err)
		}
		switch kind {
		case msgOutput:
			if output != nil {
				if _, err := output.Write(content); err != nil {
					return response{}, err
				}
			}
		case msgResponse:
			var resp response
			if err := json.Unmarshal(content, &resp); err != nil {
				return response{}, err
			}
			return resp, nil
		default:
			return response{}, errors.New("gate: the root helper sent a message that is not valid")
		}
	}
}

func (c *Client) roundTrip(ctx context.Context, req request) (response, error) {
	conn, stop, err := c.open(ctx, req)
	if err != nil {
		return response{}, err
	}
	defer conn.Close()
	defer stop()
	return c.finish(ctx, conn, nil)
}

func (c *Client) Run(ctx context.Context, name string, args ...string) (string, error) {
	return c.RunEnv(ctx, nil, "", name, args...)
}

func (c *Client) RunInput(ctx context.Context, stdin, name string, args ...string) (string, error) {
	return c.RunEnv(ctx, nil, stdin, name, args...)
}

func (c *Client) RunEnv(ctx context.Context, env []string, stdin, name string, args ...string) (string, error) {
	resp, err := c.roundTrip(ctx, request{Kind: kindRun, Argv: append([]string{name}, args...), Env: env, Stdin: stdin})
	if err != nil {
		return "", err
	}
	return resp.Out, resp.err()
}

func (c *Client) Pipe(ctx context.Context, from, to []string) error {
	resp, err := c.roundTrip(ctx, request{Kind: kindPipe, Argv: from, To: to})
	if err != nil {
		return err
	}
	return resp.err()
}

func (c *Client) Stream(ctx context.Context, stdin io.Reader, stdout io.Writer, name string, args ...string) error {
	req := request{Kind: kindStream, Argv: append([]string{name}, args...), SendsInput: stdin != nil, WantsOutput: stdout != nil}
	conn, stop, err := c.open(ctx, req)
	if err != nil {
		return err
	}
	defer conn.Close()
	defer stop()
	sent := make(chan error, 1)
	if stdin != nil {
		go func() {
			buf := make([]byte, 64<<10)
			for {
				n, err := stdin.Read(buf)
				if n > 0 {
					if werr := writeMessage(conn, msgInput, buf[:n]); werr != nil {
						sent <- nil // The helper ended. Its response has the cause.
						return
					}
				}
				if err == io.EOF {
					sent <- writeMessage(conn, msgInputEnd, nil)
					return
				}
				if err != nil {
					// The helper sees the end of the connection, and stops the command.
					conn.Close()
					sent <- err
					return
				}
			}
		}()
	} else {
		sent <- nil
	}
	resp, err := c.finish(ctx, conn, stdout)
	if err != nil {
		// A read error of the input is the cause of the closed connection.
		select {
		case serr := <-sent:
			if serr != nil {
				return serr
			}
		default:
		}
		return err
	}
	return resp.err()
}

func (c *Client) Call(ctx context.Context, op string, in, out any) error {
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	resp, err := c.roundTrip(ctx, request{Kind: kindCall, Op: op, In: raw})
	if err != nil {
		return err
	}
	if err := resp.err(); err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(resp.Result, out)
}

// Dial opens a connection to a local socket of the list of the helper. After the answer of
// the helper, the connection carries the bytes of that socket.
func (c *Client) Dial(ctx context.Context, name string) (net.Conn, error) {
	conn, stop, err := c.open(ctx, request{Kind: kindDial, Name: name})
	if err != nil {
		return nil, err
	}
	resp, err := c.finish(ctx, conn, nil)
	// From here, ctx does not close the connection: the caller owns it.
	stopped := !stop()
	if err == nil {
		err = resp.err()
	}
	if err == nil && stopped {
		err = ctx.Err()
	}
	if err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}
