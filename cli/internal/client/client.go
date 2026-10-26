// Package client calls the public API of the Cloudfactory panel (openapi/openapi.yaml).
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Client calls the panel API with a token.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// New gives a client for a panel, for example "https://panel.example.com".
func New(baseURL, token string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Token: token, HTTP: &http.Client{Timeout: 60 * time.Second}}
}

// Error is an error answer from the panel.
type Error struct {
	Status  int
	Message string
	Fields  map[string][]string
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
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		e := &Error{Status: resp.StatusCode}
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
	UpdateReady bool   `json:"updateReady"`
	Runtime     struct {
		ImageID    string   `json:"imageId"`
		Image      string   `json:"image"`
		PHPVersion *string  `json:"phpVersion"`
		WorkerMode bool     `json:"workerMode"`
		Extensions []string `json:"extensions"`
	} `json:"runtime"`
	Domains []struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
	} `json:"domains"`
	ManagedByTerraform bool `json:"managedByTerraform"`
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
	for {
		job, err := c.Job(ctx, id)
		if err != nil {
			return job, err
		}
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

// FindWebsite gets a website by its ID or by its name.
func (c *Client) FindWebsite(ctx context.Context, nameOrID string) (Website, error) {
	var one struct{ Data Website }
	if !strings.Contains(nameOrID, ".") {
		err := c.Do(ctx, http.MethodGet, "/websites/"+url.PathEscape(nameOrID), nil, nil, &one)
		return one.Data, err
	}
	var list struct{ Data []Website }
	if err := c.Do(ctx, http.MethodGet, "/websites", url.Values{"q": {nameOrID}, "perPage": {"100"}}, nil, &list); err != nil {
		return Website{}, err
	}
	for _, w := range list.Data {
		if w.Name == nameOrID {
			return w, nil
		}
	}
	return Website{}, fmt.Errorf("there is no website %s", nameOrID)
}
