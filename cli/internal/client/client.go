// Package client calls the public API of the Cloudfactory panel (openapi/openapi.yaml).
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Client calls the panel API with a token.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// maxAnswer is the largest answer that the client reads.
const maxAnswer = 16 << 20

// New gives a client for a panel, for example "https://panel.example.com".
func New(baseURL, token string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Token: token, HTTP: &http.Client{Timeout: 60 * time.Second, Transport: transport(),
		// Adversarial review, finding C1-07: the API gives no redirect. A redirect could send the
		// token to a different host, or with no encryption.
		CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// transport is the default transport with one change: a name in "localhost", for example
// panel.localhost, is the computer itself (RFC 6761). A browser knows this. The resolver of Go
// asks the DNS for such a name, and on a computer with no local resolver the answer is "no such
// host". Thus the CLI connects to the loopback address for such a name, and uses no proxy.
func transport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, loopbackAddress(addr))
	}
	t.Proxy = func(r *http.Request) (*url.URL, error) {
		if localName(r.URL.Hostname()) {
			return nil, nil
		}
		return http.ProxyFromEnvironment(r)
	}
	return t
}

// localName is true for "localhost" and for each name in "localhost".
func localName(host string) bool {
	name := strings.ToLower(strings.TrimSuffix(host, "."))
	return name == "localhost" || strings.HasSuffix(name, ".localhost")
}

// loopbackAddress changes the host of "panel.localhost:443" to "localhost". /etc/hosts gives
// the IPv4 and the IPv6 loopback address for this name.
func loopbackAddress(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || !localName(host) {
		return addr
	}
	return net.JoinHostPort("localhost", port)
}

// Error is an error answer from the panel.
type Error struct {
	Status  int
	Message string
	Fields  map[string][]string
	// Body is the answer of the panel. Some answers have more fields than the message, for
	// example the DNS record that shows who controls a domain.
	Body []byte
}

func (e *Error) Error() string {
	if len(e.Fields) == 0 {
		return e.Message
	}
	keys := make([]string, 0, len(e.Fields))
	for k := range e.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s: %s; ", k, strings.Join(e.Fields[k], " "))
	}
	return strings.TrimSuffix(b.String(), "; ")
}

// Do sends a request. out can be nil.
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	u := c.BaseURL + "/api/v1" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, r)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer+1))
	if err != nil {
		return err
	}
	if len(data) > maxAnswer {
		return errors.New("the answer of the panel is larger than 16 MiB")
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return fmt.Errorf("the panel answered with a redirect to %s. Sign in with the correct address of the panel", resp.Header.Get("Location"))
	}
	if resp.StatusCode >= 400 {
		e := &Error{Status: resp.StatusCode, Body: data}
		var payload struct {
			Message string              `json:"message"`
			Errors  map[string][]string `json:"errors"`
		}
		if json.Unmarshal(data, &payload) == nil {
			e.Message, e.Fields = payload.Message, payload.Errors
		}
		if e.Message == "" {
			e.Message = fmt.Sprintf("the panel answered with status %d", resp.StatusCode)
		}
		return e
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// Job is a change that an agent applies (spec section 6.3).
type Job struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Action     string   `json:"action"`
	State      string   `json:"state"`
	Steps      []string `json:"steps"`
	Step       int      `json:"step"`
	Error      *string  `json:"error"`
	WebsiteID  *string  `json:"websiteId"`
	CreatedAt  string   `json:"createdAt"`
	FinishedAt *string  `json:"finishedAt"`
	// Result is the summary of a job that has one, for example of an archive.
	Result *JobResult `json:"result,omitempty"`
}

// JobResult is the summary of a job that makes or extracts an archive.
type JobResult struct {
	Files        int      `json:"files"`
	Directories  int      `json:"directories"`
	Links        int      `json:"links"`
	SizeBytes    int64    `json:"sizeBytes"`
	Kept         int      `json:"kept"`
	Skipped      int      `json:"skipped"`
	SkippedNames []string `json:"skippedNames"`
}

// Finished reports whether the job has a result.
func (j Job) Finished() bool { return j.State == "succeeded" || j.State == "failed" }

// Website is a website as the API gives it.
type Website struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Application string `json:"application"`
	Status      string `json:"status"`
	ServerID    string `json:"serverId"`
	UnixUser    string `json:"unixUser"`
	NetworkHost string `json:"networkHost"`
	NetworkPort int    `json:"networkPort"`
	// Kind is "public", "private" or "worker". Command is the start command of a worker or of
	// a Node.js or Python website. Environment has the names of the variables, not the values.
	Kind           string   `json:"kind"`
	Command        string   `json:"command"`
	InstallCommand string   `json:"installCommand"`
	Environment    []string `json:"environment"`
	UpdateReady    bool     `json:"updateReady"`
	Runtime        struct {
		ImageID    string   `json:"imageId"`
		Image      string   `json:"image"`
		PHPVersion *string  `json:"phpVersion"`
		Version    *string  `json:"version"`
		WorkerMode bool     `json:"workerMode"`
		Extensions []string `json:"extensions"`
	} `json:"runtime"`
	Domains []struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
	} `json:"domains"`
	ManagedByTerraform bool               `json:"managedByTerraform"`
	PlanID             string             `json:"planId"`
	Limits             map[string]float64 `json:"limits"`
	Sftp               *Sftp              `json:"sftp"`
}

