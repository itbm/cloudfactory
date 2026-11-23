package main

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// The file manager, the logs, the cron jobs and the deploys of a website.

// maxFileBytes is the largest file that the file manager of the panel writes.
const maxFileBytes = 5 << 20

// sitePath gives a path of the file manager: it starts with "/", which is the directory of
// the website.
func sitePath(p string) string {
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
	}
	return p
}

type fileEntry struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Type      string `json:"type"`
	SizeBytes int64  `json:"sizeBytes"`
	Modified  string `json:"modifiedAt"`
	Mode      string `json:"mode"`
	Target    string `json:"target"`
}

func (a *app) files(ctx context.Context, sub string, args []string) error {
	if err := need(args, 1, "a website"); err != nil {
		return err
	}
	w, err := a.api.FindWebsite(ctx, args[0])
	if err != nil {
		return err
	}
	base := "/websites/" + w.ID + "/files"
	path := "/"
	if len(args) > 1 && !strings.HasPrefix(args[1], "--") {
		path = sitePath(args[1])
	} else if sub != "list" && sub != "ls" {
		return errors.New("give a website and a path, for example /public/index.php")
	}
	done := func(out map[string]any, text string) error {
		if a.json {
			return a.printJSON(out)
		}
		fmt.Fprintln(a.out, text)
		return nil
	}
	switch sub {
	case "list", "ls":
		var out struct {
			Entries   []map[string]any `json:"entries"`
			Truncated bool             `json:"truncated"`
		}
		if err := a.api.Do(ctx, http.MethodGet, base, url.Values{"path": {path}}, nil, &out); err != nil {
			return err
		}
		if a.json {
			return a.printJSON(out)
		}
		a.table(out.Entries, []string{"type", "mode", "sizeBytes", "modifiedAt", "name"})
		if out.Truncated {
			fmt.Fprintln(a.out, "The directory has more entries. Use SFTP to see all.")
		}
		return nil
	case "get", "cat":
		fs := flag.NewFlagSet("get", flag.ContinueOnError)
		output := fs.String("output", "", "write the content to this file")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		var out struct {
			File     fileEntry `json:"file"`
			Encoding string    `json:"encoding"`
			Content  string    `json:"content"`
		}
		if err := a.api.Do(ctx, http.MethodGet, base+"/content", url.Values{"path": {path}}, nil, &out); err != nil {
			return err
		}
		if a.json {
			return a.printJSON(out)
		}
		content := []byte(out.Content)
		if out.Encoding == "base64" {
			if content, err = base64.StdEncoding.DecodeString(out.Content); err != nil {
				return err
			}
			if *output == "" {
				return errors.New("the file is not text. Add --output FILE to save it")
			}
		}
		if *output == "" {
			_, err = a.raw.Write(content)
			return err
		}
		if err := os.WriteFile(*output, content, 0o600); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "Saved %s (%d bytes).\n", *output, len(content))
		return nil
	case "put":
		fs := flag.NewFlagSet("put", flag.ContinueOnError)
		from := fs.String("from", "", `the file on this computer, or "-" for the standard input`)
		createOnly := fs.Bool("create-only", false, "do not replace a file")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		var content []byte
		switch *from {
		case "":
			return errors.New("give --from FILE")
		case "-":
			in := a.in
			if in == nil {
				in = os.Stdin
			}
			content, err = io.ReadAll(io.LimitReader(in, maxFileBytes+1))
		default:
			content, err = readLimited(*from, maxFileBytes)
		}
		if err != nil {
			return err
		}
		if len(content) > maxFileBytes {
			return errors.New("the file has more than 5 MB. Use SFTP to upload it")
		}
		var out map[string]any
		body := map[string]any{"path": path, "content": base64.StdEncoding.EncodeToString(content), "encoding": "base64", "createOnly": *createOnly}
		if err := a.api.Do(ctx, http.MethodPut, base+"/content", nil, body, &out); err != nil {
			return err
		}
		return done(out, fmt.Sprintf("Saved %s (%d bytes).", path, len(content)))
	case "mkdir":
		var out map[string]any
		if err := a.api.Do(ctx, http.MethodPost, base+"/directories", nil, map[string]any{"path": path}, &out); err != nil {
			return err
		}
		return done(out, "Made "+path+".")
	case "move", "mv":
		if err := need(args, 3, "a website, a path and the new path"); err != nil {
			return err
		}
		to := sitePath(args[2])
		var out map[string]any
		if err := a.api.Do(ctx, http.MethodPost, base+"/move", nil, map[string]any{"from": path, "to": to}, &out); err != nil {
			return err
		}
		return done(out, "Moved "+path+" to "+to+".")
	case "delete", "rm":
		if err := a.api.Do(ctx, http.MethodDelete, base, url.Values{"path": {path}}, nil, nil); err != nil {
			return err
		}
		return done(map[string]any{"deleted": path}, "Deleted "+path+".")
	}
	return fmt.Errorf("unknown command files %s", sub)
}

