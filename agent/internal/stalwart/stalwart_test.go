package stalwart

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

const zone = `v1-ed25519-20261114._domainkey.acme.example.com. IN TXT "v=DKIM1; k=ed25519; h=sha256; p=a/UILaqJ="
v1-rsa-20261114._domainkey.acme.example.com. IN TXT (
    "v=DKIM1; k=rsa; h=sha256; p=MIIB"
    "IjAN"
)
acme.example.com. IN TXT "v=spf1 mx -all"
acme.example.com. IN MX 10 mail.example.net.
_dmarc.acme.example.com. IN TXT "v=DMARC1; p=reject; rua=mailto:postmaster@acme.example.com"
_imaps._tcp.acme.example.com. IN SRV 0 1 993 mail.example.net.
mta-sts.acme.example.com. IN CNAME mail.example.net.
_mta-sts.acme.example.com. IN TXT "v=STSv1; id=138449342089700316"
`

func TestRecordsKeepsTheRecordsThatTheOwnerAdds(t *testing.T) {
	want := []Record{
		{"TXT", "v1-ed25519-20261114._domainkey.acme.example.com", "v=DKIM1; k=ed25519; h=sha256; p=a/UILaqJ=", "dkim"},
		{"TXT", "v1-rsa-20261114._domainkey.acme.example.com", "v=DKIM1; k=rsa; h=sha256; p=MIIBIjAN", "dkim"},
		{"TXT", "acme.example.com", "v=spf1 mx -all", "spf"},
		{"MX", "acme.example.com", "10 mail.example.net.", "mx"},
		{"TXT", "_dmarc.acme.example.com", "v=DMARC1; p=reject; rua=mailto:postmaster@acme.example.com", "dmarc"},
	}
	if got := Records(zone, "acme.example.com"); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v", got)
	}
	// The records of a different domain are not records of this domain.
	if got := Records(zone, "example.com"); len(got) != 0 {
		t.Fatalf("got %#v", got)
	}
}

func TestReplacePortChangesEachRuleForPort25(t *testing.T) {
	in := object{"require": object{"match": object{"0": object{"if": "local_port != 25", "then": "true"}}, "else": "false"}, "other": "local_port == 250", "n": 25.0}
	got, changed := replacePort(in, "2025")
	want := object{"require": object{"match": object{"0": object{"if": "local_port != 2025", "then": "true"}}, "else": "false"}, "other": "local_port == 20250", "n": 25.0}
	if !changed || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v", got)
	}
	if _, changed := replacePort(object{"a": "local_port == 2025"}, "2025"); changed {
		t.Fatal("a rule for the new port is not a change")
	}
}