// Sftp is the SFTP access of a website.
type Sftp struct {
	Enabled        bool     `json:"enabled"`
	Host           *string  `json:"host"`
	Port           int      `json:"port"`
	Username       string   `json:"username"`
	AuthorizedKeys []string `json:"authorizedKeys"`
}

// Job gets a job.
func (c *Client) Job(ctx context.Context, id string) (Job, error) {
	var out struct{ Data Job }
	err := c.Do(ctx, http.MethodGet, "/jobs/"+url.PathEscape(id), nil, nil, &out)
	return out.Data, err
}

// WaitJob gets the job until it is finished. progress is called when the step changes.
func (c *Client) WaitJob(ctx context.Context, id string, every time.Duration, progress func(Job)) (Job, error) {
	last := -1
	failures := 0
	var job Job
	for {
		next, err := c.Job(ctx, id)
		if err != nil {
			// Adversarial review, finding C1-13: one failed request does not end the wait. The
			// job continues in the panel. An answer such as 401 or 404 ends it at once.
			var apiErr *Error
			final := errors.As(err, &apiErr) && apiErr.Status < 500 && apiErr.Status != 429
			if failures++; final || failures > 5 || ctx.Err() != nil {
				job.ID = id
				return job, fmt.Errorf("%w. The job continues in the panel: cf jobs show %s", err, id)
			}
			select {
			case <-ctx.Done():
				return job, ctx.Err()
			case <-time.After(every):
			}
			continue
		}
		failures = 0
		job = next
		if job.Step != last && progress != nil {
			progress(job)
			last = job.Step
		}
		if job.Finished() {
			if job.State == "failed" {
				msg := "the job failed"
				if job.Error != nil {
					msg = *job.Error
				}
				return job, errors.New(msg)
			}
			return job, nil
		}
		select {
		case <-ctx.Done():
			return job, ctx.Err()
		case <-time.After(every):
		}
	}
}

// All gets each page of a list and gives all rows. It follows links.next, so that no row is
// missing (audit batch 3, issue 4). A list with no pages gives its rows at once.
func All[T any](ctx context.Context, c *Client, path string, query url.Values) ([]T, error) {
	rows, _, err := Pages[T](ctx, c, path, query, 0)
	return rows, err
}

// Pages gets the pages of a list until it has limit rows. A limit of 0 gets all rows. It also
// gives the number of rows in the full list.
func Pages[T any](ctx context.Context, c *Client, path string, query url.Values, limit int) ([]T, int, error) {
	q := url.Values{}
	for k, v := range query {
		q[k] = v
	}
	if q.Get("perPage") == "" {
		q.Set("perPage", "100")
	}
	var rows []T
	for page := 1; ; page++ {
		q.Set("page", strconv.Itoa(page))
		var out struct {
			Data  []T `json:"data"`
			Links *struct {
				Next *string `json:"next"`
			} `json:"links"`
			Meta *struct {
				LastPage int `json:"last_page"`
				// The audit log and the notifications give this name.
				LastPageCamel int `json:"lastPage"`
				Total         int `json:"total"`
			} `json:"meta"`
		}
		if err := c.Do(ctx, http.MethodGet, path, q, nil, &out); err != nil {
			return nil, 0, err
		}
		rows = append(rows, out.Data...)
		total := len(rows)
		if out.Meta != nil && out.Meta.Total > total {
			total = out.Meta.Total
		}
		more := out.Links != nil && out.Links.Next != nil && *out.Links.Next != ""
		if out.Links == nil && out.Meta != nil {
			more = page < max(out.Meta.LastPage, out.Meta.LastPageCamel)
		}
		if limit > 0 && len(rows) >= limit {
			return rows[:limit], total, nil
		}
		if !more || len(out.Data) == 0 {
			return rows, total, nil
		}
	}
}

// FindWebsite gets a website by its ID or by its name.
func (c *Client) FindWebsite(ctx context.Context, nameOrID string) (Website, error) {
	var one struct{ Data Website }
	if !strings.Contains(nameOrID, ".") {
		err := c.Do(ctx, http.MethodGet, "/websites/"+url.PathEscape(nameOrID), nil, nil, &one)
		return one.Data, err
	}
	// The search can match more websites, for example shop.acme.example for acme.example.
	list, err := All[Website](ctx, c, "/websites", url.Values{"q": {nameOrID}})
	if err != nil {
		return Website{}, err
	}
	for _, w := range list {
		if w.Name == nameOrID {
			return w, nil
		}
	}
	return Website{}, fmt.Errorf("there is no website %s", nameOrID)
}
