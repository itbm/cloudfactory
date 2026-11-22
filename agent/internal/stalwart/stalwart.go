// Package stalwart manages a Stalwart mail server (release 1.1 or later) through its
// management API. Stalwart keeps all its settings, domains and accounts in its data store. A
// client changes them with JMAP method calls on the HTTP port: "x:<Object>/get", "/set" and
// "/query".
//
// The agent uses only this package to change the mail server. Each function is idempotent: a
// job that runs again gives the same result.
package stalwart

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

// AdminUser is the name of the administrator that the container gets in its environment
// (STALWART_RECOVERY_ADMIN). Stalwart accepts it with all rights while the variable is set.
const AdminUser = "admin"

// singleton is the ID of an object that the server has one time, for example SystemSettings.
const singleton = "singleton"

// Client calls the management API of one mail server.
type Client struct {
	// URL is the base address of the HTTP port, for example "http://169.254.77.1:19025".
	URL      string
	Password string
	HTTP     *http.Client
}

// Ports are the ports of the listeners in the container. The firewall of the host redirects
// the public ports 25, 465, 587 and 993 to them. Stalwart does not run as root, so it cannot
// listen on a port below 1024 in the container.
type Ports struct {
	SMTP, Submissions, Submission, IMAPS int
	// HTTP is the port of the management API.
	HTTP int
}

// Settings are the settings that the agent applies to the mail server.
type Settings struct {
	// Hostname is the name of the mail server in the DNS, for example "mail.example.net".
	Hostname string
	Ports    Ports
	// CertFile and KeyFile are the paths, in the container, of the TLS certificate of
	// Hostname and of its key.
	CertFile, KeyFile string
	// SpamRules is the path, in the container, of the file with the rules of the spam filter.
	// Empty: the agent does not change the rules.
	SpamRules string
}

// SpamFilter is the spam filter of the mail server.
type SpamFilter struct {
	// Enable is true when the mail server examines each new message.
	Enable bool
	// SpamScore is the score from which a message goes to the junk folder.
	SpamScore float64
	// RejectScore is the score from which the server refuses a message. 0: no refusal.
	RejectScore float64
}

// Relay is a mail server that sends the outgoing mail of this mail server.
type Relay struct {
	Host string
	// Port 465 uses TLS from the start. The other ports use STARTTLS.
	Port     int
	Username string
	Password string
}

// Record is a DNS record that the owner of a mail domain adds.
type Record struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Value   string `json:"value"`
	Purpose string `json:"purpose"`
}

type object = map[string]any

// set gives a JSON set: Stalwart writes a set of values as a map of each value to true.
func set(values ...string) object {
	out := object{}
	for _, v := range values {
		out[v] = true
	}
	return out
}

