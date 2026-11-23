package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/itbm/cloudfactory/cli/internal/client"
)

// uptimeCheck is a check as the API gives it (UptimeController::present).
type uptimeCheck struct {
	ID                  string        `json:"id"`
	WebsiteName         string        `json:"websiteName"`
	URL                 string        `json:"url"`
	IntervalSeconds     int           `json:"intervalSeconds"`
	TimeoutSeconds      int           `json:"timeoutSeconds"`
	ExpectedStatus      *int          `json:"expectedStatus"`
	Keyword             *string       `json:"keyword"`
	FailuresBeforeAlert int           `json:"failuresBeforeAlert"`
	Enabled             bool          `json:"enabled"`
	State               string        `json:"state"`
	PausedReason        *string       `json:"pausedReason"`
	Since               *string       `json:"since"`
	LastCheckedAt       *string       `json:"lastCheckedAt"`
	LastResult          *uptimeResult `json:"lastResult"`
	NextCheckAt         *string       `json:"nextCheckAt"`
	Uptime              struct {
		Day   *float64 `json:"day"`
		Week  *float64 `json:"week"`
		Month *float64 `json:"month"`
	} `json:"uptime"`
	Incidents []uptimeIncident `json:"incidents"`
}

type uptimeResult struct {
	OK         bool    `json:"ok"`
	Status     *int    `json:"status"`
	ResponseMs *int    `json:"responseMs"`
	Error      *string `json:"error"`
}

type uptimeIncident struct {
	StartedAt       string  `json:"startedAt"`
	ResolvedAt      *string `json:"resolvedAt"`
	DurationSeconds int     `json:"durationSeconds"`
	Error           string  `json:"error"`
	EndedByPause    bool    `json:"endedByPause"`
	WebsiteName     string  `json:"websiteName"`
	URL             string  `json:"url"`
}

// percent shows a percent of uptime, or "-" when the check has no result yet.
func percent(v *float64) string {
	if v == nil {
		return "-"
	}
	return strconv.FormatFloat(*v, 'f', -1, 64) + "%"
}

// text shows the result of a request: "200 in 214 ms", or the error.
func (r *uptimeResult) text() string {
	if r == nil {
		return "-"
	}
	if !r.OK && r.Error != nil {
		return *r.Error
	}
	s := ""
	if r.Status != nil {
		s = strconv.Itoa(*r.Status)
	}
	if r.ResponseMs != nil {
		s += fmt.Sprintf(" in %d ms", *r.ResponseMs)
	}
	return strings.TrimSpace(s)
}

// expects shows the answer that a check expects.
func (c uptimeCheck) expects() string {
	s := "status 200 to 399"
	if c.ExpectedStatus != nil {
		s = "status " + strconv.Itoa(*c.ExpectedStatus)
	}
	if c.Keyword != nil {
		s += fmt.Sprintf(" and the text %q", *c.Keyword)
	}
	return s
}

// lasting shows a length of time, for example "1h5m" (Monitor::duration gives the words).
func lasting(seconds int) string {
	if seconds < 60 {
		return "<1m"
	}
	m := seconds / 60
	switch {
	case m >= 1440:
		return fmt.Sprintf("%dd%dh", m/1440, m%1440/60)
	case m >= 60:
		return fmt.Sprintf("%dh%dm", m/60, m%60)
	}
	return fmt.Sprintf("%dm", m)
}

// uptimeFlags are the settings of a check, for "add" and for "set".
type uptimeFlags struct {
	fs                                *flag.FlagSet
	domain, path, text                *string
	http, https, noText, anyStatus    *bool
	on, off                           *bool
	interval, timeout, status, failed *int
}

func newUptimeFlags(name string, set bool) *uptimeFlags {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	f := &uptimeFlags{fs: fs}
	f.domain = fs.String("domain", "", "a domain of the website (default: the primary domain)")
	f.path = fs.String("path", "", `the path and the query, for example "/health" (default: "/")`)
	f.http = fs.Bool("http", false, "use HTTP, not HTTPS")
	f.interval = fs.Int("interval", 0, "the seconds between two requests: 60, 120, 300, 600, 900, 1800 or 3600 (default: 300)")
	f.timeout = fs.Int("timeout", 0, "the seconds to wait for the answer, from 2 to 30 (default: 10)")
	f.status = fs.Int("status", 0, "the HTTP status that the check expects (default: each status from 200 to 399)")
	f.text = fs.String("text", "", "a text that the first 1 MiB of the answer must have")
	f.failed = fs.Int("failures", 0, "the failed requests in a row before the email, from 1 to 5 (default: 2)")
	f.off = fs.Bool("off", false, "turn the check off")
	if set {
		f.https = fs.Bool("https", false, "use HTTPS")
		f.anyStatus = fs.Bool("any-status", false, "accept each status from 200 to 399")
		f.noText = fs.Bool("no-text", false, "do not look for a text")
		f.on = fs.Bool("on", false, "turn the check on")
	}
	return f
}

