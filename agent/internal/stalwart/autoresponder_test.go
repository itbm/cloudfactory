package stalwart

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPlainTextReadsTheHTMLOfAMailProgram(t *testing.T) {
	for in, want := range map[string]string{
		"<p>I am away.</p><p>Back on <b>Monday</b> &amp; Tuesday.</p>": "I am away.\nBack on Monday & Tuesday.",
		"<div>One<br>Two<br/>Three</div>":                              "One\nTwo\nThree",
		"No tags":                                                      "No tags",
		"<p>One</p>\n\n\n\n<p>Two</p>":                                 "One\n\nTwo",
	} {
		if got := plainText(in); got != want {
			t.Errorf("plainText(%q) = %q, want %q", in, got, want)
		}
	}
}

// testAutoresponder runs in TestRealServer, on a domain with the mailbox "info".
func testAutoresponder(ctx context.Context, t *testing.T, c *Client, domain string) {
	t.Helper()
	a, err := c.GetAutoresponder(ctx, domain, "info")
	if err != nil || a.Enabled || a.Text != "" || a.FilterScript != "" {
		t.Fatalf("a new mailbox: %+v, err %v", a, err)
	}
	if _, err := c.GetAutoresponder(ctx, domain, "nobody"); !errors.Is(err, ErrNoMailbox) {
		t.Fatalf("an address with no mailbox: %v", err)
	}
	if _, err := c.GetAutoresponder(ctx, "missing.example.com", "info"); !errors.Is(err, ErrNoMailbox) {
		t.Fatalf("a domain that the server does not have: %v", err)
	}
	// The filter rules of the user: the autoresponder stops them when it is on.
	account, err := c.MailAccount(ctx, domain, "info")
	if err != nil {
		t.Fatal(err)
	}
	script := "require \"fileinto\";\nif header :contains \"subject\" \"offer\" { fileinto \"Junk\"; }\n"
	blob, err := c.upload(ctx, account, "application/sieve", strings.NewReader(script), int64(len(script)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.call(ctx, "SieveScript/set", object{"accountId": account, "create": object{"f": object{"name": "filters", "blobId": blob}}, "onSuccessActivateScript": "#f"}); err != nil {
		t.Fatal(err)
	}
	if a, err := c.GetAutoresponder(ctx, domain, "info"); err != nil || a.FilterScript != "filters" {
		t.Fatalf("with filter rules: %+v, err %v", a, err)
	}
	start, end := time.Date(2026, 12, 20, 8, 0, 0, 0, time.UTC), time.Date(2027, 1, 4, 23, 59, 59, 0, time.UTC)
	want := Autoresponder{Enabled: true, Subject: `"Away" \ back soon`, Text: "Hello,\n\nI am away. Café \"; discard; \".\n\tAnn", StartsAt: start, EndsAt: end}
	for range 2 {
		got, err := c.SetAutoresponder(ctx, domain, "info", want)
		if err != nil || got != want {
			t.Fatalf("set %+v, err %v", got, err)
		}
	}
	scripts, err := c.call(ctx, "SieveScript/get", object{"accountId": account, "properties": []string{"name", "isActive"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range scripts["list"].([]any) {
		s := item.(object)
		if active := s["name"] == vacationScript; s["isActive"] != active {
			t.Errorf("script %v", s)
		}
	}
	// Off, with no subject and no dates: the text stays for the next time.
	off := Autoresponder{Text: "Back soon."}
	if got, err := c.SetAutoresponder(ctx, domain, "info", off); err != nil || got != off {
		t.Fatalf("off %+v, err %v", got, err)
	}
	if _, err := c.SetAutoresponder(ctx, domain, "info", Autoresponder{Enabled: true, Text: strings.Repeat("a", 4000)}); !errors.Is(err, ErrRefused) {
		t.Fatalf("a text that is too long: %v", err)
	}
	// The webmail can set only the HTML text.
	if _, err := c.call(ctx, "VacationResponse/set", object{"accountId": account, "update": object{singleton: object{"textBody": nil, "htmlBody": "<p>Away</p><p>Ann</p>"}}}); err != nil {
		t.Fatal(err)
	}
	if a, err := c.GetAutoresponder(ctx, domain, "info"); err != nil || a.Text != "Away\nAnn" {
		t.Fatalf("HTML only: %+v, err %v", a, err)
	}
}