// logs searches the logs of a website: "cf logs WEBSITE".
func (a *app) logs(ctx context.Context, website string, args []string) error {
	w, err := a.api.FindWebsite(ctx, website)
	if err != nil {
		return err
	}
	return a.searchLogs(ctx, "/websites/"+w.ID+"/logs", true, args)
}

// The limits of a log search: the API gives at most logPage entries in one answer, and the
// command reads at most maxLogEntries entries.
const (
	logPage       = 1000
	maxLogEntries = 10000
)

// logEntry is an entry of a log search: a request of the access log, or a line of a container.
type logEntry struct {
	At     string `json:"at"`
	Source string `json:"source"`
	Text   string `json:"text"`
	Access *struct {
		Client     string  `json:"client"`
		Method     string  `json:"method"`
		Host       string  `json:"host"`
		URI        string  `json:"uri"`
		Proto      string  `json:"proto"`
		Status     int     `json:"status"`
		SizeBytes  int64   `json:"sizeBytes"`
		DurationMs float64 `json:"durationMs"`
		UserAgent  string  `json:"userAgent"`
		Referer    string  `json:"referer"`
	} `json:"access"`
}

type logCount struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
}

type logSummary struct {
	Total      int64            `json:"total"`
	Sources    map[string]int64 `json:"sources"`
	Statuses   map[string]int64 `json:"statuses"`
	TopPaths   []logCount       `json:"topPaths"`
	TopClients []logCount       `json:"topClients"`
	SizeBytes  int64            `json:"sizeBytes"`
	Oldest     *string          `json:"oldest"`
}

type logResult struct {
	Entries []logEntry  `json:"entries"`
	Next    *string     `json:"next"`
	Summary *logSummary `json:"summary"`
}

