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

	"github.com/itbm/cloudfactory/cli/internal/client"
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
	case "chmod":
		if err := need(args, 3, "a website, a path and a mode, for example 644"); err != nil {
			return err
		}
		fs := flag.NewFlagSet("chmod", flag.ContinueOnError)
		recursive := fs.Bool("recursive", false, "also change all directories and files in the directory")
		fileMode := fs.String("file-mode", "", "with --recursive: the mode of each file, for example 644")
		if err := fs.Parse(args[3:]); err != nil {
			return err
		}
		if *recursive != (*fileMode != "") {
			return errors.New("use --recursive and --file-mode together, for example --recursive --file-mode 644")
		}
		body := map[string]any{"path": path, "mode": args[2]}
		if *recursive {
			body["recursive"], body["fileMode"] = true, *fileMode
		}
		var out struct {
			File    fileEntry `json:"file"`
			Changed int       `json:"changed"`
		}
		if err := a.api.Do(ctx, http.MethodPut, base+"/permissions", nil, body, &out); err != nil {
			return err
		}
		if a.json {
			return a.printJSON(out)
		}
		if *recursive {
			fmt.Fprintf(a.out, "Changed the mode of %d directories and files in %s.\n", out.Changed, path)
			return nil
		}
		fmt.Fprintf(a.out, "Changed the mode of %s to %s.\n", path, out.File.Mode)
		return nil
	case "archive":
		if err := need(args, 3, "a website, the path of the new archive and the paths to put in it"); err != nil {
			return err
		}
		paths := make([]string, 0, len(args)-2)
		for _, p := range args[2:] {
			paths = append(paths, sitePath(p))
		}
		return a.archiveJob(ctx, base+"/archives", map[string]any{"paths": paths, "path": path}, path)
	case "extract":
		fs := flag.NewFlagSet("extract", flag.ContinueOnError)
		to := fs.String("to", "", "the directory for the files (the default is the directory of the archive)")
		overwrite := fs.Bool("overwrite", false, "replace the files that exist")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		body := map[string]any{"path": path, "overwrite": *overwrite}
		if *to != "" {
			body["to"] = sitePath(*to)
		}
		return a.archiveJob(ctx, base+"/extract", body, path)
	}
	return fmt.Errorf("unknown command files %s", sub)
}

// archiveJob starts a job that makes or extracts an archive. It waits for the job, then
// prints the summary of the job.
func (a *app) archiveJob(ctx context.Context, path string, body map[string]any, archive string) error {
	var out struct {
		Job *client.Job `json:"job"`
	}
	if err := a.api.Do(ctx, http.MethodPost, path, nil, body, &out); err != nil {
		return err
	}
	if a.json {
		return a.printJSON(out)
	}
	if out.Job == nil {
		return errors.New("the panel did not start a job")
	}
	if a.noWait {
		fmt.Fprintf(a.out, "Job %s is in the queue: %s\n", out.Job.ID, out.Job.Title)
		return nil
	}
	job, err := a.api.WaitJob(ctx, out.Job.ID, a.poll, nil)
	if err != nil {
		return fmt.Errorf("%s: %w", job.Title, err)
	}
	s := job.Result
	if s == nil {
		fmt.Fprintf(a.out, "%s: complete.\n", job.Title)
		return nil
	}
	contents := fmt.Sprintf("%s, %s and %s", plural(s.Files, "file", "files"), plural(s.Directories, "directory", "directories"), plural(s.Links, "link", "links"))
	if strings.HasSuffix(path, "/archives") {
		fmt.Fprintf(a.out, "Made %s: %s, %s.\n", archive, contents, plural(int(s.SizeBytes), "byte", "bytes"))
	} else {
		fmt.Fprintf(a.out, "Extracted %s: %s.\n", archive, contents)
	}
	if s.Kept > 0 {
		fmt.Fprintf(a.out, "%s already and did not change. Add --overwrite to replace them.\n", plural(s.Kept, "file exists", "files exist"))
	}
	if s.Skipped > 0 {
		fmt.Fprintf(a.out, "Skipped %s:\n", plural(s.Skipped, "entry", "entries"))
		for _, n := range s.SkippedNames {
			fmt.Fprintf(a.out, "  %s\n", n)
		}
		if s.Skipped > len(s.SkippedNames) {
			fmt.Fprintf(a.out, "  and %d more\n", s.Skipped-len(s.SkippedNames))
		}
	}
	return nil
}

// logs prints the last log lines of a website: "cf logs WEBSITE".
func (a *app) logs(ctx context.Context, website string, args []string) error {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	lines := fs.Int("lines", 200, "the number of lines, at most 2000")
	stream := fs.String("type", "", "access or error")
	if err := fs.Parse(args); err != nil {
		return err
	}
	w, err := a.api.FindWebsite(ctx, website)
	if err != nil {
		return err
	}
	q := url.Values{"lines": {strconv.Itoa(*lines)}}
	if *stream != "" {
		q.Set("stream", *stream)
	}
	var out struct {
		Lines []struct {
			At     string `json:"at"`
			Stream string `json:"stream"`
			Text   string `json:"text"`
		} `json:"lines"`
	}
	if err := a.api.Do(ctx, http.MethodGet, "/websites/"+w.ID+"/logs", q, nil, &out); err != nil {
		return err
	}
	if a.json {
		return a.printJSON(out)
	}
	for _, l := range out.Lines {
		fmt.Fprintf(a.out, "%s %-6s %s\n", l.At, l.Stream, l.Text)
	}
	return nil
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
