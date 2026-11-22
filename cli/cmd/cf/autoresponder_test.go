package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestTheAutoresponderOfAMailbox(t *testing.T) {
	srv, calls := mailPanel(t)
	a, out := testApp(srv)
	ctx := context.Background()
	if err := a.dispatch(ctx, []string{"mail", "mailboxes", "autoresponder", "info@acme.example.com"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Autoresponder: off", `Subject:       "Auto:" and the subject of the message`, "Last day:      2027-01-04T23:59:59Z", `filter rules "filters"`, "Back soon."} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output does not have %q:\n%s", want, out)
		}
	}
	if len(*calls) != 0 {
		t.Errorf("a read changed the autoresponder: %v", *calls)
	}
	out.Reset()
	a.in = strings.NewReader("I am away.\n")
	if err := a.dispatch(ctx, []string{"mail", "mailboxes", "autoresponder", "info@acme.example.com", "--on", "--subject", "Out of office", "--text-file", "-", "--from", "2026-12-20T08:00:00Z", "--until", "-"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Autoresponder: on") {
		t.Errorf("output:\n%s", out)
	}
	if err := a.dispatch(ctx, []string{"mail", "mailboxes", "autoresponder", "info@acme.example.com", "--off", "--subject", "-"}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`PUT /api/v1/mail/mailboxes/MB1/autoresponder {"enabled":true,"subject":"Out of office","text":"I am away.\n","startsAt":"2026-12-20T08:00:00Z","endsAt":null}`,
		`PUT /api/v1/mail/mailboxes/MB1/autoresponder {"enabled":false,"subject":null,"text":"Back soon.","startsAt":null,"endsAt":"2027-01-04T23:59:59Z"}`,
	}
	if strings.Join(*calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s", strings.Join(*calls, "\n"))
	}
	for _, args := range [][]string{
		{"mail", "mailboxes", "autoresponder"},
		{"mail", "mailboxes", "autoresponder", "info@acme.example.com", "--on", "--off"},
		{"mail", "mailboxes", "autoresponder", "info@acme.example.com", "--from", "20 December"},
		{"mail", "mailboxes", "autoresponder", "info@acme.example.com", "--text-file", "/no/such/file.txt"},
		{"mail", "mailboxes", "autoresponder", "nobody@acme.example.com"},
	} {
		if err := a.dispatch(ctx, args); err == nil {
			t.Errorf("%v must be an error", args)
		}
	}
}

func TestADayIsInTheLocalTimeZone(t *testing.T) {
	local := time.Local
	defer func() { time.Local = local }()
	time.Local = time.FixedZone("CET", 3600)
	for in, want := range map[[2]string]string{
		{"2026-12-20", "start"}:           "2026-12-19T23:00:00Z",
		{"2026-12-20", "end"}:             "2026-12-20T22:59:59Z",
		{"2026-12-20T09:00:00+01:00", ""}: "2026-12-20T08:00:00Z",
	} {
		got, err := dayOrTime(in[0], in[1] == "end")
		if err != nil || *got != want {
			t.Errorf("dayOrTime(%q, %s) = %v, %v; want %s", in[0], in[1], got, err, want)
		}
	}
	if got, err := dayOrTime("-", true); got != nil || err != nil {
		t.Errorf("dayOrTime(-) = %v, %v", got, err)
	}
}
