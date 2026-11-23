package stalwart

import (
	"context"
	"errors"
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"
)

// ErrRefused is the error of an autoresponder that the mail server did not accept, for
// example a text that is too long.
var ErrRefused = errors.New("the mail server refused the autoresponder")

// vacationScript is the name of the Sieve script that the mail server makes from the
// autoresponder (JMAP VacationResponse).
const vacationScript = "vacation"

// Autoresponder is the autoresponder of a mailbox (JMAP VacationResponse). The mail server
// makes a Sieve script from it, and runs the script for each new message.
type Autoresponder struct {
	Enabled bool
	// Empty: the subject is "Auto:" and the subject of the message.
	Subject string
	Text    string
	// Zero: no start, or no end.
	StartsAt, EndsAt time.Time
	// FilterScript is the name of the active Sieve script of the mailbox when it is not the
	// script of the autoresponder. The mail server runs one script for each mailbox.
	FilterScript string
}

// GetAutoresponder gives the autoresponder of a mailbox.
func (c *Client) GetAutoresponder(ctx context.Context, domain, localPart string) (Autoresponder, error) {
	account, err := c.MailAccount(ctx, domain, localPart)
	if err != nil {
		return Autoresponder{}, err
	}
	return c.autoresponder(ctx, account)
}

func (c *Client) autoresponder(ctx context.Context, account string) (Autoresponder, error) {
	result, err := c.call(ctx, "VacationResponse/get", object{"accountId": account, "ids": []string{singleton}})
	if err != nil {
		return Autoresponder{}, err
	}
	list, _ := result["list"].([]any)
	var v object
	if len(list) == 1 {
		v, _ = list[0].(object)
	}
	if v == nil {
		return Autoresponder{}, fmt.Errorf("mail server: VacationResponse/get gave no autoresponder: %v", result)
	}
	a := Autoresponder{}
	a.Enabled, _ = v["isEnabled"].(bool)
	a.Subject, _ = v["subject"].(string)
	a.Text, _ = v["textBody"].(string)
	if htmlBody, _ := v["htmlBody"].(string); a.Text == "" && htmlBody != "" {
		// A mail program can set only the HTML text.
		a.Text = plainText(htmlBody)
	}
	for field, t := range map[string]*time.Time{"fromDate": &a.StartsAt, "toDate": &a.EndsAt} {
		if s, ok := v[field].(string); ok && s != "" {
			if *t, err = time.Parse(time.RFC3339, s); err != nil {
				return Autoresponder{}, fmt.Errorf("mail server: the autoresponder has the date %q: %w", s, err)
			}
		}
	}
	scripts, err := c.call(ctx, "SieveScript/get", object{"accountId": account, "properties": []string{"name", "isActive"}})
	if err != nil {
		return Autoresponder{}, err
	}
	list, _ = scripts["list"].([]any)
	for _, item := range list {
		s, _ := item.(object)
		if name, _ := s["name"].(string); s["isActive"] == true && name != vacationScript {
			a.FilterScript = name
		}
	}
	return a, nil
}

// SetAutoresponder sets the autoresponder of a mailbox, and gives it as the mail server has
// it. The autoresponder gets only the plain text: the HTML text of a mail program goes.
// FilterScript is not used.
//
// When the autoresponder is on, the mail server makes its Sieve script active. The other
// active script of the mailbox stops, and does not start again when the autoresponder is off.
func (c *Client) SetAutoresponder(ctx context.Context, domain, localPart string, a Autoresponder) (Autoresponder, error) {
	account, err := c.MailAccount(ctx, domain, localPart)
	if err != nil {
		return Autoresponder{}, err
	}
	date := func(t time.Time) any {
		if t.IsZero() {
			return nil
		}
		return t.UTC().Format(time.RFC3339)
	}
	text := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	result, err := c.call(ctx, "VacationResponse/set", object{"accountId": account, "update": object{singleton: object{
		"isEnabled": a.Enabled,
		"subject":   text(a.Subject),
		"textBody":  text(a.Text),
		"htmlBody":  nil,
		"fromDate":  date(a.StartsAt),
		"toDate":    date(a.EndsAt),
	}}})
	if err != nil {
		return Autoresponder{}, err
	}
	if failed, _ := result["notUpdated"].(object); failed[singleton] != nil {
		e := setError("VacationResponse/set", failed[singleton])
		if e.Type == "invalidProperties" {
			return Autoresponder{}, fmt.Errorf("%w: %s", ErrRefused, e.Detail)
		}
		return Autoresponder{}, e
	}
	return c.autoresponder(ctx, account)
}

var (
	htmlBreak = regexp.MustCompile(`(?i)<br\s*/?>|</(p|div|li|h[1-6]|tr)>`)
	htmlTag   = regexp.MustCompile(`<[^>]*>`)
	blankRuns = regexp.MustCompile(`\n{3,}`)
)

// plainText gives the text of an HTML text, with a line break for each paragraph.
func plainText(s string) string {
	s = htmlBreak.ReplaceAllString(s, "\n")
	s = htmlTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(line)
	}
	return strings.TrimSpace(blankRuns.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}
