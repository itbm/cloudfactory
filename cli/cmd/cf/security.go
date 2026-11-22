package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
)

// The protection of a website in the edge proxy: the firewall, the IP address rules, the
// password-protected paths and the sign-in limit (docs/architecture.md#protection-of-a-website).

// wordPressSignInPaths are the sign-in paths of WordPress (WebsiteSecurity::WORDPRESS_SIGN_IN_PATHS).
var wordPressSignInPaths = []string{"/wp-login.php", "/xmlrpc.php"}

type securityState struct {
	Firewall struct {
		Mode          string   `json:"mode"`
		ExcludedRules []int    `json:"excludedRules"`
		SkippedPaths  []string `json:"skippedPaths"`
	} `json:"firewall"`
	IPRules struct {
		Allow []string `json:"allow"`
		Block []string `json:"block"`
	} `json:"ipRules"`
	ProtectedPaths []struct {
		Path  string `json:"path"`
		Users []struct {
			Name string `json:"name"`
		} `json:"users"`
	} `json:"protectedPaths"`
	SignInLimit struct {
		RequestsPerMinute int      `json:"requestsPerMinute"`
		Paths             []string `json:"paths"`
	} `json:"signInLimit"`
}

type firewallEvent struct {
	At       string `json:"at"`
	ClientIP string `json:"clientIp"`
	Host     string `json:"host"`
	URI      string `json:"uri"`
	Action   string `json:"action"`
	Score    int    `json:"score"`
	Rules    []struct {
		ID      int    `json:"id"`
		Message string `json:"message"`
		Data    string `json:"data"`
	} `json:"rules"`
}

func (a *app) security(ctx context.Context, sub string, args []string) error {
	if err := need(args, 1, "a website"); err != nil {
		return err
	}
	w, err := a.api.FindWebsite(ctx, args[0])
	if err != nil {
		return err
	}
	base := "/websites/" + w.ID + "/security"
	read := func() (securityState, error) {
		var out struct {
			Security securityState `json:"security"`
		}
		err := a.api.Do(ctx, http.MethodGet, base, nil, nil, &out)
		return out.Security, err
	}
	switch sub {
	case "show", "get":
		s, err := read()
		if err != nil {
			return err
		}
		if a.json {
			return a.printJSON(s)
		}
		a.printSecurity(s)
		return nil
	case "firewall":
		fs := flag.NewFlagSet("firewall", flag.ContinueOnError)
		mode := fs.String("mode", "", "off, detect or block")
		noExclude := fs.Bool("no-exclusions", false, "turn all rules on again")
		noSkip := fs.Bool("no-skip", false, "examine all paths again")
		var exclude, include, skip multi
		fs.Var(&exclude, "exclude", "the ID of a rule to turn off; give the option one time for each rule")
		fs.Var(&include, "include", "the ID of a rule to turn on again")
		fs.Var(&skip, "skip", "a path that the firewall does not examine, for example /wp-admin/")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		s, err := read()
		if err != nil {
			return err
		}
		if *mode == "" {
			*mode = s.Firewall.Mode
		}
		rules := s.Firewall.ExcludedRules
		if *noExclude {
			rules = nil
		}
		for _, v := range exclude {
			id, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("--exclude %q: give the number of the rule, for example 942100", v)
			}
			rules = append(rules, id)
		}
		for _, v := range include {
			id, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("--include %q: give the number of the rule, for example 942100", v)
			}
			rules = slices.DeleteFunc(rules, func(r int) bool { return r == id })
		}
		paths := s.Firewall.SkippedPaths
		if *noSkip {
			paths = nil
		}
		paths = append(paths, skip...)
		return a.change(ctx, http.MethodPatch, base, map[string]any{"firewall": map[string]any{"mode": *mode, "excludedRules": nonNil(rules), "skippedPaths": nonNil(paths)}})
	case "ip", "ips":
		fs := flag.NewFlagSet("ip", flag.ContinueOnError)
		noAllow := fs.Bool("no-allow", false, "remove all addresses from the allow list")
		noBlock := fs.Bool("no-block", false, "remove all addresses from the block list")
		var allow, block, remove multi
		fs.Var(&allow, "allow", "an address or a range to allow, for example 203.0.113.0/24; with an allow list, the edge proxy refuses all other addresses")
		fs.Var(&block, "block", "an address or a range to block")
		fs.Var(&remove, "remove", "an address or a range to remove from the two lists")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if len(allow)+len(block)+len(remove) == 0 && !*noAllow && !*noBlock {
			return errors.New("give --allow, --block, --remove, --no-allow or --no-block")
		}
		s, err := read()
		if err != nil {
			return err
		}
		lists := map[string][]string{"allow": s.IPRules.Allow, "block": s.IPRules.Block}
		if *noAllow {
			lists["allow"] = nil
		}
		if *noBlock {
			lists["block"] = nil
		}
		lists["allow"] = append(lists["allow"], allow...)
		lists["block"] = append(lists["block"], block...)
		for name, list := range lists {
			lists[name] = nonNil(slices.DeleteFunc(list, func(v string) bool { return slices.Contains(remove, v) }))
		}
		return a.change(ctx, http.MethodPatch, base, map[string]any{"ipRules": lists})
	case "sign-in-limit", "signin-limit":
		fs := flag.NewFlagSet("sign-in-limit", flag.ContinueOnError)
		perMinute := fs.Int("per-minute", -1, "the largest number of POST requests in one minute from one IP address; 0 is no limit")
		wordPress := fs.Bool("wordpress", false, "use the sign-in paths of WordPress: "+strings.Join(wordPressSignInPaths, " "))
		var paths multi
		fs.Var(&paths, "path", "a sign-in path, for example /login; give the option one time for each path")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *perMinute < 0 {
			return errors.New("give --per-minute, for example --per-minute 10")
		}
		body := map[string]any{"requestsPerMinute": *perMinute}
		if *wordPress {
			paths = append(paths, wordPressSignInPaths...)
		}
		if len(paths) > 0 {
			body["paths"] = []string(paths)
		}
		return a.change(ctx, http.MethodPatch, base, map[string]any{"signInLimit": body})
	case "protect", "unprotect":
		if err := need(args, 2, "a website and a path, for example /staging/"); err != nil {
			return err
		}
		path := args[1]
		fs := flag.NewFlagSet(sub, flag.ContinueOnError)
		user := fs.String("user", "", "the user name")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		if sub == "protect" && *user == "" {
			return errors.New("give --user")
		}
		s, err := read()
		if err != nil {
			return err
		}
		// The list of the API has all paths. A user with no password keeps the password.
		var list []map[string]any
		found, changed := false, false
		for _, p := range s.ProtectedPaths {
			var users []map[string]any
			for _, u := range p.Users {
				if p.Path == path && u.Name == *user {
					changed = true
					if sub == "unprotect" {
						continue
					}
				}
				users = append(users, map[string]any{"name": u.Name})
			}
			if p.Path == path {
				found = true
				if sub == "unprotect" && *user == "" {
					changed = true
					continue
				}
			}
			if len(users) > 0 {
				list = append(list, map[string]any{"path": p.Path, "users": users})
			}
		}
		if sub == "unprotect" {
			if !changed {
				if !found {
					return fmt.Errorf("the path %s has no password", path)
				}
				return fmt.Errorf("the path %s has no user %s", path, *user)
			}
			return a.change(ctx, http.MethodPatch, base, map[string]any{"protectedPaths": nonNil(list)})
		}
		password, err := a.secret("CF_PATH_PASSWORD", "Password for "+*user+": ")
		if err != nil {
			return err
		}
		if password == "" {
			return errors.New("give the password in CF_PATH_PASSWORD or on stdin")
		}
		entry := map[string]any{"name": *user, "password": password}
		added := false
		for _, p := range list {
			if p["path"] != path {
				continue
			}
			users := p["users"].([]map[string]any)
			for i, u := range users {
				if u["name"] == *user {
					users[i] = entry
					added = true
				}
			}
			if !added {
				p["users"] = append(users, entry)
				added = true
			}
		}
		if !added {
			list = append(list, map[string]any{"path": path, "users": []map[string]any{entry}})
		}
		return a.change(ctx, http.MethodPatch, base, map[string]any{"protectedPaths": list})
	case "events":
		fs := flag.NewFlagSet("events", flag.ContinueOnError)
		limit := fs.Int("limit", 50, "the number of events, at most 200")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		var out struct {
			Events []firewallEvent `json:"events"`
		}
		if err := a.api.Do(ctx, http.MethodGet, base+"/events", url.Values{"limit": {strconv.Itoa(*limit)}}, nil, &out); err != nil {
			return err
		}
		if a.json {
			return a.printJSON(out.Events)
		}
		if len(out.Events) == 0 {
			fmt.Fprintln(a.out, "The firewall found no attacks.")
		}
		for _, e := range out.Events {
			fmt.Fprintf(a.out, "%s %-8s %s score %d %s\n", e.At, e.Action, e.ClientIP, e.Score, e.URI)
			for _, r := range e.Rules {
				fmt.Fprintf(a.out, "    rule %d: %s\n", r.ID, r.Message)
			}
		}
		return nil
	}
	return fmt.Errorf("unknown command security %s", sub)
}