// TestRealServer uses a Stalwart container. It runs only with CF_TEST_STALWART, the address
// of the management API, and CF_TEST_STALWART_PASSWORD, the password of the administrator.
// CF_TEST_STALWART_SPAM_RULES is the path of the rules of the spam filter in the container.
// For example:
//
//	curl -LO https://github.com/stalwartlabs/spam-filter/releases/download/v3.0.4/spam-filter-rules.json.gz
//	docker run -d -e STALWART_RECOVERY_ADMIN=admin:secret -p 18080:8080 -v $PWD/spam-filter-rules.json.gz:/rules.json.gz:ro stalwartlabs/stalwart:v1.1
//	CF_TEST_STALWART=http://127.0.0.1:18080 CF_TEST_STALWART_PASSWORD=secret CF_TEST_STALWART_SPAM_RULES=/rules.json.gz go test ./internal/stalwart/
func TestRealServer(t *testing.T) {
	url := os.Getenv("CF_TEST_STALWART")
	if url == "" {
		t.Skip("CF_TEST_STALWART is not set")
	}
	c := &Client{URL: url, Password: os.Getenv("CF_TEST_STALWART_PASSWORD")}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := c.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	settings := Settings{Hostname: "mail.host.example.com", Ports: Ports{SMTP: 2025, Submissions: 2465, Submission: 2587, IMAPS: 2993, HTTP: 8080}, CertFile: "/etc/stalwart-certs/tls.crt", KeyFile: "/etc/stalwart-certs/tls.key", SpamRules: os.Getenv("CF_TEST_STALWART_SPAM_RULES")}
	if _, err := c.Configure(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("CF_TEST_STALWART_SETUP") != "" {
		// The server is in the recovery mode, as in the setup of the agent: it makes no DKIM
		// keys. Start it again in the normal mode, and run the test without this variable.
		return
	}
	// The second time changes nothing.
	if restart, err := c.Configure(ctx, settings); err != nil || restart {
		t.Fatalf("restart %v, err %v", restart, err)
	}
	listeners, err := c.get(ctx, "x:NetworkListener", nil, "name")
	if err != nil || len(listeners) != 5 {
		t.Fatalf("listeners %v, err %v", listeners, err)
	}
	if settings.SpamRules != "" {
		// The server reads the rules from the file in a task.
		deadline := time.Now().Add(time.Minute)
		for n, err := c.SpamRules(ctx); n == 0; n, err = c.SpamRules(ctx) {
			if err != nil || time.Now().After(deadline) {
				t.Fatalf("the spam filter has no rules: %v", err)
			}
			time.Sleep(time.Second)
		}
	}
	testSettings(ctx, t, c)

	const domain = "cf-test.example.com"
	records, err := c.SetDomain(ctx, domain)
	if err != nil {
		t.Fatal(err)
	}
	purposes := map[string]int{}
	for _, r := range records {
		purposes[r.Purpose]++
		if r.Purpose == "mx" && r.Value != "10 mail.host.example.com." {
			t.Errorf("MX record %q", r.Value)
		}
	}
	if !reflect.DeepEqual(purposes, map[string]int{"mx": 1, "spf": 1, "dkim": 2, "dmarc": 1}) {
		t.Fatalf("records %v", records)
	}
	again, err := c.SetDomain(ctx, domain)
	if err != nil || !reflect.DeepEqual(again, records) {
		t.Fatalf("the second time gave %v, err %v", again, err)
	}

	// A bcrypt hash of "correct horse battery".
	const hash = "$2y$12$7mTVQ4oM0n0Nq8sK4q6sUeUj2r4cQwQy6u1p8bq3v2w1o0n9m8l7K"
	if err := c.SetMailbox(ctx, domain, "info", 1<<30, hash); err != nil {
		t.Fatal(err)
	}
	if err := c.SetMailbox(ctx, domain, "info", 2<<30, ""); err != nil {
		t.Fatal(err)
	}
	if err := c.SetMailbox(ctx, domain, "new", 1<<30, ""); err == nil {
		t.Fatal("a new mailbox with no password must fail")
	}
	if err := c.SetMailbox(ctx, "missing.example.com", "info", 1<<30, hash); err == nil {
		t.Fatal("a mailbox in a domain that the server does not have must fail")
	}
	// Encryption at rest with the OpenPGP public key of the owner of the mailbox.
	publicKey, err := os.ReadFile("testdata/public-key.asc")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		key, err := c.SetEncryption(ctx, domain, "info", string(publicKey))
		if err != nil || len(key.Fingerprints) != 1 || key.Fingerprints[0] != "9E1777E56AFB330404EC710E0868736DF95B6518" || !strings.HasPrefix(key.Type, "OpenPGP") {
			t.Fatalf("key %+v, err %v", key, err)
		}
	}
	accounts, err := c.get(ctx, "x:Account", nil, "name", "encryptionAtRest")
	if err != nil || len(accounts) != 1 || accounts[0]["encryptionAtRest"].(object)["@type"] != "Enabled" {
		t.Fatalf("accounts %v, err %v", accounts, err)
	}
	// The second time removed the key of the first time.
	keys, err := c.call(ctx, "x:PublicKey/get", object{"accountId": accounts[0]["id"]})
	if list, _ := keys["list"].([]any); err != nil || len(list) != 1 {
		t.Fatalf("keys %v, err %v", keys, err)
	}
	var refused *ErrKey
	if _, err := c.SetEncryption(ctx, domain, "info", "-----BEGIN PGP PUBLIC KEY BLOCK-----\n\nAAAA\n-----END PGP PUBLIC KEY BLOCK-----\n"); !errors.As(err, &refused) {
		t.Fatalf("a block with no key gave %v", err)
	}
	if _, err := c.SetEncryption(ctx, domain, "nobody", string(publicKey)); err == nil {
		t.Fatal("a key for a mailbox that the server does not have must fail")
	}
	for range 2 {
		if key, err := c.SetEncryption(ctx, domain, "info", ""); err != nil || key != nil {
			t.Fatalf("key %+v, err %v", key, err)
		}
	}
	accounts, err = c.get(ctx, "x:Account", nil, "encryptionAtRest")
	if err != nil || accounts[0]["encryptionAtRest"].(object)["@type"] != "Disabled" {
		t.Fatalf("accounts %v, err %v", accounts, err)
	}
	usage, err := c.Usage(ctx)
	if _, ok := usage["info@"+domain]; err != nil || !ok {
		t.Fatalf("usage %v, err %v", usage, err)
	}
	for _, destinations := range [][]string{{"info@" + domain}, {"info@" + domain, "ann@elsewhere.example.net"}} {
		if err := c.SetAddress(ctx, domain, "team", destinations); err != nil {
			t.Fatal(err)
		}
	}
	lists, err := c.get(ctx, "x:MailingList", nil, "emailAddress", "recipients")
	if err != nil || len(lists) != 1 || len(lists[0]["recipients"].(object)) != 2 {
		t.Fatalf("lists %v, err %v", lists, err)
	}
	if err := c.DeleteDomain(ctx, domain); err == nil {
		t.Fatal("a domain with a mailbox must stay")
	}
	// Each removal can run again.
	for range 2 {
		if err := c.DeleteAddress(ctx, domain, "team"); err != nil {
			t.Fatal(err)
		}
		if err := c.DeleteMailbox(ctx, domain, "info"); err != nil {
			t.Fatal(err)
		}
		if err := c.DeleteDomain(ctx, domain); err != nil {
			t.Fatal(err)
		}
	}
	if keys, err := c.get(ctx, "x:DkimSignature", nil, "domainId"); err != nil || len(keys) != 2 {
		// The 2 keys of the host name stay.
		t.Fatalf("keys %v, err %v", keys, err)
	}
}

