package actions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/stalwart"
)

// fakeVacation is a mail server with the mailbox info@acme.example.com.
type fakeVacation struct {
	mu       sync.Mutex
	response map[string]any
	calls    []string
}

func (f *fakeVacation) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MethodCalls [][]json.RawMessage `json:"methodCalls"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var method string
	var args map[string]any
	_ = json.Unmarshal(req.MethodCalls[0][0], &method)
	_ = json.Unmarshal(req.MethodCalls[0][1], &args)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, method)
	var result map[string]any
	switch method {
	case "x:Domain/query":
		result = map[string]any{"ids": []string{"d1"}}
	case "x:Domain/get":
		result = map[string]any{"list": []any{map[string]any{"id": "d1", "name": "acme.example.com"}}}
	case "x:Account/query":
		filter, _ := args["filter"].(map[string]any)
		ids := []string{}
		if filter["name"] == "info" {
			ids = append(ids, "a1")
		}
		result = map[string]any{"ids": ids}
	case "x:Account/get":
		result = map[string]any{"list": []any{map[string]any{"id": "a1", "name": "info", "domainId": "d1"}}}
	case "VacationResponse/get":
		result = map[string]any{"list": []any{f.response}}
	case "VacationResponse/set":
		update := args["update"].(map[string]any)["singleton"].(map[string]any)
		if text, _ := update["textBody"].(string); len(text) > 2047 {
			result = map[string]any{"notUpdated": map[string]any{"singleton": map[string]any{"type": "invalidProperties", "properties": []string{"textBody"}}}}
			break
		}
		for k, v := range update {
			f.response[k] = v
		}
		result = map[string]any{"updated": map[string]any{"singleton": nil}}
	case "SieveScript/get":
		result = map[string]any{"list": []any{map[string]any{"name": "vacation", "isActive": f.response["isEnabled"]}, map[string]any{"name": "filters", "isActive": f.response["isEnabled"] != true}}}
	default:
		result = map[string]any{"type": "unknownMethod"}
		method = "error"
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"methodResponses": []any{[]any{method, result, "0"}}})
}

func TestTheAutoresponderOfAMailbox(t *testing.T) {
	d := mailDeps(t)
	ctx := context.Background()
	address := &agentv1.MailAddressRequest{Domain: "acme.example.com", LocalPart: "info"}
	if _, err := d.GetAutoresponder(ctx, address); !errors.Is(err, ErrNoMail) {
		t.Fatalf("a server with no mail server: %v", err)
	}
	if _, err := d.prepareMail(ctx, mailHost); err != nil {
		t.Fatal(err)
	}
	fake := &fakeVacation{response: map[string]any{"id": "singleton", "isEnabled": false, "subject": nil, "textBody": nil, "htmlBody": "<p>Away</p>", "fromDate": nil, "toDate": nil}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	both(d, func(d *Deps) { d.MailURL = srv.URL })

	got, err := d.GetAutoresponder(ctx, address)
	if err != nil || got.GetEnabled() || got.GetText() != "Away" || got.GetFilterScript() != "filters" {
		t.Fatalf("get %v, err %v", got, err)
	}
	want := &agentv1.Autoresponder{Enabled: true, Subject: "Out of office", Text: "I am away.\n\nAnn", StartsAt: "2026-12-20T08:00:00Z", EndsAt: "2027-01-04T23:59:59Z"}
	set := &agentv1.SetAutoresponderRequest{Domain: "acme.example.com", LocalPart: "info", Autoresponder: want}
	if got, err = d.SetAutoresponder(ctx, set); err != nil || got.String() != want.String() {
		t.Fatalf("set %v, err %v", got, err)
	}
	if fake.response["htmlBody"] != nil {
		t.Errorf("the HTML text of the mail program stays: %v", fake.response["htmlBody"])
	}
	// A time in another time zone is in UTC on the mail server.
	set.Autoresponder = &agentv1.Autoresponder{Enabled: true, Text: "Away", StartsAt: "2026-12-20T09:00:00+01:00"}
	if got, err = d.SetAutoresponder(ctx, set); err != nil || got.GetStartsAt() != "2026-12-20T08:00:00Z" || got.GetEndsAt() != "" || got.GetSubject() != "" {
		t.Fatalf("set %v, err %v", got, err)
	}
	if _, err := d.GetAutoresponder(ctx, &agentv1.MailAddressRequest{Domain: "acme.example.com", LocalPart: "nobody"}); !errors.Is(err, stalwart.ErrNoMailbox) {
		t.Errorf("an address with no mailbox: %v", err)
	}
	// The mail server refuses a text that is too long for it.
	set.Autoresponder = &agentv1.Autoresponder{Enabled: true, Text: strings.Repeat("a", 2000) + strings.Repeat("é", 100)}
	var refused *InvalidError
	fake.calls = nil
	if _, err := d.SetAutoresponder(ctx, set); !errors.As(err, &refused) {
		t.Errorf("a long text: %v", err)
	}
	if len(fake.calls) != 0 {
		t.Errorf("the agent sent a text that the rule refuses: %v", fake.calls)
	}

	for name, a := range map[string]*agentv1.Autoresponder{
		"no text":              {Enabled: true},
		"a subject with lines": {Enabled: true, Text: "Away", Subject: "Away\nBcc: x@example.org"},
		"a long subject":       {Enabled: true, Text: "Away", Subject: strings.Repeat("a", 201)},
		"a control character":  {Enabled: true, Text: "Away\x1b[31m"},
		"a date":               {Enabled: true, Text: "Away", StartsAt: "2026-12-20"},
		"the end first":        {Enabled: true, Text: "Away", StartsAt: "2026-12-20T08:00:00Z", EndsAt: "2026-12-19T08:00:00Z"},
	} {
		if _, err := d.SetAutoresponder(ctx, &agentv1.SetAutoresponderRequest{Domain: "acme.example.com", LocalPart: "info", Autoresponder: a}); !errors.As(err, &refused) {
			t.Errorf("SetAutoresponder accepted %s: %v", name, err)
		}
	}
	if _, err := d.GetAutoresponder(ctx, &agentv1.MailAddressRequest{Domain: "acme.example.com", LocalPart: "a..b"}); !errors.As(err, &refused) {
		t.Errorf("an address that is not valid: %v", err)
	}
}