// call sends one method call and gives its result.
func (c *Client) call(ctx context.Context, method string, args object) (object, error) {
	body, err := json.Marshal(object{
		"using":       []string{"urn:ietf:params:jmap:core", "urn:stalwart:jmap", "urn:ietf:params:jmap:filenode"},
		"methodCalls": []any{[]any{method, args, "0"}},
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.URL, "/")+"/jmap/", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(AdminUser, c.Password)
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mail server: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("mail server: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mail server: %s: status %d: %s", method, resp.StatusCode, short(raw))
	}
	var out struct {
		MethodResponses [][]json.RawMessage `json:"methodResponses"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.MethodResponses) != 1 || len(out.MethodResponses[0]) < 2 {
		return nil, fmt.Errorf("mail server: %s: the answer is not a method response: %s", method, short(raw))
	}
	var name string
	result := object{}
	if err := json.Unmarshal(out.MethodResponses[0][0], &name); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(out.MethodResponses[0][1], &result); err != nil {
		return nil, err
	}
	if name == "error" {
		return nil, fmt.Errorf("mail server: %s: %v: %v", method, result["type"], result["description"])
	}
	return result, nil
}

func short(b []byte) string {
	if len(b) > 300 {
		b = b[:300]
	}
	return strings.TrimSpace(string(b))
}

// Ready reports nil when the management API answers.
func (c *Client) Ready(ctx context.Context) error {
	_, err := c.call(ctx, "x:SystemSettings/get", object{"ids": []string{singleton}, "properties": []string{"defaultHostname"}})
	return err
}

// get gives the objects with these IDs, with these properties.
func (c *Client) get(ctx context.Context, kind string, ids []string, properties ...string) ([]object, error) {
	args := object{"ids": ids}
	if ids == nil {
		args["ids"] = nil
	}
	if len(properties) > 0 {
		args["properties"] = properties
	}
	result, err := c.call(ctx, kind+"/get", args)
	if err != nil {
		return nil, err
	}
	list, _ := result["list"].([]any)
	out := make([]object, 0, len(list))
	for _, item := range list {
		if o, ok := item.(object); ok {
			out = append(out, o)
		}
	}
	return out, nil
}

// query gives the IDs of all objects that agree with the filter.
func (c *Client) query(ctx context.Context, kind string, filter object) ([]string, error) {
	const page = 500
	var ids []string
	for position := 0; ; position += page {
		args := object{"position": position, "limit": page}
		if filter != nil {
			args["filter"] = filter
		}
		result, err := c.call(ctx, kind+"/query", args)
		if err != nil {
			return nil, err
		}
		list, _ := result["ids"].([]any)
		for _, id := range list {
			if s, ok := id.(string); ok {
				ids = append(ids, s)
			}
		}
		if len(list) < page {
			return ids, nil
		}
	}
}

// SetError is the reason that the mail server did not make, change or remove an object.
type SetError struct {
	Method string
	Type   string
	Detail string
	// Existing is the ID of the object that has the same key, for the type "primaryKeyViolation".
	Existing string
}

func (e *SetError) Error() string {
	return fmt.Sprintf("mail server: %s: %s %s", e.Method, e.Type, e.Detail)
}

func setError(method string, v any) *SetError {
	o, _ := v.(object)
	e := &SetError{Method: method, Type: fmt.Sprint(o["type"])}
	if d, ok := o["description"].(string); ok {
		e.Detail = d
	}
	if p, ok := o["properties"]; ok {
		e.Detail = strings.TrimSpace(e.Detail + " " + fmt.Sprint(p))
	}
	if v, ok := o["validationErrors"]; ok {
		e.Detail = strings.TrimSpace(e.Detail + " " + fmt.Sprint(v))
	}
	if id, ok := o["objectId"].(object); ok {
		e.Existing, _ = id["id"].(string)
	}
	return e
}

// create makes one object and gives its ID.
func (c *Client) create(ctx context.Context, kind string, value object) (string, error) {
	result, err := c.call(ctx, kind+"/set", object{"create": object{"new": value}})
	if err != nil {
		return "", err
	}
	if created, ok := result["created"].(object); ok {
		if o, ok := created["new"].(object); ok {
			id, _ := o["id"].(string)
			return id, nil
		}
	}
	failed, _ := result["notCreated"].(object)
	return "", setError(kind+"/set", failed["new"])
}

// update changes properties of one object.
func (c *Client) update(ctx context.Context, kind, id string, changes object) error {
	result, err := c.call(ctx, kind+"/set", object{"update": object{id: changes}})
	if err != nil {
		return err
	}
	if failed, ok := result["notUpdated"].(object); ok && failed[id] != nil {
		return setError(kind+"/set", failed[id])
	}
	return nil
}

// destroy removes objects. An object that the server does not have is not an error.
func (c *Client) destroy(ctx context.Context, kind string, ids ...string) error {
	if len(ids) == 0 {
		return nil
	}
	result, err := c.call(ctx, kind+"/set", object{"destroy": ids})
	if err != nil {
		return err
	}
	failed, _ := result["notDestroyed"].(object)
	for _, id := range ids {
		if failed[id] == nil {
			continue
		}
		if e := setError(kind+"/set", failed[id]); e.Type != "notFound" {
			return e
		}
	}
	return nil
}

// reload makes the server read its settings or its certificates again.
func (c *Client) reload(ctx context.Context, action string) error {
	_, err := c.create(ctx, "x:Action", object{"@type": action})
	return err
}

// Configure applies the settings of the agent to the mail server:
//
//   - the listeners for SMTP, submission and IMAP on the ports of the container, and the
//     HTTP listener of the management API. It removes the other listeners (POP3, ManageSieve
//     and HTTPS);
//   - the rules of the server that name port 25, which now is Ports.SMTP;
//   - the host name, with a domain of the same name for the reports of the server;
//   - the TLS certificate from files;
//   - the log on the standard output, for the journal of the host;
//   - no download of the web interface or of the network lists from the internet;
//   - the rules of the spam filter from a file of the image.
//
// It reports true when the listeners changed: the server uses new listeners after a restart.
func (c *Client) Configure(ctx context.Context, s Settings) (restart bool, err error) {
	if restart, err = c.listeners(ctx, s.Ports); err != nil {
		return false, err
	}
	if err := c.smtpPort(ctx, s.Ports.SMTP); err != nil {
		return false, err
	}
	if err := c.hostname(ctx, s.Hostname); err != nil {
		return false, err
	}
	if err := c.certificate(ctx, s.CertFile, s.KeyFile); err != nil {
		return false, err
	}
	if err := c.logToStdout(ctx); err != nil {
		return false, err
	}
	if err := c.noDownloads(ctx); err != nil {
		return false, err
	}
	if err := c.reload(ctx, "ReloadSettings"); err != nil {
		return false, err
	}
	return restart, c.spamRules(ctx, s.SpamRules)
}

// spamRulesTask is the task of the server that reads the rules of the spam filter again.
var spamRulesTask = object{"@type": "SpamFilterMaintenance", "maintenanceType": "updateRules"}

// spamRules makes the server read the rules of the spam filter from a file of the image. By
// default, the server downloads them from GitHub. The firewall stops that download, and with
// no rules the filter gives each message the score 0. The server reads the file again at each
// start of the agent, so a new image gives new rules.
func (c *Client) spamRules(ctx context.Context, file string) error {
	if file == "" {
		return nil
	}
	url := "file://" + file
	current, err := c.get(ctx, "x:SpamSettings", []string{singleton}, "spamFilterRulesUrl")
	if err != nil {
		return err
	}
	if len(current) != 1 || current[0]["spamFilterRulesUrl"] != url {
		if err := c.update(ctx, "x:SpamSettings", singleton, object{"spamFilterRulesUrl": url}); err != nil {
			return err
		}
		if err := c.reload(ctx, "ReloadSettings"); err != nil {
			return err
		}
	}
	_, err = c.create(ctx, "x:Task", spamRulesTask)
	return err
}

// SpamRules gives the number of rules that the spam filter has.
func (c *Client) SpamRules(ctx context.Context) (int, error) {
	ids, err := c.query(ctx, "x:SpamRule", nil)
	return len(ids), err
}

// SetSpamFilter turns the spam filter on or off and sets its scores. The server adds the
// header X-Spam-Status to each message, and puts a message with SpamScore or more in the junk
// folder.
func (c *Client) SetSpamFilter(ctx context.Context, f SpamFilter) error {
	if err := c.update(ctx, "x:SpamSettings", singleton, object{"enable": f.Enable, "scoreSpam": f.SpamScore, "scoreReject": f.RejectScore}); err != nil {
		return err
	}
	return c.reload(ctx, "ReloadSettings")
}

// relayRoute is the name of the route of the outbound relay.
const relayRoute = "relay"

// directRoute is the route of the outgoing mail with no relay: to the MX of the recipient.
const directRoute = "'mx'"

// SetRelay sends the outgoing mail through a relay, or with nil, directly to the mail server of
// each recipient. Mail for a domain of this server always stays on this server.
func (c *Client) SetRelay(ctx context.Context, r *Relay) error {
	routes, err := c.get(ctx, "x:MtaRoute", nil, "name")
	if err != nil {
		return err
	}
	id := ""
	var others []string
	for _, route := range routes {
		rid, _ := route["id"].(string)
		if route["name"] == relayRoute && route["@type"] == "Relay" && id == "" {
			id = rid
		} else if route["name"] == relayRoute {
			others = append(others, rid)
		}
	}
	if err := c.destroy(ctx, "x:MtaRoute", others...); err != nil {
		return err
	}
	target := directRoute
	if r != nil {
		secret := object{"@type": "None"}
		if r.Username != "" {
			secret = object{"@type": "Value", "secret": r.Password}
		}
		route := object{
			"@type": "Relay", "name": relayRoute, "description": "The outbound relay of the panel",
			"host": r.Host, "port": r.Port, "protocol": "smtp", "implicitTls": r.Port == 465,
			"allowInvalidCerts": false, "authUsername": nullIfEmpty(r.Username), "authSecret": secret,
		}
		if id == "" {
			if _, err := c.create(ctx, "x:MtaRoute", route); err != nil {
				return err
			}
		} else if err := c.update(ctx, "x:MtaRoute", id, route); err != nil {
			return err
		}
		target = "'" + relayRoute + "'"
	}
	if err := c.outboundRoute(ctx, target); err != nil {
		return err
	}
	if r == nil && id != "" {
		// The strategy does not name the route now: the server lets it go.
		if err := c.destroy(ctx, "x:MtaRoute", id); err != nil {
			return err
		}
	}
	return c.reload(ctx, "ReloadSettings")
}

// outboundRoute sets the route of the outgoing mail. The rule for the local domains stays
// first, so that mail for a domain of this server never goes to the relay.
func (c *Client) outboundRoute(ctx context.Context, target string) error {
	current, err := c.get(ctx, "x:MtaOutboundStrategy", []string{singleton}, "route")
	if err != nil {
		return err
	}
	want := object{"match": object{"0": object{"if": "is_local_domain(rcpt_domain)", "then": "'local'"}}, "else": target}
	if len(current) == 1 && reflect.DeepEqual(current[0]["route"], want) {
		return nil
	}
	return c.update(ctx, "x:MtaOutboundStrategy", singleton, object{"route": want})
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (c *Client) listeners(ctx context.Context, p Ports) (changed bool, err error) {
	type listener struct {
		protocol string
		port     int
		implicit bool
	}
	want := map[string]listener{
		"smtp":        {"smtp", p.SMTP, false},
		"submissions": {"smtp", p.Submissions, true},
		"submission":  {"smtp", p.Submission, false},
		"imaps":       {"imap", p.IMAPS, true},
		// The management API. A new server in recovery mode has no listener.
		"http": {"http", p.HTTP, false},
	}
	current, err := c.get(ctx, "x:NetworkListener", nil, "name", "bind", "protocol", "implicitTls")
	if err != nil {
		return false, err
	}
	var remove []string
	for _, l := range current {
		name, _ := l["name"].(string)
		id, _ := l["id"].(string)
		w, ok := want[name]
		if !ok {
			remove = append(remove, id)
			continue
		}
		delete(want, name)
		bind := set("[::]:" + strconv.Itoa(w.port))
		if have, _ := l["bind"].(object); len(have) == 1 && have["[::]:"+strconv.Itoa(w.port)] == true && l["implicitTls"] == w.implicit {
			continue
		}
		if err := c.update(ctx, "x:NetworkListener", id, object{"bind": bind, "implicitTls": w.implicit}); err != nil {
			return false, err
		}
		changed = true
	}
	if err := c.destroy(ctx, "x:NetworkListener", remove...); err != nil {
		return false, err
	}
	names := make([]string, 0, len(want))
	for name := range want {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		w := want[name]
		if _, err := c.create(ctx, "x:NetworkListener", object{
			"name": name, "protocol": w.protocol, "bind": set("[::]:" + strconv.Itoa(w.port)), "implicitTls": w.implicit, "useTls": true,
		}); err != nil {
			return false, err
		}
		changed = true
	}
	return changed || len(remove) > 0, nil
}

// smtpStages are the settings with rules for "local_port == 25": the port on which other mail
// servers deliver mail. On that port, the server asks for no sign-in and checks the sender
// (SPF, DKIM and DMARC). On the other ports, a user must sign in.
var smtpStages = []string{"x:MtaStageAuth", "x:MtaStageConnect", "x:MtaStageData", "x:MtaStageEhlo", "x:MtaStageMail", "x:MtaStageRcpt", "x:SenderAuth", "x:MtaInboundSession", "x:MtaExtensions"}

// smtpPort changes each rule for port 25 to the SMTP port of the container.
func (c *Client) smtpPort(ctx context.Context, port int) error {
	if port == 25 {
		return nil
	}
	for _, kind := range smtpStages {
		list, err := c.get(ctx, kind, []string{singleton})
		if err != nil {
			return err
		}
		if len(list) != 1 {
			continue
		}
		changes := object{}
		for key, value := range list[0] {
			if next, changed := replacePort(value, strconv.Itoa(port)); changed {
				changes[key] = next
			}
		}
		if len(changes) == 0 {
			continue
		}
		if err := c.update(ctx, kind, singleton, changes); err != nil {
			return err
		}
	}
	return nil
}

// replacePort gives a JSON value with "local_port == 25" and "local_port != 25" changed to
// the port, in each text of the value.
func replacePort(v any, port string) (any, bool) {
	switch t := v.(type) {
	case string:
		next := t
		for _, op := range []string{"==", "!="} {
			next = strings.ReplaceAll(next, "local_port "+op+" 25", "local_port "+op+" "+port)
		}
		return next, next != t
	case object:
		out, changed := object{}, false
		for k, item := range t {
			next, c := replacePort(item, port)
			out[k] = next
			changed = changed || c
		}
		return out, changed
	case []any:
		out, changed := make([]any, len(t)), false
		for i, item := range t {
			next, c := replacePort(item, port)
			out[i] = next
			changed = changed || c
		}
		return out, changed
	}
	return v, false
}

func (c *Client) hostname(ctx context.Context, hostname string) error {
	id, err := c.domainID(ctx, hostname)
	if err != nil {
		return err
	}
	if id == "" {
		// The domain of the server itself: the sender of the reports of the server.
		if id, err = c.createDomain(ctx, hostname); err != nil {
			return fmt.Errorf("the mail server does not accept the host name %s: %w", hostname, err)
		}
	}
	return c.update(ctx, "x:SystemSettings", singleton, object{"defaultHostname": hostname, "defaultDomainId": id})
}

func (c *Client) certificate(ctx context.Context, certFile, keyFile string) error {
	want := object{
		"certificate": object{"@type": "File", "filePath": certFile},
		"privateKey":  object{"@type": "File", "filePath": keyFile},
	}
	current, err := c.get(ctx, "x:Certificate", nil, "certificate")
	if err != nil {
		return err
	}
	found := false
	var remove []string
	for _, cert := range current {
		id, _ := cert["id"].(string)
		source, _ := cert["certificate"].(object)
		if !found && source["@type"] == "File" && source["filePath"] == certFile {
			found = true
			continue
		}
		remove = append(remove, id)
	}
	if err := c.destroy(ctx, "x:Certificate", remove...); err != nil {
		return err
	}
	if found {
		return nil
	}
	_, err = c.create(ctx, "x:Certificate", want)
	return err
}

// ReloadCertificate makes the server read the certificate files again.
func (c *Client) ReloadCertificate(ctx context.Context) error {
	return c.reload(ctx, "ReloadTlsCertificates")
}

// logToStdout replaces the log files with a log on the standard output. The file system of
// the container is read-only, and the journal of the host keeps the output.
func (c *Client) logToStdout(ctx context.Context) error {
	tracers, err := c.get(ctx, "x:Tracer", nil, "enable")
	if err != nil {
		return err
	}
	found := false
	var remove []string
	for _, t := range tracers {
		id, _ := t["id"].(string)
		if t["@type"] == "Stdout" && !found {
			found = true
			continue
		}
		remove = append(remove, id)
	}
	if !found {
		if _, err := c.create(ctx, "x:Tracer", object{
			"@type": "Stdout", "enable": true, "level": "info", "ansi": false, "multiline": false,
			"buffered": false, "lossy": false, "eventsPolicy": "exclude", "events": object{},
		}); err != nil {
			return err
		}
	}
	return c.destroy(ctx, "x:Tracer", remove...)
}

// noDownloads stops the downloads of the server from the internet: the web interface and the
// lists of networks and countries. The firewall lets the mail server connect only to the DNS
// and to other mail servers, so the downloads can only fail.
func (c *Client) noDownloads(ctx context.Context) error {
	apps, err := c.get(ctx, "x:Application", nil, "enable")
	if err != nil {
		return err
	}
	for _, app := range apps {
		if id, _ := app["id"].(string); app["enable"] == true {
			if err := c.update(ctx, "x:Application", id, object{"enable": false}); err != nil {
				return err
			}
		}
	}
	asn, err := c.get(ctx, "x:Asn", []string{singleton})
	if err != nil || len(asn) != 1 || asn[0]["@type"] == "Disabled" {
		return err
	}
	return c.update(ctx, "x:Asn", singleton, object{"@type": "Disabled"})
}

func (c *Client) domainID(ctx context.Context, name string) (string, error) {
	ids, err := c.query(ctx, "x:Domain", object{"name": name})
	if err != nil || len(ids) == 0 {
		return "", err
	}
	return c.exact(ctx, "x:Domain", ids, object{"name": name})
}

// exact gives the ID of the one object in ids that has each value of want. The agent does not
// trust that a filter of the server is an exact comparison: a change of the wrong domain or
// the wrong mailbox would be a change for a different customer (adversarial review, finding
// G8-03). No object gives "". More than one object is an error.
func (c *Client) exact(ctx context.Context, kind string, ids []string, want object) (string, error) {
	fields := make([]string, 0, len(want))
	for k := range want {
		fields = append(fields, k)
	}
	found, err := c.get(ctx, kind, ids, fields...)
	if err != nil {
		return "", err
	}
	id := ""
next:
	for _, o := range found {
		for k, v := range want {
			if o[k] != v {
				continue next
			}
		}
		if id != "" {
			return "", fmt.Errorf("the mail server has more than one %s with %v", kind, want)
		}
		id, _ = o["id"].(string)
	}
	return id, nil
}

func (c *Client) createDomain(ctx context.Context, name string) (string, error) {
	id, err := c.create(ctx, "x:Domain", object{
		"name": name,
		// The edge proxy gets the certificate, and the customer adds the DNS records.
		"certificateManagement": object{"@type": "Manual"},
		"dnsManagement":         object{"@type": "Manual"},
		// The server makes the DKIM keys of the new domain.
		"dkimManagement": object{"@type": "Automatic"},
		// "name+tag@domain" is the mailbox "name@domain".
		"subAddressing": object{"@type": "Enabled"},
	})
	var exists *SetError
	if errors.As(err, &exists) && exists.Type == "primaryKeyViolation" && exists.Existing != "" {
		return exists.Existing, nil
	}
	return id, err
}

// SetDomain adds a mail domain, and gives the DNS records that its owner adds: MX, SPF, DKIM
// and DMARC. It gives the same records for a domain that the server has.
//
// The server makes the DKIM keys one time. The owner adds their DNS records by hand, so the
// agent turns off the automatic change of the keys: a new key would not be in the DNS.
func (c *Client) SetDomain(ctx context.Context, name string) ([]Record, error) {
	id, err := c.domainID(ctx, name)
	if err != nil {
		return nil, err
	}
	if id == "" {
		if id, err = c.createDomain(ctx, name); err != nil {
			return nil, err
		}
	}
	// The server makes the keys in a task, some moments after it makes the domain, and it does
	// not make all keys at the same moment. The records are complete when they have a DKIM
	// key and two more reads give the same records.
	deadline := time.Now().Add(time.Minute)
	var last []Record
	same := 0
	for {
		list, err := c.get(ctx, "x:Domain", []string{id}, "dnsZoneFile", "dkimManagement")
		if err != nil {
			return nil, err
		}
		if len(list) != 1 {
			return nil, fmt.Errorf("the mail server does not have the domain %s", name)
		}
		zone, _ := list[0]["dnsZoneFile"].(string)
		records := Records(zone, name)
		if hasPurpose(records, "dkim") && reflect.DeepEqual(records, last) {
			same++
		} else {
			same = 0
		}
		last = records
		if same >= 2 {
			if management, _ := list[0]["dkimManagement"].(object); management["@type"] != "Manual" {
				if err := c.update(ctx, "x:Domain", id, object{"dkimManagement": object{"@type": "Manual"}}); err != nil {
					return nil, err
				}
			}
			return records, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("the mail server made no DKIM key for the domain %s", name)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func hasPurpose(records []Record, purpose string) bool {
	for _, r := range records {
		if r.Purpose == purpose {
			return true
		}
	}
	return false
}

// DeleteDomain removes a mail domain with its DKIM keys. The domain must have no mailbox and
// no other address. A domain that the server does not have is not an error.
func (c *Client) DeleteDomain(ctx context.Context, name string) error {
	id, err := c.domainID(ctx, name)
	if err != nil || id == "" {
		return err
	}
	accounts, err := c.query(ctx, "x:Account", object{"domainId": id})
	if err != nil {
		return err
	}
	if len(accounts) > 0 {
		return fmt.Errorf("the domain %s has %d mailboxes on the mail server", name, len(accounts))
	}
	keys, err := c.get(ctx, "x:DkimSignature", nil, "domainId")
	if err != nil {
		return err
	}
	var remove []string
	for _, k := range keys {
		if k["domainId"] == id {
			if keyID, ok := k["id"].(string); ok {
				remove = append(remove, keyID)
			}
		}
	}
	if err := c.destroy(ctx, "x:DkimSignature", remove...); err != nil {
		return err
	}
	return c.destroy(ctx, "x:Domain", id)
}

// existingDomain gives the ID of a domain that the server must have.
func (c *Client) existingDomain(ctx context.Context, name string) (string, error) {
	id, err := c.domainID(ctx, name)
	if err == nil && id == "" {
		err = fmt.Errorf("the mail server does not have the domain %s", name)
	}
	return id, err
}

func (c *Client) accountID(ctx context.Context, domainID, localPart string) (string, error) {
	ids, err := c.query(ctx, "x:Account", object{"name": localPart, "domainId": domainID})
	if err != nil || len(ids) == 0 {
		return "", err
	}
	return c.exact(ctx, "x:Account", ids, object{"name": localPart, "domainId": domainID})
}

// SetMailbox makes a mailbox, or changes its size. passwordHash is a bcrypt hash: the agent
// never has the password. An empty hash keeps the password of a mailbox that exists.
func (c *Client) SetMailbox(ctx context.Context, domain, localPart string, quotaBytes int64, passwordHash string) error {
	domainID, err := c.existingDomain(ctx, domain)
	if err != nil {
		return err
	}
	id, err := c.accountID(ctx, domainID, localPart)
	if err != nil {
		return err
	}
	credentials := object{"0": object{"@type": "Password", "secret": passwordHash}}
	quotas := object{"maxDiskQuota": quotaBytes}
	if id == "" {
		if passwordHash == "" {
			return fmt.Errorf("the mail server does not have the mailbox %s@%s, and the job has no password for a new mailbox", localPart, domain)
		}
		_, err := c.create(ctx, "x:Account", object{"@type": "User", "name": localPart, "domainId": domainID, "credentials": credentials, "quotas": quotas})
		return err
	}
	changes := object{"quotas": quotas}
	if passwordHash != "" {
		changes["credentials"] = credentials
	}
	return c.update(ctx, "x:Account", id, changes)
}

// KeyDescription is the description of the public keys that the panel adds. The owner of a
// mailbox can add other keys in a mail program: the panel does not change those.
const KeyDescription = "CloudFactory"

// Key tells what the mail server found in an OpenPGP public key.
type Key struct {
	// The fingerprints of the keys in the block, in upper-case hexadecimal.
	Fingerprints []string `json:"fingerprints"`
	// For example "OpenPGP v4: RSA-3072, X25519".
	Type string `json:"type"`
	// RFC 3339, or empty for a key that does not expire.
	ExpiresAt string `json:"expiresAt"`
	// For example "expiresSoon", "weakRsa" or "addressMismatch".
	Warnings []string `json:"warnings"`
}

// ErrKey is the error of a public key that the mail server refused.
type ErrKey struct{ Reason string }

func (e *ErrKey) Error() string { return "the mail server refused the public key: " + e.Reason }

// SetEncryption turns on encryption at rest for a mailbox, with an OpenPGP public key in
// ASCII armour. The mail server then encrypts each message before it stores the message, and
// only the private key can decrypt it. Messages that the mailbox has do not change. An empty
// key turns off the encryption: new messages are then not encrypted.
func (c *Client) SetEncryption(ctx context.Context, domain, localPart, publicKey string) (*Key, error) {
	domainID, err := c.existingDomain(ctx, domain)
	if err != nil {
		return nil, err
	}
	id, err := c.accountID(ctx, domainID, localPart)
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, fmt.Errorf("the mail server does not have the mailbox %s@%s", localPart, domain)
	}
	// The keys of a mailbox are objects of its account, not of the administrator.
	listed, err := c.call(ctx, "x:PublicKey/get", object{"accountId": id, "properties": []string{"description"}})
	if err != nil {
		return nil, err
	}
	var old []string
	items, _ := listed["list"].([]any)
	for _, item := range items {
		if o, ok := item.(object); ok && o["description"] == KeyDescription {
			if keyID, ok := o["id"].(string); ok {
				old = append(old, keyID)
			}
		}
	}
	if publicKey == "" {
		if err := c.update(ctx, "x:Account", id, object{"encryptionAtRest": object{"@type": "Disabled"}}); err != nil {
			return nil, err
		}
		return nil, c.destroyKeys(ctx, id, old)
	}
	created, err := c.call(ctx, "x:PublicKey/set", object{"accountId": id, "create": object{"new": object{"key": publicKey, "description": KeyDescription}}})
	if err != nil {
		return nil, err
	}
	keyID := ""
	if made, ok := created["created"].(object); ok {
		if o, ok := made["new"].(object); ok {
			keyID, _ = o["id"].(string)
		}
	}
	if keyID == "" {
		failed, _ := created["notCreated"].(object)
		e := setError("x:PublicKey/set", failed["new"])
		if e.Type == "invalidProperties" {
			return nil, &ErrKey{Reason: e.Detail}
		}
		return nil, e
	}
	// encryptOnAppend: the server also encrypts the messages that a mail program stores, for
	// example the copies of sent mail.
	if err := c.update(ctx, "x:Account", id, object{"encryptionAtRest": object{"@type": "Enabled", "publicKey": keyID, "encryptOnAppend": true}}); err != nil {
		// Without this, each failed job leaves one more key in the account.
		_ = c.destroyKeys(ctx, id, []string{keyID})
		return nil, err
	}
	if err := c.destroyKeys(ctx, id, old); err != nil {
		return nil, err
	}
	got, err := c.call(ctx, "x:PublicKey/get", object{"accountId": id, "ids": []string{keyID}})
	if err != nil {
		return nil, err
	}
	key := &Key{}
	if list, _ := got["list"].([]any); len(list) == 1 {
		if o, ok := list[0].(object); ok {
			key.Type, _ = o["keyType"].(string)
			key.ExpiresAt, _ = o["expiresAt"].(string)
			key.Fingerprints = texts(o["fingerprints"])
			key.Warnings = texts(o["warnings"])
		}
	}
	return key, nil
}

func (c *Client) destroyKeys(ctx context.Context, accountID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	result, err := c.call(ctx, "x:PublicKey/set", object{"accountId": accountID, "destroy": ids})
	if err != nil {
		return err
	}
	failed, _ := result["notDestroyed"].(object)
	for _, id := range ids {
		if failed[id] != nil {
			if e := setError("x:PublicKey/set", failed[id]); e.Type != "notFound" {
				return e
			}
		}
	}
	return nil
}

// texts gives the texts of a JSON list. Stalwart writes some lists as a map of each value to
// true.
func texts(v any) []string {
	out := []string{}
	switch v := v.(type) {
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	case object:
		for s := range v {
			out = append(out, s)
		}
		sort.Strings(out)
	}
	return out
}

// DeleteMailbox removes a mailbox and its messages. A mailbox that the server does not have
// is not an error.
func (c *Client) DeleteMailbox(ctx context.Context, domain, localPart string) error {
	domainID, err := c.domainID(ctx, domain)
	if err != nil || domainID == "" {
		return err
	}
	id, err := c.accountID(ctx, domainID, localPart)
	if err != nil || id == "" {
		return err
	}
	return c.destroy(ctx, "x:Account", id)
}

// listID gives the ID of the list with this address. The server finds lists by text, so the
// function compares the address of each result.
func (c *Client) listID(ctx context.Context, domainID, localPart string) (string, error) {
	ids, err := c.query(ctx, "x:MailingList", object{"text": localPart})
	if err != nil || len(ids) == 0 {
		return "", err
	}
	lists, err := c.get(ctx, "x:MailingList", ids, "name", "domainId")
	if err != nil {
		return "", err
	}
	for _, l := range lists {
		if l["name"] == localPart && l["domainId"] == domainID {
			id, _ := l["id"].(string)
			return id, nil
		}
	}
	return "", nil
}

// SetAddress makes an address with no mailbox, or changes its destinations. The server sends
// each message for the address to all destinations: an alias has one, a forwarder a list.
func (c *Client) SetAddress(ctx context.Context, domain, localPart string, destinations []string) error {
	domainID, err := c.existingDomain(ctx, domain)
	if err != nil {
		return err
	}
	id, err := c.listID(ctx, domainID, localPart)
	if err != nil {
		return err
	}
	if id == "" {
		_, err := c.create(ctx, "x:MailingList", object{"name": localPart, "domainId": domainID, "recipients": set(destinations...)})
		return err
	}
	return c.update(ctx, "x:MailingList", id, object{"recipients": set(destinations...)})
}

// DeleteAddress removes an alias or a forwarder. An address that the server does not have is
// not an error.
func (c *Client) DeleteAddress(ctx context.Context, domain, localPart string) error {
	domainID, err := c.domainID(ctx, domain)
	if err != nil || domainID == "" {
		return err
	}
	id, err := c.listID(ctx, domainID, localPart)
	if err != nil || id == "" {
		return err
	}
	return c.destroy(ctx, "x:MailingList", id)
}

// Usage gives the size of the messages of each mailbox, by address.
func (c *Client) Usage(ctx context.Context) (map[string]int64, error) {
	ids, err := c.query(ctx, "x:Account", nil)
	if err != nil {
		return nil, err
	}
	out := map[string]int64{}
	for len(ids) > 0 {
		n := min(len(ids), 500)
		accounts, err := c.get(ctx, "x:Account", ids[:n], "emailAddress", "usedDiskQuota")
		if err != nil {
			return nil, err
		}
		for _, a := range accounts {
			address, _ := a["emailAddress"].(string)
			used, _ := a["usedDiskQuota"].(float64)
			if address != "" {
				out[address] = int64(used)
			}
		}
		ids = ids[n:]
	}
	return out, nil
}

// SetCluster makes the mail server one of the mail servers of a cluster: it sets the store for
// the locks and the counters that all mail servers share, and it tells the mail servers to
// send their notices to each other through that store. store is the setting of the store,
// for example the result of mailcluster.Cluster.InMemoryStore.
//
// The settings are in the database, so one call is sufficient for all mail servers of the
// cluster. It reports true when a setting changed: a mail server uses the settings after a
// restart.
func (c *Client) SetCluster(ctx context.Context, store map[string]any) (restart bool, err error) {
	current, err := c.get(ctx, "x:InMemoryStore", []string{singleton})
	if err != nil {
		return false, err
	}
	// The server adds its default values to the setting, so compare only the given values.
	same := len(current) == 1
	for key, value := range store {
		same = same && reflect.DeepEqual(jsonValue(current[0][key]), jsonValue(value))
	}
	if !same {
		// The store goes before the coordinator: the server refuses a coordinator while the
		// store is the local store of one server.
		if err := c.update(ctx, "x:InMemoryStore", singleton, store); err != nil {
			return false, err
		}
		restart = true
	}
	coordinator, err := c.get(ctx, "x:Coordinator", []string{singleton})
	if err != nil {
		return false, err
	}
	if len(coordinator) != 1 || coordinator[0]["@type"] != "Default" {
		// "Default" uses the connection to the store.
		if err := c.update(ctx, "x:Coordinator", singleton, object{"@type": "Default"}); err != nil {
			return false, err
		}
		restart = true
	}
	return restart, nil
}

// jsonValue gives a value as the JSON decoder gives it, to compare a value of the program
// with a value from the server.
func jsonValue(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

// Node is one mail server of a cluster, as the mail servers see each other.
type Node struct {
	Hostname string `json:"hostname"`
	// "active", "stale" (no sign of life for one hour) or "inactive".
	Status string `json:"status"`
}

// Nodes gives the mail servers that use the database of this mail server.
func (c *Client) Nodes(ctx context.Context) ([]Node, error) {
	list, err := c.get(ctx, "x:ClusterNode", nil)
	if err != nil {
		return nil, err
	}
	nodes := []Node{}
	for _, o := range list {
		n := Node{}
		n.Hostname, _ = o["hostname"].(string)
		n.Status, _ = o["status"].(string)
		nodes = append(nodes, n)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Hostname < nodes[j].Hostname })
	return nodes, nil
}
