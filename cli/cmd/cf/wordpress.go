package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"strings"
)

// FR-206: WordPress on a website. The panel installs WordPress, updates it with a restore point,
// and runs one WP-CLI command at a time in the container of the website.

type wordpressItem struct {
	Name    string  `json:"name"`
	Title   string  `json:"title"`
	Status  string  `json:"status"`
	Version string  `json:"version"`
	Update  *string `json:"update"`
}

type wordpressSite struct {
	Status      string          `json:"status"`
	Version     *string         `json:"version"`
	CoreMinor   *string         `json:"coreMinor"`
	CoreLatest  *string         `json:"coreLatest"`
	SiteURL     *string         `json:"siteUrl"`
	AdminURL    *string         `json:"adminUrl"`
	Plugins     []wordpressItem `json:"plugins"`
	Themes      []wordpressItem `json:"themes"`
	CheckedAt   *string         `json:"checkedAt"`
	AutoUpdates struct {
		Core    string `json:"core"`
		Plugins bool   `json:"plugins"`
		Themes  bool   `json:"themes"`
	} `json:"autoUpdates"`
	LastUpdate *struct {
		At        string  `json:"at"`
		Automatic bool    `json:"automatic"`
		State     string  `json:"state"`
		Error     *string `json:"error"`
	} `json:"lastUpdate"`
	RestorePoint *struct {
		At       string  `json:"at"`
		Version  *string `json:"version"`
		Database bool    `json:"database"`
	} `json:"restorePoint"`
	Error *string `json:"error"`
}

type wpCliCommand struct {
	ID       string  `json:"id"`
	Command  string  `json:"command"`
	Status   string  `json:"status"`
	ExitCode *int    `json:"exitCode"`
	Output   *string `json:"output"`
	JobID    string  `json:"jobId"`
}