// searchLogs searches the logs of a website, or the access log of a server (no sources). It
// reads the pages of the API until it has the number of entries, and shows the oldest entry
// first, as a log file does.
func (a *app) searchLogs(ctx context.Context, path string, sources bool, args []string) error {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	source := new(string)
	if sources {
		source = fs.String("source", "", "access, output or error, with commas between them")
	}
	search := fs.String("search", "", `the words that each entry must have. "-word" is a word that the entry must not have`)
	status := fs.String("status", "", "status codes or classes, for example 404,5xx")
	method := fs.String("method", "", "request methods, for example GET,POST")
	clientIP := fs.String("client", "", "an IP address or a network, for example 203.0.113.0/24")
	since := fs.String("since", "", "a duration such as 15m, 2h or 7d, or a time such as 2026-11-14T09:00:00Z")
	until := fs.String("until", "", "a time such as 2026-11-14T10:00:00Z")
	limit := fs.Int("limit", 100, fmt.Sprintf("the number of entries, at most %d", maxLogEntries))
	summary := fs.Bool("summary", false, "also count all the entries that agree with the search")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q: give the words of the search with --search", fs.Arg(0))
	}
	if *limit < 1 || *limit > maxLogEntries {
		return fmt.Errorf("--limit must be from 1 to %d", maxLogEntries)
	}
	q := url.Values{}
	for key, value := range map[string]string{"sources": *source, "q": *search, "status": *status, "method": *method, "client": *clientIP} {
		if v := strings.TrimSpace(value); v != "" {
			q.Set(key, v)
		}
	}
	for key, value := range map[string]string{"from": *since, "to": *until} {
		if value == "" {
			continue
		}
		t, err := logTime(value, time.Now())
		if err != nil {
			return fmt.Errorf("--%s: %w", map[string]string{"from": "since", "to": "until"}[key], err)
		}
		q.Set(key, t.UTC().Format(time.RFC3339Nano))
	}
	var all logResult
	for {
		q.Set("limit", strconv.Itoa(min(*limit-len(all.Entries), logPage)))
		if *summary && all.Next == nil {
			q.Set("summary", "1")
		} else {
			q.Del("summary")
		}
		var page logResult
		if err := a.api.Do(ctx, http.MethodGet, path, q, nil, &page); err != nil {
			return err
		}
		all.Entries = append(all.Entries, page.Entries...)
		all.Next = page.Next
		if page.Summary != nil {
			all.Summary = page.Summary
		}
		if all.Next == nil || len(all.Entries) >= *limit {
			break
		}
		q.Set("cursor", *all.Next)
	}
	if a.json {
		return a.printJSON(all)
	}
	if s := all.Summary; s != nil {
		a.printLogSummary(s)
	}
	for i := len(all.Entries) - 1; i >= 0; i-- {
		fmt.Fprintln(a.out, logLine(all.Entries[i]))
	}
	if all.Next != nil {
		fmt.Fprintf(a.out, "There are older entries. Use --limit with a number larger than %d, or --until %s.\n", len(all.Entries), all.Entries[len(all.Entries)-1].At)
	}
	return nil
}

// logTime reads a time, or a duration before now such as 15m, 2h or 7d.
func logTime(s string, now time.Time) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if days, ok := strings.CutSuffix(s, "d"); ok {
		if n, err := strconv.Atoi(days); err == nil && n > 0 {
			return now.AddDate(0, 0, -n), nil
		}
	} else if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return now.Add(-d), nil
	}
	return time.Time{}, fmt.Errorf("give a duration such as 15m, 2h or 7d, or a time such as 2026-11-14T09:00:00Z, not %q", s)
}

// logLine gives one entry as one line of text.
func logLine(e logEntry) string {
	a := e.Access
	if a == nil {
		return fmt.Sprintf("%s %-6s %s", e.At, e.Source, e.Text)
	}
	return fmt.Sprintf("%s %-6s %d %s %s%s %s %s %.1fms %q %q", e.At, e.Source, a.Status, a.Method, a.Host, a.URI, a.Client, logBytes(a.SizeBytes), a.DurationMs, a.Referer, a.UserAgent)
}

func (a *app) printLogSummary(s *logSummary) {
	fmt.Fprintf(a.out, "Requests: %d (2xx %d, 3xx %d, 4xx %d, 5xx %d), %s sent\n", s.Sources["access"], s.Statuses["2xx"], s.Statuses["3xx"], s.Statuses["4xx"], s.Statuses["5xx"], logBytes(s.SizeBytes))
	fmt.Fprintf(a.out, "Output lines: %d. Error lines: %d.\n", s.Sources["output"], s.Sources["error"])
	counts := func(title string, list []logCount) {
		if len(list) == 0 {
			return
		}
		parts := make([]string, len(list))
		for i, c := range list {
			parts[i] = fmt.Sprintf("%s (%d)", c.Value, c.Count)
		}
		fmt.Fprintf(a.out, "%s: %s\n", title, strings.Join(parts, ", "))
	}
	counts("Top paths", s.TopPaths)
	counts("Top clients", s.TopClients)
	if s.Oldest != nil {
		fmt.Fprintf(a.out, "The oldest entry is from %s.\n", *s.Oldest)
	}
	fmt.Fprintln(a.out)
}