// body gives the fields that the flags set.
func (f *uptimeFlags) body(set bool) (map[string]any, error) {
	b := map[string]any{}
	given := map[string]bool{}
	f.fs.Visit(func(fl *flag.Flag) { given[fl.Name] = true })
	if *f.domain != "" {
		b["domain"] = *f.domain
	}
	if *f.path != "" {
		b["path"] = *f.path
	}
	if *f.http {
		b["scheme"] = "http"
	}
	if set && *f.https {
		if *f.http {
			return nil, errors.New("give --http or --https, not both")
		}
		b["scheme"] = "https"
	}
	for name, v := range map[string]*int{"intervalSeconds": f.interval, "timeoutSeconds": f.timeout, "failuresBeforeAlert": f.failed} {
		if *v != 0 {
			b[name] = *v
		}
	}
	if given["status"] {
		b["expectedStatus"] = *f.status
	}
	if set && *f.anyStatus {
		if given["status"] {
			return nil, errors.New("give --status or --any-status, not both")
		}
		b["expectedStatus"] = nil
	}
	if given["text"] {
		b["keyword"] = *f.text
	}
	if set && *f.noText {
		if given["text"] {
			return nil, errors.New("give --text or --no-text, not both")
		}
		b["keyword"] = nil
	}
	if *f.off {
		b["enabled"] = false
	}
	if set && *f.on {
		if *f.off {
			return nil, errors.New("give --on or --off, not both")
		}
		b["enabled"] = true
	}
	return b, nil
}

// uptime runs "cf uptime-checks". The panel requests an address of a website on a schedule,
// and sends an email when the address is down and when it is up again.
func (a *app) uptime(ctx context.Context, sub string, args []string) error {
	if sub == "list" || sub == "ls" {
		return a.uptimeList(ctx, args)
	}
	if sub == "incidents" {
		return a.uptimeIncidents(ctx, args)
	}
	if err := need(args, 1, "a website"); err != nil {
		return err
	}
	w, err := a.api.FindWebsite(ctx, args[0])
	if err != nil {
		return err
	}
	base := "/websites/" + w.ID + "/uptime-checks"
	check := func() (string, error) {
		if err := need(args, 2, "a website and the ID of an uptime check (cf uptime-checks list WEBSITE)"); err != nil {
			return "", err
		}
		return base + "/" + url.PathEscape(args[1]), nil
	}
	switch sub {
	case "get", "show":
		path, err := check()
		if err != nil {
			return err
		}
		return a.uptimeShow(ctx, path)
	case "add", "create":
		f := newUptimeFlags("add", false)
		if err := f.fs.Parse(args[1:]); err != nil {
			return err
		}
		body, err := f.body(false)
		if err != nil {
			return err
		}
		if body["domain"] == nil {
			for _, d := range w.Domains {
				if d.Kind == "primary" {
					body["domain"] = d.Name
				}
			}
			if body["domain"] == nil {
				return errors.New("the website has no domain; give --domain")
			}
		}
		var out struct {
			UptimeCheck uptimeCheck `json:"uptimeCheck"`
		}
		if err := a.api.Do(ctx, http.MethodPost, base, nil, body, &out); err != nil {
			return err
		}
		if a.json {
			return a.printJSON(out)
		}
		fmt.Fprintf(a.out, "Added the uptime check %s of %s. The first request is within 1 minute.\n", out.UptimeCheck.ID, out.UptimeCheck.URL)
		return nil
	case "set":
		path, err := check()
		if err != nil {
			return err
		}
		f := newUptimeFlags("set", true)
		if err := f.fs.Parse(args[2:]); err != nil {
			return err
		}
		body, err := f.body(true)
		if err != nil {
			return err
		}
		if len(body) == 0 {
			return errors.New("give a setting to change; see cf help")
		}
		return a.change(ctx, http.MethodPatch, path, body)
	case "run":
		path, err := check()
		if err != nil {
			return err
		}
		var out struct {
			UptimeCheck uptimeCheck  `json:"uptimeCheck"`
			Result      uptimeResult `json:"result"`
		}
		if err := a.api.Do(ctx, http.MethodPost, path+"/run", nil, nil, &out); err != nil {
			return err
		}
		if a.json {
			return a.printJSON(out)
		}
		if out.Result.OK {
			fmt.Fprintf(a.out, "%s: up, %s. The check is %s.\n", out.UptimeCheck.URL, out.Result.text(), out.UptimeCheck.State)
			return nil
		}
		fmt.Fprintf(a.out, "%s: the request failed: %s The check is %s.\n", out.UptimeCheck.URL, out.Result.text(), out.UptimeCheck.State)
		return &exitError{code: 1, text: "the uptime check failed"}
	case "remove", "delete", "rm":
		path, err := check()
		if err != nil {
			return err
		}
		return a.change(ctx, http.MethodDelete, path, nil)
	}
	return fmt.Errorf("unknown command uptime-checks %s", sub)
}