func str(s *string) string {
	if s == nil {
		return "-"
	}
	return *s
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func (a *app) wordpress(ctx context.Context, sub string, args []string) error {
	if err := need(args, 1, "a website"); err != nil {
		return err
	}
	w, err := a.api.FindWebsite(ctx, args[0])
	if err != nil {
		return err
	}
	base := "/websites/" + w.ID + "/wordpress"
	switch sub {
	case "show", "get":
		var out struct {
			WordPress *wordpressSite `json:"wordpress"`
			Busy      *string        `json:"busy"`
		}
		if err := a.api.Do(ctx, http.MethodGet, base, nil, nil, &out); err != nil {
			return err
		}
		if a.json {
			return a.printJSON(out)
		}
		return a.printWordPress(w.Name, out.WordPress, out.Busy)
	case "install":
		fs := flag.NewFlagSet("install", flag.ContinueOnError)
		title := fs.String("title", "", "the title of the site")
		user := fs.String("admin-user", "", "the user name of the first administrator. Do not use admin")
		email := fs.String("admin-email", "", "the email address of the first administrator")
		locale := fs.String("locale", "en_GB", "the language of WordPress, for example en_GB or de_DE")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *title == "" || *user == "" || *email == "" {
			return errors.New("give --title, --admin-user and --admin-email")
		}
		var out map[string]json.RawMessage
		body := map[string]any{"title": *title, "adminUser": *user, "adminEmail": *email, "locale": *locale}
		if err := a.api.Do(ctx, http.MethodPost, base, nil, body, &out); err != nil {
			return err
		}
		if a.json {
			return a.printJSON(out)
		}
		var admin struct {
			User     string `json:"user"`
			Password string `json:"password"`
			LoginURL string `json:"loginUrl"`
		}
		json.Unmarshal(out["admin"], &admin)
		fmt.Fprintf(a.out, "Administrator: %s\nPassword: %s\nSign in at: %s\nCopy the password now. The panel does not show it again.\n", admin.User, admin.Password, admin.LoginURL)
		return a.followJob(ctx, out)
	case "check":
		return a.change(ctx, http.MethodPost, base+"/check", nil)
	case "update":
		fs := flag.NewFlagSet("update", flag.ContinueOnError)
		all := fs.Bool("all", false, "each available update: WordPress, the plugins and the themes")
		core := fs.String("core", "", "update WordPress: minor (security and maintenance) or latest")
		var plugins, themes multi
		fs.Var(&plugins, "plugin", "the slug of a plugin to update (repeat for more)")
		fs.Var(&themes, "theme", "the slug of a theme to update (repeat for more)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		body := map[string]any{}
		if *all {
			body["all"] = true
		}
		if *core != "" {
			body["core"] = *core
		}
		if len(plugins) > 0 {
			body["plugins"] = []string(plugins)
		}
		if len(themes) > 0 {
			body["themes"] = []string(themes)
		}
		if len(body) == 0 {
			return errors.New("give --all, --core, --plugin or --theme")
		}
		return a.change(ctx, http.MethodPost, base+"/updates", body)
	case "restore", "rollback":
		return a.change(ctx, http.MethodPost, base+"/restore", nil)
	case "settings", "set":
		fs := flag.NewFlagSet("settings", flag.ContinueOnError)
		core := fs.String("core", "", "the automatic updates of WordPress: off, minor or all")
		plugins := fs.String("plugins", "", "the automatic updates of the plugins: on or off")
		themes := fs.String("themes", "", "the automatic updates of the themes: on or off")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		auto := map[string]any{}
		if *core != "" {
			auto["core"] = *core
		}
		for name, v := range map[string]string{"plugins": *plugins, "themes": *themes} {
			switch v {
			case "":
			case "on", "off":
				auto[name] = v == "on"
			default:
				return fmt.Errorf("--%s is on or off", name)
			}
		}
		if len(auto) == 0 {
			return errors.New("give --core, --plugins or --themes")
		}
		return a.change(ctx, http.MethodPatch, base, map[string]any{"autoUpdates": auto})
	case "cli", "wp":
		words := args[1:]
		if len(words) > 0 && words[0] == "--" {
			words = words[1:]
		}
		if len(words) > 0 && words[0] == "wp" {
			words = words[1:]
		}
		if len(words) == 0 {
			return errors.New("give a WP-CLI command, for example: cf wordpress cli WEBSITE -- plugin list")
		}
		return a.wpCli(ctx, base, words)
	case "commands", "history":
		var out struct {
			Data []map[string]any `json:"data"`
		}
		if err := a.api.Do(ctx, http.MethodGet, base+"/commands", nil, nil, &out); err != nil {
			return err
		}
		if a.json {
			return a.printJSON(out.Data)
		}
		a.table(out.Data, []string{"ranAt", "user", "status", "exitCode", "command"})
		return nil
	}
	return fmt.Errorf("unknown command wordpress %s", sub)
}

// followJob waits for the job of an answer, unless the user gave --no-wait.
func (a *app) followJob(ctx context.Context, out map[string]json.RawMessage) error {
	var job struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if raw, ok := out["job"]; ok {
		json.Unmarshal(raw, &job)
	}
	if job.ID == "" {
		return nil
	}
	if a.noWait {
		fmt.Fprintf(a.out, "Job %s is in the queue: %s\n", job.ID, job.Title)
		return nil
	}
	return a.wait(ctx, job.ID)
}

// wpCli runs one WP-CLI command, waits for it, and prints its output. The command fails when
// the exit code of WP-CLI is not 0.
func (a *app) wpCli(ctx context.Context, base string, words []string) error {
	var out struct {
		Command wpCliCommand `json:"command"`
		Job     struct {
			ID string `json:"id"`
		} `json:"job"`
	}
	if err := a.api.Do(ctx, http.MethodPost, base+"/cli", nil, map[string]any{"args": words}, &out); err != nil {
		return err
	}
	if a.noWait {
		if a.json {
			return a.printJSON(out)
		}
		fmt.Fprintf(a.out, "Job %s is in the queue: %s\n", out.Job.ID, out.Command.Command)
		return nil
	}
	if _, err := a.api.WaitJob(ctx, out.Job.ID, a.poll, nil); err != nil {
		return err
	}
	var list struct {
		Data []wpCliCommand `json:"data"`
	}
	if err := a.api.Do(ctx, http.MethodGet, base+"/commands", nil, nil, &list); err != nil {
		return err
	}
	for _, c := range list.Data {
		if c.ID != out.Command.ID {
			continue
		}
		if a.json {
			return a.printJSON(c)
		}
		if c.Output != nil && *c.Output != "" {
			fmt.Fprintln(a.out, strings.TrimRight(*c.Output, "\n"))
		}
		if c.Status == "failed" {
			return errors.New("the command did not run")
		}
		if c.ExitCode != nil && *c.ExitCode != 0 {
			return fmt.Errorf("WP-CLI stopped with exit code %d", *c.ExitCode)
		}
		return nil
	}
	return errors.New("the panel has no result for the command")
}

func (a *app) printWordPress(name string, s *wordpressSite, busy *string) error {
	if s == nil {
		fmt.Fprintf(a.out, "%s has no WordPress that the panel knows.\nInstall it: cf wordpress install %s --title TITLE --admin-user USER --admin-email EMAIL\nOr find a site that you uploaded: cf wordpress check %s\n", name, name, name)
		return nil
	}
	fmt.Fprintf(a.out, "WordPress %s (%s)\n", str(s.Version), s.Status)
	if s.CoreMinor != nil {
		fmt.Fprintf(a.out, "Update: %s (cf wordpress update %s --core minor)\n", *s.CoreMinor, name)
	}
	if s.CoreLatest != nil {
		fmt.Fprintf(a.out, "New major version: %s (cf wordpress update %s --core latest)\n", *s.CoreLatest, name)
	}
	fmt.Fprintf(a.out, "Site: %s\nAdmin: %s\nLast check: %s\n", str(s.SiteURL), str(s.AdminURL), str(s.CheckedAt))
	fmt.Fprintf(a.out, "Automatic updates: WordPress %s, plugins %s, themes %s\n", s.AutoUpdates.Core, onOff(s.AutoUpdates.Plugins), onOff(s.AutoUpdates.Themes))
	if s.RestorePoint != nil {
		db := "files with no uploads"
		if s.RestorePoint.Database {
			db += ", and the database"
		}
		fmt.Fprintf(a.out, "Restore point: %s, WordPress %s (%s)\n", s.RestorePoint.At, str(s.RestorePoint.Version), db)
	}
	if u := s.LastUpdate; u != nil {
		fmt.Fprintf(a.out, "Last update: %s, %s", u.At, u.State)
		if u.Error != nil {
			fmt.Fprintf(a.out, ": %s", *u.Error)
		}
		fmt.Fprintln(a.out)
	}
	if s.Error != nil {
		fmt.Fprintf(a.out, "Error: %s\n", *s.Error)
	}
	if busy != nil {
		fmt.Fprintf(a.out, "A WordPress job runs now: %s\n", *busy)
	}
	for _, group := range []struct {
		title string
		items []wordpressItem
	}{{"Plugins", s.Plugins}, {"Themes", s.Themes}} {
		fmt.Fprintf(a.out, "\n%s:\n", group.title)
		rows := make([]map[string]any, 0, len(group.items))
		for _, i := range group.items {
			rows = append(rows, map[string]any{"name": i.Name, "status": i.Status, "version": i.Version, "update": str(i.Update)})
		}
		a.table(rows, []string{"name", "status", "version", "update"})
	}
	return nil
}