// logBytes gives a size with a binary unit, for example "21.8MB".
func logBytes(n int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	v, i := float64(n), 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%dB", n)
	}
	return fmt.Sprintf("%.1f%s", v, units[i])
}

func (a *app) cron(ctx context.Context, sub string, args []string) error {
	if err := need(args, 1, "a website"); err != nil {
		return err
	}
	w, err := a.api.FindWebsite(ctx, args[0])
	if err != nil {
		return err
	}
	base := "/websites/" + w.ID + "/cron-jobs"
	job := func() (string, error) {
		if err := need(args, 2, "a website and the ID of a cron job (cf cron list WEBSITE)"); err != nil {
			return "", err
		}
		return base + "/" + url.PathEscape(args[1]), nil
	}
	switch sub {
	case "list", "ls":
		return a.list(ctx, base, nil, []string{"id", "schedule", "enabled", "lastRunAt", "lastResult", "command"})
	case "add", "create":
		fs := flag.NewFlagSet("add", flag.ContinueOnError)
		schedule := fs.String("schedule", "", `the five fields of cron, for example "*/5 * * * *". The time is UTC`)
		command := fs.String("command", "", "the command, one line")
		off := fs.Bool("off", false, "do not run the cron job yet")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *schedule == "" || *command == "" {
			return errors.New("give --schedule and --command")
		}
		return a.change(ctx, http.MethodPost, base, map[string]any{"schedule": *schedule, "command": *command, "enabled": !*off})
	case "set":
		path, err := job()
		if err != nil {
			return err
		}
		fs := flag.NewFlagSet("set", flag.ContinueOnError)
		schedule := fs.String("schedule", "", "the new schedule")
		command := fs.String("command", "", "the new command")
		on := fs.Bool("on", false, "turn the cron job on")
		off := fs.Bool("off", false, "turn the cron job off")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		body := map[string]any{}
		if *schedule != "" {
			body["schedule"] = *schedule
		}
		if *command != "" {
			body["command"] = *command
		}
		if *on != *off {
			body["enabled"] = *on
		} else if *on {
			return errors.New("give --on or --off, not both")
		}
		if len(body) == 0 {
			return errors.New("give --schedule, --command, --on or --off")
		}
		return a.change(ctx, http.MethodPatch, path, body)
	case "run":
		path, err := job()
		if err != nil {
			return err
		}
		return a.change(ctx, http.MethodPost, path+"/run", nil)
	case "delete", "rm":
		path, err := job()
		if err != nil {
			return err
		}
		return a.change(ctx, http.MethodDelete, path, nil)
	}
	return fmt.Errorf("unknown command cron %s", sub)
}

type deployState struct {
	Source *struct {
		Repository       string   `json:"repository"`
		Branch           string   `json:"branch"`
		WebDirectory     string   `json:"webDirectory"`
		Trigger          string   `json:"trigger"`
		CheckMinutes     int      `json:"checkMinutes"`
		CheckedAt        string   `json:"checkedAt"`
		CheckError       string   `json:"checkError"`
		BuildCommands    []string `json:"buildCommands"`
		MigrationCommand string   `json:"migrationCommand"`
		DeployKey        string   `json:"deployKey"`
		WebhookURL       string   `json:"webhookUrl"`
		WebhookSecret    string   `json:"webhookSecret"`
		DeployQueued     bool     `json:"deployQueued"`
		Secrets          []string `json:"secrets"`
	} `json:"source"`
	Releases []map[string]any `json:"releases"`
}

