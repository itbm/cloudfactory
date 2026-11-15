package stalwart

import "strings"

// Records gives the DNS records of a mail domain that its owner must add, from the zone text
// that the mail server gives for the domain. It keeps the MX record, the SPF record, the DKIM
// keys and the DMARC record. The other records of the zone text are for functions that the
// agent does not open, for example POP3 and CalDAV.
func Records(zone, domain string) []Record {
	var out []Record
	for _, line := range joinLines(zone) {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[1] != "IN" {
			continue
		}
		name := strings.ToLower(strings.TrimSuffix(fields[0], "."))
		kind := fields[2]
		rest := strings.TrimSpace(line[strings.Index(line, " "+kind+" ")+len(kind)+2:])
		switch {
		case kind == "MX" && name == domain:
			out = append(out, Record{Type: "MX", Name: name, Value: rest, Purpose: "mx"})
		case kind == "TXT":
			value := unquote(rest)
			purpose := ""
			switch {
			case name == domain && strings.HasPrefix(value, "v=spf1"):
				purpose = "spf"
			case strings.HasSuffix(name, "._domainkey."+domain):
				purpose = "dkim"
			case name == "_dmarc."+domain:
				purpose = "dmarc"
			}
			if purpose != "" {
				out = append(out, Record{Type: "TXT", Name: name, Value: value, Purpose: purpose})
			}
		}
	}
	return out
}

// joinLines gives the records of a zone text. A record in parentheses is on more than one line.
func joinLines(zone string) []string {
	var out []string
	open := ""
	for _, line := range strings.Split(zone, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case open != "" && line == ")":
			out = append(out, open)
			open = ""
		case open != "":
			open += " " + line
		case strings.HasSuffix(line, "("):
			open = strings.TrimSpace(strings.TrimSuffix(line, "("))
		case line != "":
			out = append(out, line)
		}
	}
	return out
}

// unquote joins the quoted parts of a TXT value: `"ab" "cd"` is "abcd".
func unquote(s string) string {
	var b strings.Builder
	in := false
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\\' && in && i+1 < len(s):
			i++
			b.WriteByte(s[i])
		case s[i] == '"':
			in = !in
		case in:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}