func (a *app) printSecurity(s securityState) {
	tw := tabwriter.NewWriter(a.out, 0, 4, 2, ' ', 0)
	none := func(v []string) string {
		if len(v) == 0 {
			return "-"
		}
		return strings.Join(v, " ")
	}
	rules := make([]string, len(s.Firewall.ExcludedRules))
	for i, r := range s.Firewall.ExcludedRules {
		rules[i] = strconv.Itoa(r)
	}
	fmt.Fprintf(tw, "Firewall\t%s\n", s.Firewall.Mode)
	fmt.Fprintf(tw, "Rules that are off\t%s\n", none(rules))
	fmt.Fprintf(tw, "Paths with no firewall\t%s\n", none(s.Firewall.SkippedPaths))
	fmt.Fprintf(tw, "Allowed addresses\t%s\n", none(s.IPRules.Allow))
	fmt.Fprintf(tw, "Blocked addresses\t%s\n", none(s.IPRules.Block))
	if len(s.ProtectedPaths) == 0 {
		fmt.Fprintf(tw, "Protected paths\t-\n")
	}
	for _, p := range s.ProtectedPaths {
		names := make([]string, len(p.Users))
		for i, u := range p.Users {
			names[i] = u.Name
		}
		fmt.Fprintf(tw, "Protected path %s\tusers %s\n", p.Path, strings.Join(names, " "))
	}
	if s.SignInLimit.RequestsPerMinute > 0 {
		fmt.Fprintf(tw, "Sign-in limit\t%d a minute for %s\n", s.SignInLimit.RequestsPerMinute, none(s.SignInLimit.Paths))
	} else {
		fmt.Fprintf(tw, "Sign-in limit\t-\n")
	}
	tw.Flush()
}

// nonNil gives an empty list for nil, so that the JSON value is [] and not null.
func nonNil[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}