// uptimeList prints the checks of one website, or of all websites with the checks that are down first.
func (a *app) uptimeList(ctx context.Context, args []string) error {
	website := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		website, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	state := fs.String("state", "", "up, down, pending or paused")
	if err := fs.Parse(args); err != nil {
		return err
	}
	path, q := "/uptime-checks", url.Values{}
	if *state != "" {
		q.Set("state", *state)
	}
	if website != "" {
		w, err := a.api.FindWebsite(ctx, website)
		if err != nil {
			return err
		}
		q.Set("websiteId", w.ID)
	}
	rows, _, err := client.Pages[json.RawMessage](ctx, a.api, path, q, 0)
	if err != nil {
		return err
	}
	if a.json {
		return a.printJSON(rows)
	}
	table := make([]map[string]any, 0, len(rows))
	for _, raw := range rows {
		var c uptimeCheck
		json.Unmarshal(raw, &c)
		table = append(table, map[string]any{
			"id": c.ID, "website": c.WebsiteName, "url": c.URL, "state": c.State, "last": c.LastResult.text(),
			"24h": percent(c.Uptime.Day), "30d": percent(c.Uptime.Month),
		})
	}
	if len(table) == 0 {
		fmt.Fprintln(a.out, "No uptime checks. Add one: cf uptime-checks add WEBSITE")
		return nil
	}
	a.table(table, []string{"id", "website", "url", "state", "last", "24h", "30d"})
	return nil
}

// uptimeShow prints one check with its incidents.
func (a *app) uptimeShow(ctx context.Context, path string) error {
	var raw json.RawMessage
	if err := a.api.Do(ctx, http.MethodGet, path, nil, nil, &raw); err != nil {
		return err
	}
	if a.json {
		return a.printJSON(raw)
	}
	var c uptimeCheck
	json.Unmarshal(raw, &c)
	state := c.State
	if c.PausedReason != nil {
		state += ": " + *c.PausedReason
	} else if c.Since != nil {
		state += " since " + *c.Since
	}
	fmt.Fprintf(a.out, "URL:         %s\nState:       %s\nExpects:     %s\n", c.URL, state, c.expects())
	fmt.Fprintf(a.out, "Interval:    %d seconds, timeout %d seconds, email after %d failed requests\n", c.IntervalSeconds, c.TimeoutSeconds, c.FailuresBeforeAlert)
	if c.LastCheckedAt != nil {
		fmt.Fprintf(a.out, "Last check:  %s: %s\n", *c.LastCheckedAt, c.LastResult.text())
	}
	if c.NextCheckAt != nil {
		fmt.Fprintf(a.out, "Next check:  %s\n", *c.NextCheckAt)
	}
	fmt.Fprintf(a.out, "Uptime:      %s in 24 hours, %s in 7 days, %s in 30 days\n", percent(c.Uptime.Day), percent(c.Uptime.Week), percent(c.Uptime.Month))
	if len(c.Incidents) == 0 {
		fmt.Fprintln(a.out, "\nNo incidents.")
		return nil
	}
	fmt.Fprintln(a.out)
	a.table(incidentRows(c.Incidents, false), []string{"startedAt", "length", "error"})
	return nil
}

// uptimeIncidents prints the incidents of the checks that the user can see, the open incidents first.
func (a *app) uptimeIncidents(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("incidents", flag.ContinueOnError)
	website := fs.String("website", "", "only the incidents of this website")
	open := fs.Bool("open", false, "only the incidents that are not closed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	q := url.Values{}
	if *open {
		q.Set("state", "open")
	}
	if *website != "" {
		w, err := a.api.FindWebsite(ctx, *website)
		if err != nil {
			return err
		}
		q.Set("websiteId", w.ID)
	}
	var out struct {
		Data []uptimeIncident `json:"data"`
	}
	if err := a.api.Do(ctx, http.MethodGet, "/uptime-incidents", q, nil, &out); err != nil {
		return err
	}
	if a.json {
		return a.printJSON(out.Data)
	}
	if len(out.Data) == 0 {
		fmt.Fprintln(a.out, "No incidents.")
		return nil
	}
	a.table(incidentRows(out.Data, true), []string{"website", "url", "startedAt", "length", "error"})
	return nil
}

func incidentRows(incidents []uptimeIncident, all bool) []map[string]any {
	rows := make([]map[string]any, 0, len(incidents))
	for _, i := range incidents {
		length := lasting(i.DurationSeconds)
		if i.ResolvedAt == nil {
			length = "down for " + length
		} else if i.EndedByPause {
			length += " (paused)"
		}
		row := map[string]any{"startedAt": i.StartedAt, "length": length, "error": i.Error}
		if all {
			row["website"], row["url"] = i.WebsiteName, i.URL
		}
		rows = append(rows, row)
	}
	return rows
}