func (a *app) printDeploy(d deployState) error {
	if a.json {
		return a.printJSON(d)
	}
	if d.Source == nil {
		fmt.Fprintln(a.out, "The website has no Git repository. Connect one: cf deploys connect WEBSITE --repository URL --branch BRANCH")
		return nil
	}
	s := d.Source
	fmt.Fprintf(a.out, "Repository:     %s\nBranch:         %s\nWeb directory:  %s\nTrigger:        %s\n", s.Repository, s.Branch, s.WebDirectory, s.Trigger)
	switch s.Trigger {
	case "schedule":
		fmt.Fprintf(a.out, "Check:          each %d min, last %s\n", s.CheckMinutes, cell(s.CheckedAt))
		if s.CheckError != "" {
			fmt.Fprintf(a.out, "Check error:    %s\n", s.CheckError)
		}
	case "webhook":
		if s.DeployQueued {
			fmt.Fprintln(a.out, "Queued:         a push came during the deploy. The panel deploys it next.")
		}
	}
	for i, c := range s.BuildCommands {
		fmt.Fprintf(a.out, "Build %d:        %s\n", i+1, c)
	}
	fmt.Fprintf(a.out, "Migration:      %s\nSecrets:        %s\nDeploy key:     %s\n", cell(s.MigrationCommand), cell(strings.Join(s.Secrets, ", ")), s.DeployKey)
	if s.Trigger == "webhook" {
		fmt.Fprintf(a.out, "Webhook:        %s\nWebhook secret: %s\n", cell(s.WebhookURL), cell(s.WebhookSecret))
	}
	if len(d.Releases) > 0 {
		fmt.Fprintln(a.out)
		for _, r := range d.Releases {
			if c, ok := r["commit"].(string); ok && len(c) > 7 {
				r["commit"] = c[:7]
			}
			// The health check failed after a migration, and the website kept the release.
			if h, ok := r["healthError"].(string); ok && h != "" {
				r["state"] = fmt.Sprint(r["state"]) + ", check failed"
			}
		}
		a.table(d.Releases, []string{"id", "createdAt", "state", "live", "trigger", "migration", "commit", "message"})
	}
	return nil
}

