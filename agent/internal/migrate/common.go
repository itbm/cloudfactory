package migrate

import (
	"bufio"
	"bytes"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// keyValues reads lines of "key=value". sep is the separator, for example "=" or ":".
func keyValues(b []byte, sep string) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, sep)
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out
}

// lines gives the lines of a file with no comments and no empty lines.
func lines(b []byte) []string {
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
	}
	return out
}

// yamlDoc is the small part of YAML that cPanel writes in its userdata files: values, maps of
// values and lists of values, one level deep.
type yamlDoc struct {
	values map[string]string
	maps   map[string]map[string]string
	lists  map[string][]string
}

func parseYAML(b []byte) yamlDoc {
	doc := yamlDoc{values: map[string]string{}, maps: map[string]map[string]string{}, lists: map[string][]string{}}
	var key string
	for _, raw := range strings.Split(string(b), "\n") {
		raw = strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || trimmed == "---" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indented := raw[0] == ' ' || raw[0] == '\t'
		if !indented {
			k, v, ok := strings.Cut(trimmed, ":")
			if !ok {
				key = ""
				continue
			}
			key = unquote(strings.TrimSpace(k))
			v = strings.TrimSpace(v)
			if v != "" && v != "{}" && v != "[]" {
				doc.values[key] = unquote(v)
			}
			continue
		}
		if key == "" {
			continue
		}
		if item, ok := strings.CutPrefix(trimmed, "- "); ok {
			doc.lists[key] = append(doc.lists[key], unquote(strings.TrimSpace(item)))
			continue
		}
		if k, v, ok := strings.Cut(trimmed, ":"); ok {
			if doc.maps[key] == nil {
				doc.maps[key] = map[string]string{}
			}
			doc.maps[key][unquote(strings.TrimSpace(k))] = unquote(strings.TrimSpace(v))
		}
	}
	return doc
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '\'' && s[len(s)-1] == '\'' || s[0] == '"' && s[len(s)-1] == '"') {
		return s[1 : len(s)-1]
	}
	return s
}

// domainName gives a domain in lower case, or "" when the shared rules refuse it.
func domainName(s string) string {
	s = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(s), "."))
	if validate.Domain(s) != nil {
		return ""
	}
	return s
}

// passwordHash gives a mail password hash that the mail server can check (the rule
// "mailPasswordHash"): bcrypt, SHA-512-crypt, SHA-256-crypt or MD5-crypt. Other hashes give "",
// and the import makes a new password.
func passwordHash(s string) string {
	s = strings.TrimSpace(s)
	for _, scheme := range []string{"{SHA512-CRYPT}", "{SHA256-CRYPT}", "{MD5-CRYPT}", "{BLF-CRYPT}", "{CRYPT}"} {
		s = strings.TrimPrefix(s, scheme)
	}
	if validate.MailPasswordHash(s) == nil {
		return s
	}
	return ""
}

// phpVersion gives the PHP version of a handler name, for example "8.1" for "ea-php81" and
// "7.4" for "alt-php74" or "php74".
var (
	phpHandler = regexp.MustCompile(`php(\d)(\d{1,2})$`)
	phpDotted  = regexp.MustCompile(`^\d\.\d{1,2}$`)
)

func phpVersion(handler string) string {
	h := strings.ToLower(strings.TrimSpace(handler))
	if m := phpHandler.FindStringSubmatch(h); m != nil {
		return m[1] + "." + m[2]
	}
	if phpDotted.MatchString(h) {
		return h
	}
	return ""
}

// cronEnv agrees with a line of a crontab that sets a variable, for example "MAILTO=x".
var cronEnv = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\s*=`)

// cronLine gives the schedule and the command of a crontab line. ok is false for a line
// that the panel cannot use, with the reason.
func cronLine(line string) (schedule, command string, ok bool, reason string) {
	if cronEnv.MatchString(line) {
		return "", "", false, ""
	}
	if strings.HasPrefix(line, "@") {
		return "", "", false, fmt.Sprintf("The panel has no schedule %q. Add the cron job again with five fields.", strings.Fields(line)[0])
	}
	f := strings.Fields(line)
	if len(f) < 6 {
		return "", "", false, "The line is not a cron job."
	}
	schedule = strings.Join(f[:5], " ")
	// The command keeps its own spaces.
	rest := line
	for range 5 {
		rest = strings.TrimLeft(rest, " \t")
		i := strings.IndexAny(rest, " \t")
		rest = rest[i:]
	}
	command = strings.TrimSpace(rest)
	if validate.CronSchedule(schedule) != nil {
		return "", "", false, fmt.Sprintf("The panel cannot use the schedule %q.", schedule)
	}
	if validate.CommandLine(command) != nil {
		return "", "", false, "The panel cannot use the command of this cron job."
	}
	return schedule, command, true, ""
}

// grant is a line of "GRANT ... ON ... TO ..." of a MySQL dump of grants.
var grantLine = regexp.MustCompile("(?i)^GRANT\\s+(.+?)\\s+ON\\s+(\\S+)\\s+TO\\s+'([^']+)'@'([^']*)'")

// mysqlGrants reads the grants of the database users: the privileges of each user on each
// database. The names of a database in a grant can have "\_" for "_".
func mysqlGrants(b []byte) map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, line := range lines(b) {
		m := grantLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		privileges, on, user, host := strings.ToUpper(m[1]), m[2], m[3], m[4]
		if host != "localhost" && host != "127.0.0.1" && host != "%" {
			continue
		}
		db, _, _ := strings.Cut(on, ".")
		db = strings.ReplaceAll(strings.Trim(db, "`"), `\_`, "_")
		db = strings.ReplaceAll(db, `\%`, "%")
		if db == "*" || strings.ContainsAny(db, "%*") {
			if out[user] == nil {
				out[user] = map[string]string{}
			}
			continue
		}
		if out[user] == nil {
			out[user] = map[string]string{}
		}
		access := "readonly"
		if strings.Contains(privileges, "ALL") || strings.Contains(privileges, "INSERT") || strings.Contains(privileges, "UPDATE") || strings.Contains(privileges, "DELETE") {
			access = "readwrite"
		}
		if out[user][db] != "readwrite" {
			out[user][db] = access
		}
	}
	return out
}