// testSettings sets the spam filter and the outbound relay two times, and removes the relay.
func testSettings(ctx context.Context, t *testing.T, c *Client) {
	t.Helper()
	for range 2 {
		if err := c.SetSpamFilter(ctx, SpamFilter{Enable: true, SpamScore: 6.5, RejectScore: 15}); err != nil {
			t.Fatal(err)
		}
	}
	spam, err := c.get(ctx, "x:SpamSettings", []string{singleton}, "enable", "scoreSpam", "scoreReject")
	if err != nil || len(spam) != 1 || spam[0]["enable"] != true || spam[0]["scoreSpam"] != 6.5 || spam[0]["scoreReject"] != 15.0 {
		t.Fatalf("spam settings %v, err %v", spam, err)
	}
	relay := &Relay{Host: "smtp.relay.example.net", Port: 587, Username: "mailer", Password: "secret"}
	for range 2 {
		if err := c.SetRelay(ctx, relay); err != nil {
			t.Fatal(err)
		}
	}
	// The server also has the routes "mx" and "local".
	relays := func() []object {
		routes, err := c.get(ctx, "x:MtaRoute", nil)
		if err != nil {
			t.Fatal(err)
		}
		var out []object
		for _, r := range routes {
			if r["name"] == relayRoute {
				out = append(out, r)
			}
		}
		return out
	}
	routes := relays()
	if len(routes) != 1 || routes[0]["host"] != "smtp.relay.example.net" || routes[0]["port"] != 587.0 || routes[0]["authUsername"] != "mailer" || routes[0]["implicitTls"] != false {
		t.Fatalf("routes %v", routes)
	}
	strategy, err := c.get(ctx, "x:MtaOutboundStrategy", []string{singleton}, "route")
	if err != nil || strategy[0]["route"].(object)["else"] != "'relay'" {
		t.Fatalf("strategy %v, err %v", strategy, err)
	}
	// A relay with no sign-in on port 465, then no relay.
	if err := c.SetRelay(ctx, &Relay{Host: "relay.example.net", Port: 465}); err != nil {
		t.Fatal(err)
	}
	if routes = relays(); len(routes) != 1 || routes[0]["implicitTls"] != true || routes[0]["authSecret"].(object)["@type"] != "None" {
		t.Fatalf("routes %v", routes)
	}
	for range 2 {
		if err := c.SetRelay(ctx, nil); err != nil {
			t.Fatal(err)
		}
	}
	strategy, err = c.get(ctx, "x:MtaOutboundStrategy", []string{singleton}, "route")
	if routes = relays(); err != nil || len(routes) != 0 || strategy[0]["route"].(object)["else"] != "'mx'" {
		t.Fatalf("routes %v, strategy %v, err %v", routes, strategy, err)
	}
}