func (a *app) deploys(ctx context.Context, sub string, args []string) error {
	// "cf deploys secret set|remove WEBSITE NAME".
	action := ""
	if sub == "secret" || sub == "secrets" {
		if err := need(args, 1, "set or remove"); err != nil {
			return err
		}
		action, args = args[0], args[1:]
	}
	if err := need(args, 1, "a website"); err != nil {
		return err
	}
	w, err := a.api.FindWebsite(ctx, args[0])
	if err != nil {
		return err
	}
	base := "/websites/" + w.ID + "/deploy"
	var state deployState
	switch sub {
	case "show", "get", "list", "ls":
		if err := a.api.Do(ctx, http.MethodGet, base, nil, nil, &state); err != nil {
			return err
		}
		return a.printDeploy(state)
	case "connect", "set":
		fs := flag.NewFlagSet("connect", flag.ContinueOnError)
		repository := fs.String("repository", "", "the address of the Git repository, for example git@github.com:acme/shop.git")
		branch := fs.String("branch", "", "the branch")
		webDirectory := fs.String("web-directory", "", `the directory that the web server serves, for example public, or "." for the repository`)
		trigger := fs.String("trigger", "", "manual, webhook or schedule")
		checkMinutes := fs.Int("check-minutes", 0, "for the trigger schedule: the minutes between two checks of the branch (1 to 1440)")
		migration := fs.String("migration", "", "a command that changes the database, for example php artisan migrate --force")
		noMigration := fs.Bool("no-migration", false, "remove the migration command")
		noBuild := fs.Bool("no-build", false, "remove the build commands")
		var build multi
		fs.Var(&build, "build", "a build command; give the option one time for each command")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		// A change keeps the values that the command does not give.
		if err := a.api.Do(ctx, http.MethodGet, base, nil, nil, &state); err != nil {
			return err
		}
		body := map[string]any{"repository": *repository, "branch": *branch}
		if s := state.Source; s != nil {
			if *repository == "" {
				body["repository"] = s.Repository
			}
			if *branch == "" {
				body["branch"] = s.Branch
			}
		} else if *repository == "" || *branch == "" {
			return errors.New("give --repository and --branch")
		}
		if *webDirectory != "" {
			body["webDirectory"] = *webDirectory
		}
		if *trigger != "" {
			body["trigger"] = *trigger
		}
		if *checkMinutes != 0 {
			body["checkMinutes"] = *checkMinutes
		}
		if len(build) > 0 {
			body["buildCommands"] = []string(build)
		} else if *noBuild {
			body["buildCommands"] = []string{}
		}
		if *migration != "" {
			body["migrationCommand"] = *migration
		} else if *noMigration {
			body["migrationCommand"] = nil
		}
		if err := a.api.Do(ctx, http.MethodPut, base, nil, body, &state); err != nil {
			return err
		}
		return a.printDeploy(state)
	case "disconnect":
		if err := a.api.Do(ctx, http.MethodDelete, base, nil, nil, nil); err != nil {
			return err
		}
		fmt.Fprintln(a.out, "Disconnected. The website continues to serve the release that it has.")
		return nil
	case "secret", "secrets":
		if err := need(args, 2, "a website and the name of a secret, for example NPM_TOKEN"); err != nil {
			return err
		}
		path := base + "/secrets/" + url.PathEscape(args[1])
		switch action {
		case "set":
			value, err := a.secret("CF_SECRET", "Value of "+args[1]+": ")
			if err != nil {
				return err
			}
			if err := a.api.Do(ctx, http.MethodPut, path, nil, map[string]any{"value": value}, &state); err != nil {
				return err
			}
		case "remove", "rm":
			if err := a.api.Do(ctx, http.MethodDelete, path, nil, nil, &state); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown command deploys secret %s", action)
		}
		if a.json {
			return a.printJSON(state)
		}
		names := []string{}
		if state.Source != nil {
			names = state.Source.Secrets
		}
		fmt.Fprintf(a.out, "Secrets: %s\n", cell(strings.Join(names, ", ")))
		return nil
	case "start", "now":
		fs := flag.NewFlagSet("start", flag.ContinueOnError)
		forget := fs.Bool("forget-host-key", false, "forget the SSH host key of the Git server, after the server got a new key")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		var body any
		if *forget {
			body = map[string]any{"forgetHostKey": true}
		}
		return a.change(ctx, http.MethodPost, base+"/releases", body)
	case "webhook":
		// A new address and a new secret. The old ones stop at once.
		if err := a.api.Do(ctx, http.MethodPost, base+"/webhook", nil, nil, &state); err != nil {
			return err
		}
		if a.json || state.Source == nil {
			return a.printDeploy(state)
		}
		fmt.Fprintf(a.out, "Webhook:        %s\nWebhook secret: %s\nGive the new address and secret to the Git host. The old ones do not operate.\n", cell(state.Source.WebhookURL), cell(state.Source.WebhookSecret))
		return nil
	case "activate", "go-back":
		if err := need(args, 2, "a website and the ID of a release (cf deploys show WEBSITE)"); err != nil {
			return err
		}
		return a.change(ctx, http.MethodPost, base+"/releases/"+url.PathEscape(args[1])+"/activate", nil)
	}
	return fmt.Errorf("unknown command deploys %s", sub)
}

// readLimited reads a regular file of at most limit bytes. Adversarial review, finding C1-14:
// cf does not read a very large file, a pipe or a device into the memory.
func readLimited(name string, limit int) ([]byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil {
		return nil, err
	} else if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", name)
	}
	// One more byte shows that the file is too large. The caller gives the error text.
	return io.ReadAll(io.LimitReader(f, int64(limit)+1))
}
