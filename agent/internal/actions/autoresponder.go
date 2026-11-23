package actions

import (
	"context"
	"errors"
	"fmt"
	"time"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/stalwart"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// The autoresponder of a mailbox. The mail server keeps it, and the webmail changes the same
// autoresponder: the panel keeps no copy. The worker reads and changes it at once, with no job.

// autoresponderTimeout is the maximum time of one request to the mail server.
const autoresponderTimeout = 30 * time.Second

// InvalidError is the error of a request with a value that the agent does not accept.
type InvalidError struct{ Err error }

func (e *InvalidError) Error() string { return e.Err.Error() }
func (e *InvalidError) Unwrap() error { return e.Err }

func invalid(format string, a ...any) error {
	return &InvalidError{fmt.Errorf(format, a...)}
}

// GetAutoresponder gives the autoresponder of a mailbox.
func (d *Deps) GetAutoresponder(ctx context.Context, req *agentv1.MailAddressRequest) (*agentv1.Autoresponder, error) {
	domain, localPart := req.GetDomain(), req.GetLocalPart()
	if err := checkMailAddress(domain, localPart); err != nil {
		return nil, &InvalidError{err}
	}
	ctx, cancel := context.WithTimeout(ctx, autoresponderTimeout)
	defer cancel()
	c, err := d.mailClient(ctx)
	if err != nil {
		return nil, err
	}
	a, err := c.GetAutoresponder(ctx, domain, localPart)
	if err != nil {
		return nil, err
	}
	return autoresponderProto(a), nil
}

// SetAutoresponder sets the autoresponder of a mailbox, and gives it as the mail server has it.
func (d *Deps) SetAutoresponder(ctx context.Context, req *agentv1.SetAutoresponderRequest) (*agentv1.Autoresponder, error) {
	domain, localPart := req.GetDomain(), req.GetLocalPart()
	if err := checkMailAddress(domain, localPart); err != nil {
		return nil, &InvalidError{err}
	}
	a, err := checkAutoresponder(req.GetAutoresponder())
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, autoresponderTimeout)
	defer cancel()
	c, err := d.mailClient(ctx)
	if err != nil {
		return nil, err
	}
	got, err := c.SetAutoresponder(ctx, domain, localPart, a)
	if errors.Is(err, stalwart.ErrRefused) {
		return nil, &InvalidError{err}
	}
	if err != nil {
		return nil, err
	}
	return autoresponderProto(got), nil
}

// checkAutoresponder checks the values of an autoresponder from the panel.
func checkAutoresponder(in *agentv1.Autoresponder) (stalwart.Autoresponder, error) {
	a := stalwart.Autoresponder{Enabled: in.GetEnabled(), Subject: in.GetSubject(), Text: in.GetText()}
	if err := validate.AutoresponderSubject(a.Subject); err != nil {
		return a, &InvalidError{err}
	}
	if err := validate.AutoresponderText(a.Text); err != nil {
		return a, &InvalidError{err}
	}
	if a.Enabled && a.Text == "" {
		return a, invalid("an autoresponder that is on needs a text")
	}
	for name, v := range map[string]struct {
		s string
		t *time.Time
	}{"start": {in.GetStartsAt(), &a.StartsAt}, "end": {in.GetEndsAt(), &a.EndsAt}} {
		if v.s == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, v.s)
		if err != nil {
			return a, invalid("the %s of the autoresponder is not an RFC 3339 time", name)
		}
		*v.t = t.UTC()
	}
	if !a.StartsAt.IsZero() && !a.EndsAt.IsZero() && !a.EndsAt.After(a.StartsAt) {
		return a, invalid("the end of the autoresponder must be after its start")
	}
	return a, nil
}

func autoresponderProto(a stalwart.Autoresponder) *agentv1.Autoresponder {
	date := func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.UTC().Format(time.RFC3339)
	}
	return &agentv1.Autoresponder{Enabled: a.Enabled, Subject: a.Subject, Text: a.Text,
		StartsAt: date(a.StartsAt), EndsAt: date(a.EndsAt), FilterScript: a.FilterScript}
}
