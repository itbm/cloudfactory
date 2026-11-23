package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// The preview websites of the pull requests of a website that deploys from Git. The webhook of
// the website makes, deploys and removes the previews. A CI pipeline can also make a preview
// with "cf previews create".

type previewState struct {
	Settings *struct {
		Enabled         bool   `json:"enabled"`
		Domain          string `json:"domain"`
		Limit           int    `json:"limit"`
		CopyEnvironment bool   `json:"copyEnvironment"`
	} `json:"settings"`
	SuggestedDomain string           `json:"suggestedDomain"`
	MaxLimit        int              `json:"maxLimit"`
	Previews        []map[string]any `json:"previews"`
}

func (a *app) printPreviews(p previewState) error {
	if a.json {
		return a.printJSON(p)
	}
	if p.Settings == nil {
		fmt.Fprintln(a.out, "The website has no previews. Connect a Git repository first: cf deploys connect WEBSITE --repository URL --branch BRANCH")
		return nil
	}
	s := p.Settings
	if !s.Enabled {
		fmt.Fprintf(a.out, "Previews:  off\nTurn them on: cf previews set WEBSITE --on --domain %s\n", cell(p.SuggestedDomain))
		return nil
	}
	copied := "not given to the previews"
	if s.CopyEnvironment {
		copied = "given to the previews"
	}
	fmt.Fprintf(a.out, "Previews:  on\nNames:     pr-NUMBER.%s\nLimit:     %d at the same time\nVariables: %s\n", s.Domain, s.Limit, copied)
	if len(p.Previews) == 0 {
		fmt.Fprintln(a.out, "\nNo previews.")
		return nil
	}
	fmt.Fprintln(a.out)
	a.table(p.Previews, []string{"number", "state", "name", "branch", "title"})
	for _, r := range p.Previews {
		if e, ok := r["error"].(string); ok && e != "" {
			fmt.Fprintf(a.out, "#%s: %s\n", cell(r["number"]), oneLine(e))
		}
	}
	return nil
}

func (a *app) previews(ctx context.Context, sub string, args []string) error {
	if err := need(args, 1, "a website"); err != nil {
		return err
	}
	w, err := a.api.FindWebsite(ctx, args[0])
	if err != nil {
		return err
	}
	base := "/websites/" + w.ID + "/previews"
	var state previewState
	switch sub {
	case "list", "ls", "show", "get":
		if err := a.api.Do(ctx, http.MethodGet, base, nil, nil, &state); err != nil {
			return err
		}
		return a.printPreviews(state)
	case "set":
		fs := flag.NewFlagSet("set", flag.ContinueOnError)
		on := fs.Bool("on", false, "turn the previews on")
		off := fs.Bool("off", false, "turn the previews off; the panel removes the preview websites")
		domain := fs.String("domain", "", "the domain of the previews, for example preview.example.com")
		limit := fs.Int("limit", 0, "the number of previews at the same time")
		copyEnv := fs.Bool("copy-environment", false, "give the environment variables and the build secrets of the website to the previews")
		noCopyEnv := fs.Bool("no-copy-environment", false, "do not give the environment variables and the build secrets to the previews")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *on && *off {
			return errors.New("give --on or --off, not both")
		}
		if *copyEnv && *noCopyEnv {
			return errors.New("give --copy-environment or --no-copy-environment, not both")
		}
		// A change keeps the values that the command does not give.
		if err := a.api.Do(ctx, http.MethodGet, base, nil, nil, &state); err != nil {
			return err
		}
		if state.Settings == nil {
			return errors.New("the website has no Git repository; connect one first: cf deploys connect WEBSITE --repository URL --branch BRANCH")
		}
		body := map[string]any{"enabled": (state.Settings.Enabled || *on) && !*off}
		if *domain != "" {
			body["domain"] = *domain
		}
		if *limit != 0 {
			body["limit"] = *limit
		}
		if *copyEnv || *noCopyEnv {
			body["copyEnvironment"] = *copyEnv
		}
		if err := a.api.Do(ctx, http.MethodPut, base, nil, body, &state); err != nil {
			return err
		}
		return a.printPreviews(state)
	case "create", "deploy":
		fs := flag.NewFlagSet("create", flag.ContinueOnError)
		number := fs.Int("number", 0, "the number of the pull request")
		branch := fs.String("branch", "", "the branch of the pull request")
		title := fs.String("title", "", "the title of the pull request")
		page := fs.String("url", "", "the address of the pull request on the Git host")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *number < 1 || *branch == "" {
			return errors.New("give --number and --branch")
		}
		body := map[string]any{"number": *number, "branch": *branch}
		if *title != "" {
			body["title"] = *title
		}
		if *page != "" {
			body["url"] = *page
		}
		var out struct {
			Status  string         `json:"status"`
			Message string         `json:"message"`
			Preview map[string]any `json:"preview"`
		}
		if err := a.api.Do(ctx, http.MethodPost, base, nil, body, &out); err != nil {
			return err
		}
		if a.json {
			return a.printJSON(out)
		}
		fmt.Fprintln(a.out, out.Message)
		if name, ok := out.Preview["name"].(string); ok && name != "" {
			fmt.Fprintf(a.out, "Preview: https://%s\nFollow it: cf previews list %s\n", name, args[0])
		}
		return nil
	case "remove", "rm", "delete":
		if err := need(args, 2, "a website and the number of a pull request"); err != nil {
			return err
		}
		n, err := strconv.Atoi(strings.TrimPrefix(args[1], "#"))
		if err != nil || n < 1 {
			return fmt.Errorf("the number of a pull request is a number, not %q", args[1])
		}
		return a.change(ctx, http.MethodDelete, base+"/"+strconv.Itoa(n), nil)
	}
	return fmt.Errorf("unknown command previews %s", sub)
}