// wpConfigName agrees with the database name in wp-config.php.
var wpConfigName = regexp.MustCompile(`define\s*\(\s*['"]DB_NAME['"]\s*,\s*['"]([^'"]+)['"]\s*\)`)

// wordpressDatabase gives the database name of a wp-config.php file, or "".
func wordpressDatabase(b []byte) string {
	if m := wpConfigName.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return ""
}

// aliasesFile reads a file of forwarders: lines of "address: destination, destination". It
// gives the local part of each address in the domain, and its destinations. A destination
// that is a program, a file or an error is not a mail address: the import leaves it out.
func aliasesFile(b []byte, domain string, sep string) (map[string][]string, []string) {
	out := map[string][]string{}
	var skipped []string
	for _, line := range lines(b) {
		k, v, ok := strings.Cut(line, sep)
		if !ok {
			continue
		}
		local := strings.ToLower(strings.TrimSpace(k))
		local = strings.TrimSuffix(local, "@"+domain)
		if local == "*" || local == "" || strings.Contains(local, "@") {
			// The default catch-all of cPanel refuses the mail: it is not a forwarder.
			if local == "*" && !strings.Contains(v, ":fail:") && !strings.Contains(v, ":blackhole:") {
				skipped = append(skipped, "*@"+domain)
			}
			continue
		}
		var dests []string
		for _, d := range strings.Split(v, ",") {
			d = strings.ToLower(strings.Trim(strings.TrimSpace(d), `"`))
			if d == "" {
				continue
			}
			if !strings.Contains(d, "@") && validate.MailLocalPart(d) == nil {
				d += "@" + domain
			}
			if validate.MailAddress(d) != nil {
				skipped = append(skipped, local+"@"+domain)
				continue
			}
			if !slices.Contains(dests, d) {
				dests = append(dests, d)
			}
		}
		if len(dests) > 0 {
			out[local] = dests
		}
	}
	return out, skipped
}

// validLocal reports whether the panel can use a local part of a mail address.
func validLocal(local string) bool { return validate.MailLocalPart(local) == nil }

// aliasKind is "alias" when each destination is in the domain, else "forwarder".
func aliasKind(domain string, dests []string) string {
	for _, d := range dests {
		if !strings.HasSuffix(d, "@"+domain) {
			return "forwarder"
		}
	}
	return "alias"
}

// sortedKeys gives the keys of a map in order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// maildir records the size and the number of messages of each mailbox. A mailbox is a
// Maildir: a directory with the folders "cur", "new" and "tmp", and sub-folders that start
// with ".".
type maildirs struct {
	bytes    map[string]int64
	messages map[string]int
}

func newMaildirs() *maildirs {
	return &maildirs{bytes: map[string]int64{}, messages: map[string]int{}}
}

// add records a file of the mailbox box. rel is the path of the file in the mailbox.
func (m *maildirs) add(box, rel string, e Entry) {
	if e.Type != TypeFile {
		return
	}
	m.bytes[box] += e.Size
	if _, ok := MessageOf(rel); ok {
		m.messages[box]++
	}
}

// MessageOf reports whether a path in a Maildir is a message, and gives its folder: "" for the
// inbox, or the name of the folder with "." between the levels (Maildir++), for example
// "Sent" or "Archive.2024".
func MessageOf(rel string) (folder string, ok bool) {
	parts := strings.Split(rel, "/")
	switch {
	case len(parts) == 2 && (parts[0] == "cur" || parts[0] == "new"):
		return "", parts[1] != ""
	case len(parts) == 3 && strings.HasPrefix(parts[0], ".") && len(parts[0]) > 1 && (parts[1] == "cur" || parts[1] == "new"):
		return parts[0][1:], parts[2] != ""
	}
	return "", false
}

// sizeOf parses a number of bytes, or gives 0.
func sizeOf(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// urlValues reads "a=1&b=2" with URL encoding.
func urlValues(s string) url.Values {
	v, err := url.ParseQuery(s)
	if err != nil {
		return url.Values{}
	}
	return v
}

// addDomain adds a domain to a website when no website has it.
func addDomain(w *Website, seen map[string]bool, name, kind string) {
	if name == "" || seen[name] {
		return
	}
	seen[name] = true
	w.Domains = append(w.Domains, Domain{Name: name, Kind: kind})
}

// databaseOf gives the engine of a database user grant set as a list of grants.
func grantsOf(dbs map[string]string, known map[string]bool) []Grant {
	var out []Grant
	for _, db := range sortedKeys(dbs) {
		if known[db] {
			out = append(out, Grant{Database: db, Privileges: dbs[db]})
		}
	}
	return out
}
