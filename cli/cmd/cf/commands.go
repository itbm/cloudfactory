package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// websiteCommand is one entry of the command log of a website.
type websiteCommand struct {
	ID         string  `json:"id"`
	Command    string  `json:"command"`
	State      string  `json:"state"`
	ExitCode   *int    `json:"exitCode"`
	TimedOut   bool    `json:"timedOut"`
	Actor      string  `json:"actor"`
	IP         *string `json:"ip"`
	JobID      *string `json:"jobId"`
	CreatedAt  string  `json:"createdAt"`
	StartedAt  *string `json:"startedAt"`
	FinishedAt *string `json:"finishedAt"`
	Output     *string `json:"output"`
}

// commands runs a command in the container of a website, and shows the command log.
func (a *app) commands(ctx context.Context, sub string, args []string) error {
	if err := need(args, 1, "a website"); err != nil {
		return err
	}
	w, err := a.api.FindWebsite(ctx, args[0])
	if err != nil {
		return err
	}
	base := "/websites/" + w.ID + "/commands"
	switch sub {
	case "list", "ls":
		fs := flag.NewFlagSet("list", flag.ContinueOnError)
		fs.SetOutput(a.err)
		search := fs.String("search", "", "show only the commands with this text")
		limit := limitFlags(fs)
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		q := url.Values{}
		if *search != "" {
			q.Set("q", *search)
		}
		return a.listLimit(ctx, base, q, []string{"id", "createdAt", "actor", "state", "exitCode", "command"}, limit())
	case "get", "show":
		if err := need(args, 2, "a website and the ID of a command (cf commands list WEBSITE)"); err != nil {
			return err
		}
		c, err := a.getCommand(ctx, base, args[1])
		if err != nil {
			return err
		}
		if a.json {
			return a.printJSON(c)
		}
		a.showCommand(c)
		return nil
	case "run":
		return a.runCommand(ctx, base, args[1:])
	}
	return fmt.Errorf("unknown command commands %s", sub)
}

func (a *app) getCommand(ctx context.Context, base, id string) (websiteCommand, error) {
	var out struct {
		Command websiteCommand `json:"command"`
	}
	err := a.api.Do(ctx, http.MethodGet, base+"/"+url.PathEscape(id), nil, nil, &out)
	return out.Command, err
}

// showCommand prints an entry of the command log and its output.
func (a *app) showCommand(c websiteCommand) {
	ip := ""
	if c.IP != nil {
		ip = " from " + *c.IP
	}
	fmt.Fprintf(a.out, "Command: %s\nBy: %s%s\nAt: %s\nState: %s\n", c.Command, c.Actor, ip, c.CreatedAt, c.State)
	if c.ExitCode != nil {
		fmt.Fprintf(a.out, "Exit code: %d\n", *c.ExitCode)
	}
	if c.Output != nil && *c.Output != "" {
		fmt.Fprintf(a.out, "\n%s\n", strings.TrimRight(*c.Output, "\n"))
	}
}

// runCommand runs one command line in the container of a website and waits for the result.
// The exit code of cf is the exit code of the command.
func (a *app) runCommand(ctx context.Context, base string, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(a.err)
	timeout := fs.Int("timeout", 0, "stop the command after this number of seconds, from 1 to 3600 (default 600)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	line := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if line == "" {
		return errors.New(`give the command, for example: cf commands run WEBSITE -- php artisan migrate --force`)
	}
	body := map[string]any{"command": line}
	if *timeout > 0 {
		body["timeoutSeconds"] = *timeout
	}
	var out struct {
		Command websiteCommand `json:"command"`
	}
	if err := a.api.Do(ctx, http.MethodPost, base, nil, body, &out); err != nil {
		return err
	}
	c := out.Command
	if a.noWait {
		if a.json {
			return a.printJSON(c)
		}
		fmt.Fprintf(a.out, "Command %s runs: %s\nSee the result: cf commands get WEBSITE %s\n", c.ID, c.Command, c.ID)
		return nil
	}
	// The panel completes the entry when the agent reports the result. One failed request
	// does not end the wait.
	failures := 0
	for c.State == "running" {
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w. The command continues on the server: cf commands get WEBSITE %s", ctx.Err(), c.ID)
		case <-time.After(a.poll):
		}
		next, err := a.getCommand(ctx, base, c.ID)
		if err != nil {
			if failures++; failures > 5 {
				return fmt.Errorf("%w. The command continues on the server: cf commands get WEBSITE %s", err, c.ID)
			}
			continue
		}
		failures = 0
		c = next
	}
	if a.json {
		return a.printJSON(c)
	}
	if c.Output != nil && *c.Output != "" {
		fmt.Fprintln(a.out, strings.TrimRight(*c.Output, "\n"))
	}
	switch {
	case c.State == "failed":
		return errors.New("the command did not start")
	case c.ExitCode != nil && *c.ExitCode != 0:
		text := fmt.Sprintf("the command ended with exit code %d", *c.ExitCode)
		if c.TimedOut {
			text = "the command did not end in its time limit"
		}
		return &exitError{code: *c.ExitCode, text: text}
	}
	return nil
}
