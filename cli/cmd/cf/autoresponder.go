package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// autoresponder is the autoresponder of a mailbox, as the panel API gives it.
type autoresponder struct {
	Enabled      bool    `json:"enabled"`
	Subject      *string `json:"subject"`
	Text         string  `json:"text"`
	StartsAt     *string `json:"startsAt"`
	EndsAt       *string `json:"endsAt"`
	FilterScript *string `json:"filterScript,omitempty"`
}

// mailAutoresponder shows or changes the autoresponder of a mailbox. The mail server keeps it,
// and the change has no job.
func (a *app) mailAutoresponder(ctx context.Context, args []string) error {
	if err := need(args, 1, "a mailbox"); err != nil {
		return err
	}
	fs := flag.NewFlagSet("autoresponder", flag.ContinueOnError)
	on := fs.Bool("on", false, "turn on the autoresponder")
	off := fs.Bool("off", false, "turn off the autoresponder. The mail server keeps the text")
	subject := fs.String("subject", "", `the subject of the answer. "-" removes it: the subject is then "Auto:" and the subject of the message`)
	textFile := fs.String("text-file", "", "a file with the text of the answer, or - for the standard input")
	from := fs.String("from", "", `the first day (YYYY-MM-DD, in the time zone of this computer) or an RFC 3339 time. "-" removes it`)
	until := fs.String("until", "", `the last day (YYYY-MM-DD, in the time zone of this computer) or an RFC 3339 time. "-" removes it`)
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *on && *off {
		return errors.New("give --on or --off, not the two")
	}
	m, err := a.mailRow(ctx, "/mail/mailboxes", "mailbox", args[0])
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/mail/mailboxes/%v/autoresponder", m["id"])
	var got struct {
		Autoresponder autoresponder `json:"autoresponder"`
	}
	if err := a.api.Do(ctx, http.MethodGet, path, nil, nil, &got); err != nil {
		return err
	}
	cur := got.Autoresponder
	changed := false
	if *on || *off {
		cur.Enabled, changed = *on, true
	}
	if *subject != "" {
		cur.Subject, changed = optional(*subject), true
	}
	if *textFile != "" {
		var text []byte
		if *textFile == "-" {
			in := a.in
			if in == nil {
				in = os.Stdin
			}
			text, err = io.ReadAll(io.LimitReader(in, 64<<10))
		} else {
			text, err = os.ReadFile(*textFile)
		}
		if err != nil {
			return fmt.Errorf("the text: %w", err)
		}
		cur.Text, changed = string(text), true
	}
	for _, d := range []struct {
		value string
		end   bool
		field **string
	}{{*from, false, &cur.StartsAt}, {*until, true, &cur.EndsAt}} {
		if d.value == "" {
			continue
		}
		t, err := dayOrTime(d.value, d.end)
		if err != nil {
			return err
		}
		*d.field, changed = t, true
	}
	if changed {
		body := cur
		body.FilterScript = nil
		var out struct {
			Autoresponder autoresponder `json:"autoresponder"`
		}
		if err := a.api.Do(ctx, http.MethodPut, path, nil, body, &out); err != nil {
			return err
		}
		cur = out.Autoresponder
	}
	if a.json {
		return a.printJSON(cur)
	}
	state := "off"
	if cur.Enabled {
		state = "on"
	}
	show := func(s *string, none string) string {
		if s == nil {
			return none
		}
		return *s
	}
	fmt.Fprintf(a.out, "Autoresponder: %s\nSubject:       %s\nFirst day:     %s\nLast day:      %s\n", state, show(cur.Subject, `"Auto:" and the subject of the message`), show(cur.StartsAt, "none"), show(cur.EndsAt, "none"))
	if cur.FilterScript != nil {
		fmt.Fprintf(a.out, "The mailbox has the filter rules %q. They stop while the autoresponder is on, and they do not start again after.\n", *cur.FilterScript)
	}
	fmt.Fprintf(a.out, "\n%s\n", cur.Text)
	return nil
}

// optional gives nil for "-".
func optional(s string) *string {
	if s == "-" {
		return nil
	}
	return &s
}

// dayOrTime reads a day (the start or the end of the day in the local time zone) or an RFC 3339
// time. "-" gives nil.
func dayOrTime(s string, end bool) (*string, error) {
	if s == "-" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		day, err := time.ParseInLocation(time.DateOnly, s, time.Local)
		if err != nil {
			return nil, fmt.Errorf("%q is not a day (YYYY-MM-DD) or an RFC 3339 time", s)
		}
		t = day
		if end {
			// A day with a change of the clock does not have 24 hours.
			t = time.Date(day.Year(), day.Month(), day.Day(), 23, 59, 59, 0, time.Local)
		}
	}
	out := t.UTC().Format(time.RFC3339)
	return &out, nil
}
