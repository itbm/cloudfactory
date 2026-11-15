package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// notifications: the email notifications of the user, and the emails that the panel sent.
func (a *app) notifications(ctx context.Context, sub string, args []string) error {
	switch sub {
	case "", "list", "ls":
		fs := flag.NewFlagSet("list", flag.ContinueOnError)
		state := fs.String("state", "", "only the emails in this state: queued, sent, failed or skipped")
		event := fs.String("event", "", "only the emails of this event, for example job.failed")
		limit := limitFlags(fs)
		if err := fs.Parse(args); err != nil {
			return err
		}
		q := url.Values{}
		if *state != "" {
			q.Set("state", *state)
		}
		if *event != "" {
			q.Set("event", *event)
		}
		return a.listLimit(ctx, "/notifications", q, []string{"createdAt", "event", "recipient", "subject", "state", "error", "id"}, limit())
	case "get", "show":
		if err := need(args, 1, "an email ID"); err != nil {
			return err
		}
		var d map[string]any
		if err := a.api.Do(ctx, http.MethodGet, "/notifications/"+url.PathEscape(args[0]), nil, nil, &d); err != nil {
			return err
		}
		if a.json {
			return a.printJSON(d)
		}
		fmt.Fprintf(a.out, "To:       %v\nSubject:  %v\nEvent:    %v\nState:    %v (%v tries)\n", d["recipient"], d["subject"], d["label"], d["state"], d["attempts"])
		if d["error"] != nil {
			fmt.Fprintf(a.out, "Error:    %v\n", d["error"])
		}
		fmt.Fprintf(a.out, "\n%v\n", d["body"])
		return nil
	case "retry":
		if err := need(args, 1, "an email ID"); err != nil {
			return err
		}
		if err := a.api.Do(ctx, http.MethodPost, "/notifications/"+url.PathEscape(args[0])+"/retry", nil, nil, nil); err != nil {
			return err
		}
		fmt.Fprintln(a.out, "The email is in the queue again.")
		return nil
	case "preferences", "prefs":
		var r struct {
			Data []map[string]any `json:"data"`
		}
		if err := a.api.Do(ctx, http.MethodGet, "/me/notification-preferences", nil, nil, &r); err != nil {
			return err
		}
		return a.printPreferences(r.Data)
	case "on", "off":
		if len(args) == 0 {
			return errors.New("give one or more events, for example: cf notifications off job.failed")
		}
		events := map[string]bool{}
		for _, e := range args {
			events[e] = sub == "on"
		}
		var r struct {
			Data []map[string]any `json:"data"`
		}
		if err := a.api.Do(ctx, http.MethodPut, "/me/notification-preferences", nil, map[string]any{"events": events}, &r); err != nil {
			return err
		}
		return a.printPreferences(r.Data)
	}
	return fmt.Errorf("unknown command: notifications %s", sub)
}

func (a *app) printPreferences(rows []map[string]any) error {
	if a.json {
		return a.printJSON(rows)
	}
	for _, r := range rows {
		r["email"] = map[bool]string{true: "on", false: "off"}[r["email"] == true]
		if r["required"] == true {
			r["email"] = "always on"
		}
	}
	a.table(rows, []string{"event", "email", "label"})
	return nil
}

// mailServer: the SMTP server of the panel (administrators) or of a reseller.
func (a *app) mailServer(ctx context.Context, sub string, args []string) error {
	switch sub {
	case "", "get", "show":
		var s map[string]any
		if err := a.api.Do(ctx, http.MethodGet, "/mail-server", nil, nil, &s); err != nil {
			return err
		}
		return a.printMailServer(s)
	case "set":
		fs := flag.NewFlagSet("set", flag.ContinueOnError)
		host := fs.String("host", "", "the host name of the SMTP server")
		port := fs.Int("port", 587, "25, 465, 587 or 2525")
		encryption := fs.String("encryption", "", "tls, starttls or none. The default: tls for port 465, else starttls.")
		user := fs.String("user", "", "the user name. The password comes from CF_SMTP_PASSWORD, or the prompt.")
		from := fs.String("from", "", "the from address")
		name := fs.String("from-name", "", "the from name")
		replyTo := fs.String("reply-to", "", "the reply-to address")
		keep := fs.Bool("keep-password", false, "keep the saved password")
		off := fs.Bool("off", false, "save the server, but send no email through it")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if *host == "" || *from == "" {
			return errors.New("give --host and --from")
		}
		if *encryption == "" {
			*encryption = map[bool]string{true: "tls", false: "starttls"}[*port == 465]
		}
		body := map[string]any{"enabled": !*off, "host": *host, "port": *port, "encryption": *encryption, "username": *user, "fromAddress": *from, "fromName": *name, "replyTo": *replyTo}
		if *user != "" && !*keep {
			pw, err := a.secret("CF_SMTP_PASSWORD", "SMTP password: ")
			if err != nil {
				return err
			}
			body["password"] = pw
		}
		var s map[string]any
		if err := a.api.Do(ctx, http.MethodPut, "/mail-server", nil, body, &s); err != nil {
			return err
		}
		if err := a.printMailServer(s); err != nil {
			return err
		}
		if !a.json {
			fmt.Fprintln(a.out, "Send a test email: cf mail-server test")
		}
		return nil
	case "test":
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		to := fs.String("to", "", "administrators only: the address. The default is your own address.")
		if err := fs.Parse(args); err != nil {
			return err
		}
		body := map[string]any{}
		if *to != "" {
			body["to"] = *to
		}
		var r map[string]any
		if err := a.api.Do(ctx, http.MethodPost, "/mail-server/test", nil, body, &r); err != nil {
			return err
		}
		if a.json {
			return a.printJSON(r)
		}
		if r["sent"] != true {
			return fmt.Errorf("the SMTP server did not accept the email: %v", r["error"])
		}
		fmt.Fprintf(a.out, "The SMTP server accepted the email to %v.\n", r["to"])
		return nil
	case "remove", "delete":
		if err := a.api.Do(ctx, http.MethodDelete, "/mail-server", nil, nil, nil); err != nil {
			return err
		}
		fmt.Fprintln(a.out, "Removed the SMTP server.")
		return nil
	}
	return fmt.Errorf("unknown command: mail-server %s", sub)
}

func (a *app) printMailServer(s map[string]any) error {
	if a.json {
		return a.printJSON(s)
	}
	if s["configured"] != true {
		fmt.Fprintln(a.out, "No SMTP server is set.")
		if s["panelFallback"] == true {
			fmt.Fprintln(a.out, "The emails to your customers go through the server of the panel.")
		}
		return nil
	}
	state := "on"
	if s["enabled"] != true {
		state = "off"
	}
	test := "not tested"
	if s["testError"] != nil {
		test = fmt.Sprintf("failed: %v", s["testError"])
	} else if s["testedAt"] != nil {
		test = fmt.Sprintf("works (%v)", s["testedAt"])
	}
	fmt.Fprintf(a.out, "Server:   %v:%v (%v), %s\nUser:     %s\nFrom:     %s <%v>\nTest:     %s\n",
		s["host"], cell(s["port"]), s["encryption"], state, cell(s["username"]), strings.TrimSpace(cell(s["fromName"])), s["fromAddress"], test)
	return nil
}
