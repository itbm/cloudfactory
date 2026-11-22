package migrate

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// hestia reads a HestiaCP user backup, for example "USER.2026-11-22_05-00-00.tar". A VestaCP
// backup has the same layout, with "vesta" in place of "hestia". The parts that the import
// uses:
//
//	hestia/user.conf                 the name and the email of the user (NAME, CONTACT)
//	pam/passwd                       the name of the Unix user
//	web/DOMAIN/hestia/web.conf       a web domain: ALIAS, CUSTOM_DOCROOT, BACKEND, FTP_USER
//	web/DOMAIN/domain_data.tar.zst   the files of the web domain, with public_html (or .tar.gz)
//	db/DB/hestia/db.conf             a database: DB, DBUSER, TYPE (mysql or pgsql)
//	db/DB/DB.mysql.sql.zst           the dump of the database (or .sql.gz)
//	mail/DOMAIN/hestia/mail.conf     a mail domain: CATCHALL
//	mail/DOMAIN/hestia/DOMAIN.conf   the mailboxes: ACCOUNT, ALIAS, FWD, FWD_ONLY, MD5, QUOTA
//	mail/DOMAIN/accounts.tar.zst     the Maildir of each mailbox: ACCOUNT/cur, ACCOUNT/.Sent/cur
//	dns/DOMAIN/conf/DOMAIN.db        the DNS zone (else the records in hestia/DOMAIN.conf)
//	cron/cron.conf                   the cron jobs: MIN, HOUR, DAY, MONTH, WDAY, CMD
//
// The backup also has the other files of the home directory (user_dir/NAME.tar.zst), which the
// import does not copy.
//
// Each settings file has one record on each line, as KEY='value' pairs.
type hestia struct {
	files map[string][]byte
	// order is the web domains in the order of the archive.
	order []string
	// data is the archive of the files of each web domain.
	data map[string]string
	// roots are the directories of the nested archives of the files that have a member.
	roots map[string]bool
	sizes *sizes
	mail  *maildirs
	// boxes is the archive of the Maildirs of each mail domain.
	boxes map[string]string
	dumps map[string]Content
}

func newHestia() *hestia {
	return &hestia{files: map[string][]byte{}, data: map[string]string{}, roots: map[string]bool{}, sizes: newSizes("", hestiaDepth), mail: newMaildirs(), boxes: map[string]string{}, dumps: map[string]Content{}}
}

func (h *hestia) name() string { return "hestia" }

// hestiaDepth is the number of directory levels that the adapter records: "web/DOMAIN/
// domain_data.tar.zst/public_html" and three levels in it.
const hestiaDepth = 7

// hestiaConf agrees with the directory of the settings: "hestia", or "vesta" in a VestaCP
// backup.
func hestiaConf(s string) bool { return s == "hestia" || s == "vesta" }

// hestiaData agrees with the archive of the files of a web domain, and with the archive of
// the mailboxes of a mail domain.
var (
	hestiaData     = regexp.MustCompile(`^web/([^/]+)/domain_data\.tar\.(?:zst|gz)$`)
	hestiaAccounts = regexp.MustCompile(`^mail/([^/]+)/accounts\.tar\.(?:zst|gz)$`)
	hestiaDump     = regexp.MustCompile(`^db/([^/]+)/([^/]+)\.(mysql|pgsql)\.sql(?:\.zst|\.gz)?$`)
)

// hestiaContentArchive reports whether a member is a nested archive that an inspection reads.
func hestiaContentArchive(name string) bool {
	return hestiaData.MatchString(name) || hestiaAccounts.MatchString(name)
}

func (h *hestia) wants(e Entry) bool {
	if e.Type != TypeFile {
		return false
	}
	if archive, rel, ok := nestedOf(e.Name); ok {
		return hestiaData.MatchString(archive) && strings.HasPrefix(rel, "public_html/") && path.Base(rel) == "wp-config.php" && strings.Count(rel, "/") <= 4 && e.Size <= 256<<10
	}
	parts := strings.Split(e.Name, "/")
	switch len(parts) {
	case 2:
		return hestiaConf(parts[0]) && parts[1] == "user.conf" || e.Name == "pam/passwd" || e.Name == "cron/cron.conf"
	case 4:
		switch parts[0] {
		case "web", "db", "mail", "dns":
			if hestiaConf(parts[2]) {
				return strings.HasSuffix(parts[3], ".conf")
			}
			return parts[0] == "dns" && parts[2] == "conf" && parts[3] == parts[1]+".db"
		}
	}
	return false
}

func (h *hestia) add(e Entry, content []byte) {
	if content != nil {
		h.files[e.Name] = content
	}
	if archive, rel, ok := nestedOf(e.Name); ok {
		if m := hestiaData.FindStringSubmatch(archive); m != nil {
			h.data[m[1]] = archive
			h.sizes.add(e)
			if top, _, _ := strings.Cut(rel, "/"); top != "" {
				h.roots[archive+"/"+top] = true
			}
			return
		}
		if m := hestiaAccounts.FindStringSubmatch(archive); m != nil && e.Type == TypeFile {
			h.boxes[m[1]] = archive
			// A message of a Maildir: BOX/cur/FILE, BOX/new/FILE or BOX/.FOLDER/cur/FILE.
			parts := strings.Split(rel, "/")
			if n := len(parts); n >= 3 && (parts[n-2] == "cur" || parts[n-2] == "new") {
				box := n - 2
				if box > 1 && strings.HasPrefix(parts[box-1], ".") && len(parts[box-1]) > 1 {
					box--
				}
				h.mail.add(archive+"/"+strings.Join(parts[:box], "/"), strings.Join(parts[box:], "/"), e)
			}
		}
		return
	}
	parts := strings.Split(e.Name, "/")
	if len(parts) == 4 && parts[0] == "web" && hestiaConf(parts[2]) && parts[3] == "web.conf" && content != nil && !slices.Contains(h.order, parts[1]) {
		h.order = append(h.order, parts[1])
	}
	if m := hestiaDump.FindStringSubmatch(e.Name); m != nil && e.Type == TypeFile && m[1] == m[2] {
		h.dumps[m[1]] = Content{Path: e.Name, Bytes: e.Size}
	}
}

// conf gives the directory of the settings, or "" for no HestiaCP backup.
func (h *hestia) conf() string {
	for _, c := range []string{"hestia", "vesta"} {
		if h.files[c+"/user.conf"] != nil {
			return c
		}
	}
	return ""
}

func (h *hestia) detected() bool { return h.conf() != "" }

// hestiaRecord agrees with one KEY='value' pair of a settings file.
var hestiaRecord = regexp.MustCompile(`([A-Z][A-Z0-9_]*)='([^']*)'`)

// hestiaRecords reads the records of a settings file, one on each line.
func hestiaRecords(b []byte) []map[string]string {
	var out []map[string]string
	for _, line := range lines(b) {
		r := map[string]string{}
		for _, m := range hestiaRecord.FindAllStringSubmatch(line, -1) {
			r[m[1]] = m[2]
		}
		if len(r) > 0 {
			out = append(out, r)
		}
	}
	return out
}

// hestiaValues reads a settings file with one record, or one KEY='value' pair on each line.
func hestiaValues(b []byte) map[string]string {
	out := map[string]string{}
	for _, r := range hestiaRecords(b) {
		for k, v := range r {
			out[k] = v
		}
	}
	return out
}

// hestiaList reads a list of values with "," between them.
func hestiaList(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.ToLower(strings.TrimSpace(v)); v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

// hestiaText gives the text of a value: HestiaCP keeps "'" as "%quote%" and ":" as "%dots%".
func hestiaText(s string) string {
	return strings.NewReplacer("%quote%", "'", "%dots%", ":").Replace(s)
}

// hestiaBackend gives the PHP version of a PHP-FPM template, for example "8.2" for "PHP-8_2".
var hestiaBackend = regexp.MustCompile(`(?i)^php-(\d)_(\d{1,2})$`)

// hestiaDocroot agrees with a custom document root: the public_html directory of a web domain,
// and a directory in it.
var hestiaDocroot = regexp.MustCompile(`/web/([^/]+)/public_html(?:/(.*))?$`)

// hestiaSite is a web domain of the backup.
type hestiaSite struct {
	name    string
	aliases []string
	php     string
	// owner is the web domain of the files, and dir the directory in its public_html: a
	// custom document root can be in a different web domain.
	owner, dir string
}

func (h *hestia) account() (Account, error) {
	var acc Account
	warn := func(item, format string, args ...any) {
		acc.Warnings = append(acc.Warnings, Warning{Item: item, Message: fmt.Sprintf(format, args...)})
	}
	conf := h.conf()
	user := hestiaValues(h.files[conf+"/user.conf"])
	account, _, _ := strings.Cut(string(h.files["pam/passwd"]), ":")
	acc.Source = Source{Account: strings.TrimSpace(account)}
	name := strings.TrimSpace(firstNonEmpty(user["NAME"], strings.TrimSpace(user["FNAME"]+" "+user["LNAME"]), acc.Source.Account))
	acc.Customer = &Customer{Name: name, Email: user["CONTACT"]}

	// The web domains. A web domain with a custom document root in the public_html directory
	// of a different web domain uses the files of that web domain.
	var sites []hestiaSite
	ftp := false
	for _, dir := range h.order {
		rec := hestiaValues(h.files["web/"+dir+"/"+conf+"/web.conf"])
		name := domainName(firstNonEmpty(rec["DOMAIN"], dir))
		if name == "" || name != strings.ToLower(dir) {
			warn("website:"+dir, "The panel cannot use this domain name. The import does not copy the website.")
			continue
		}
		s := hestiaSite{name: name, owner: name, dir: "."}
		for _, a := range hestiaList(rec["ALIAS"]) {
			if a = domainName(a); a != "" && a != name {
				s.aliases = append(s.aliases, a)
			}
		}
		if m := hestiaBackend.FindStringSubmatch(strings.TrimSpace(rec["BACKEND"])); m != nil {
			s.php = m[1] + "." + m[2]
		}
		if m := hestiaDocroot.FindStringSubmatch(strings.TrimRight(rec["CUSTOM_DOCROOT"], "/")); m != nil {
			if owner := domainName(m[1]); owner != "" {
				s.owner = owner
			}
			if d, ok := CleanName(m[2]); ok && validate.DocumentRoot(d) == nil {
				s.dir = d
			}
		}
		if rec["FTP_USER"] != "" {
			ftp = true
		}
		if r := strings.TrimSpace(rec["REDIRECT"]); r != "" {
			warn("website:"+name, "The domain redirects to %s on the old panel. Add the redirect again after the import.", r)
		}
		if rec["SSL_HOME"] == "single" {
			warn("website:"+name, "The domain has a separate public_shtml directory for HTTPS. The import copies public_html only.")
		}
		sites = append(sites, s)
	}

	// A web domain that shows the public_html directory of a different web domain comes last,
	// so that it finds the website of that web domain.
	alias := func(s hestiaSite) bool { return s.owner != s.name && s.dir == "." }
	slices.SortStableFunc(sites, func(a, b hestiaSite) int { return compareBool(alias(a), alias(b)) })
	seen := map[string]bool{}
	index := map[string]int{}
	for _, s := range sites {
		if seen[s.name] {
			continue
		}
		// A web domain that shows the public_html directory of a different web domain is an
		// alias of that website.
		if alias(s) {
			if i, ok := index[s.owner]; ok {
				addDomain(&acc.Websites[i], seen, s.name, "alias")
				for _, a := range s.aliases {
					addDomain(&acc.Websites[i], seen, a, "alias")
				}
				continue
			}
		}
		w := Website{Name: s.name, Kind: "public", Runtime: Runtime{Family: "php", PHPVersion: s.php, DocumentRoot: "."}}
		addDomain(&w, seen, s.name, "primary")
		for _, a := range s.aliases {
			addDomain(&w, seen, a, "alias")
		}
		files := ""
		if archive, ok := h.data[s.owner]; ok && h.roots[archive+"/public_html"] {
			files = archive + "/public_html"
			switch {
			case s.dir == ".":
			case s.owner != s.name:
				files += "/" + s.dir
			default:
				w.Runtime.DocumentRoot = s.dir
			}
		}
		if files == "" {
			warn("website:"+s.name, "The backup has no files of this website. The import makes the website with no files.")
		} else {
			bytes, _ := h.sizes.of(files)
			w.Content = &WebsiteContent{Files: &Content{Path: files, Bytes: bytes}}
			w.DiskBytes = bytes
			for _, f := range []string{"wp-config.php", path.Join(w.Runtime.DocumentRoot, "wp-config.php")} {
				if db := wordpressDatabase(h.files[files+"/"+f]); db != "" {
					w.AppConfig = &AppConfig{Kind: "wordpress", File: f, Database: db}
					break
				}
			}
		}
		index[s.name] = len(acc.Websites)
		acc.Websites = append(acc.Websites, w)
	}
	// A website with its document root in a directory of a different website has the files of
	// that directory only.
	for _, s := range sites {
		i, ok := index[s.owner]
		if !ok || s.owner == s.name || s.dir == "." || acc.Websites[i].Content == nil {
			continue
		}
		c := acc.Websites[i].Content.Files
		if !slices.Contains(c.Exclude, s.dir) {
			c.Exclude = append(c.Exclude, s.dir)
			b, _ := h.sizes.of(c.Path + "/" + s.dir)
			c.Bytes = max(c.Bytes-b, 0)
			acc.Websites[i].DiskBytes = c.Bytes
		}
	}

	// The databases. A website that names a database in its wp-config.php gets it. The first
	// website gets the others.
	for _, db := range h.dirsOf("db") {
		rec := hestiaValues(h.files["db/"+db+"/"+conf+"/db.conf"])
		name := firstNonEmpty(rec["DB"], db)
		if rec["TYPE"] == "pgsql" {
			warn("database:"+name, "The import does not copy PostgreSQL databases yet. Make the database in the panel, and restore its dump.")
			continue
		}
		if len(acc.Websites) == 0 {
			warn("database:"+name, "The account has no website for this database. The import does not copy it.")
			continue
		}
		owner := 0
		for i, w := range acc.Websites {
			if w.AppConfig != nil && w.AppConfig.Database == name {
				owner = i
				break
			}
		}
		w := &acc.Websites[owner]
		d := Database{Engine: "mariadb", Name: name}
		if c, ok := h.dumps[db]; ok {
			d.Content = &c
		} else {
			warn("database:"+name, "The backup has no dump of this database. The import makes an empty database.")
		}
		w.Databases = append(w.Databases, d)
		if u := rec["DBUSER"]; u != "" {
			w.DatabaseUsers = append(w.DatabaseUsers, DatabaseUser{Engine: "mariadb", Name: u, Access: "website", Grants: []Grant{{Database: name, Privileges: "readwrite"}}})
		}
	}

	// The cron jobs go to the first website.
	var cron []CronJob
	for _, r := range hestiaRecords(h.files["cron/cron.conf"]) {
		line := strings.Join([]string{r["MIN"], r["HOUR"], r["DAY"], r["MONTH"], r["WDAY"], hestiaText(r["CMD"])}, " ")
		schedule, command, ok, reason := cronLine(line)
		if !ok {
			if reason != "" {
				warn("cron", "%s Line: %s", reason, short(line))
			}
			continue
		}
		cron = append(cron, CronJob{Schedule: schedule, Command: command})
	}
	if len(cron) > 0 {
		if len(acc.Websites) == 0 {
			warn("cron", "The account has no website for its %d cron jobs. The import does not copy them.", len(cron))
		} else {
			acc.Websites[0].CronJobs = cron
		}
	}

	// The mail domains.
	for _, dir := range h.dirsOf("mail") {
		domain := domainName(dir)
		if domain == "" || domain != strings.ToLower(dir) {
			warn("mail:"+dir, "The panel cannot use this domain name. The import does not copy its mail.")
			continue
		}
		md := MailDomain{Name: domain}
		h.mailOf(&md, dir, conf, warn)
		if len(md.Mailboxes) > 0 || len(md.Aliases) > 0 {
			acc.Mail.Domains = append(acc.Mail.Domains, md)
		}
	}

	// The DNS zones.
	for _, dir := range h.dirsOf("dns") {
		zone := domainName(dir)
		if zone == "" {
			warn("dns:"+dir, "The panel cannot use this zone name. The import does not copy the zone.")
			continue
		}
		text := string(h.files["dns/"+dir+"/conf/"+dir+".db"])
		if strings.TrimSpace(text) == "" {
			ttl := hestiaValues(h.files["dns/"+dir+"/"+conf+"/dns.conf"])["TTL"]
			text = hestiaZone(hestiaRecords(h.files["dns/"+dir+"/"+conf+"/"+dir+".conf"]), ttl)
		}
		if strings.TrimSpace(text) != "" {
			acc.DNSZones = append(acc.DNSZones, Zone{Name: zone, ZoneFile: text})
		}
	}

	warn("files", "The import copies the public_html directory of each website. Copy other files of the home directory with SFTP.")
	if ftp {
		warn("ftp", "The panel has no FTP accounts. Use SFTP with an SSH key in place of FTP.")
	}
	warn("certificates", "The import does not copy the TLS certificates. The panel gets a certificate for each domain when its DNS points to the new server.")
	return acc, nil
}

// dirsOf gives the directories in a part of the backup ("web", "db", "mail" or "dns") that
// have a settings file, in order.
func (h *hestia) dirsOf(part string) []string {
	var out []string
	for name := range h.files {
		parts := strings.Split(name, "/")
		if len(parts) == 4 && parts[0] == part && hestiaConf(parts[2]) && !slices.Contains(out, parts[1]) {
			out = append(out, parts[1])
		}
	}
	slices.Sort(out)
	return out
}

// mailOf adds the mailboxes and the forwarders of a mail domain.
func (h *hestia) mailOf(md *MailDomain, dir, conf string, warn func(string, string, ...any)) {
	domain := md.Name
	addAlias := func(local string, dests []string) {
		for i := range md.Aliases {
			if md.Aliases[i].LocalPart == local {
				for _, d := range dests {
					if !slices.Contains(md.Aliases[i].Destinations, d) {
						md.Aliases[i].Destinations = append(md.Aliases[i].Destinations, d)
					}
				}
				md.Aliases[i].Kind = aliasKind(domain, md.Aliases[i].Destinations)
				return
			}
		}
		md.Aliases = append(md.Aliases, Alias{LocalPart: local, Kind: aliasKind(domain, dests), Destinations: dests})
	}
	newPasswords := 0
	records := hestiaRecords(h.files["mail/"+dir+"/"+conf+"/"+dir+".conf"])
	locals := map[string]bool{}
	for _, r := range records {
		locals[strings.ToLower(r["ACCOUNT"])] = true
	}
	// aliases are the other addresses of each mailbox: forwarders to it.
	type alias struct{ local, dest string }
	var aliases []alias
	for _, r := range records {
		local := strings.ToLower(r["ACCOUNT"])
		if !validLocal(local) {
			warn("mail:"+local+"@"+domain, "The panel cannot use this mailbox name.")
			continue
		}
		var dests []string
		for _, d := range hestiaList(r["FWD"]) {
			if !strings.Contains(d, "@") {
				d += "@" + domain
			}
			if validate.MailAddress(d) == nil && !slices.Contains(dests, d) {
				dests = append(dests, d)
			}
		}
		root := ""
		if archive, ok := h.boxes[dir]; ok {
			for _, p := range []string{archive + "/" + r["ACCOUNT"], archive + "/" + r["ACCOUNT"] + "/Maildir"} {
				if _, ok := h.mail.bytes[p]; ok {
					root = p
					break
				}
			}
		}
		if r["FWD_ONLY"] == "yes" && len(dests) > 0 {
			addAlias(local, dests)
			if n := h.mail.messages[root]; n > 0 {
				warn("mail:"+local+"@"+domain, "The address only forwards mail on the old panel. The import makes a forwarder, and does not copy the %d messages of its mailbox.", n)
			}
		} else {
			box := Mailbox{LocalPart: local, PasswordHash: passwordHash(r["MD5"])}
			if q, err := strconv.ParseInt(strings.TrimSpace(r["QUOTA"]), 10, 64); err == nil && q > 0 {
				box.QuotaBytes = q << 20
			}
			if box.PasswordHash == "" {
				newPasswords++
			}
			if root != "" {
				box.Content = &Content{Path: root, Bytes: h.mail.bytes[root]}
				box.Messages = h.mail.messages[root]
			}
			md.Mailboxes = append(md.Mailboxes, box)
			if len(dests) > 0 {
				warn("mail:"+local+"@"+domain, "The address is a mailbox and forwards mail on the old panel. The import copies the mailbox only.")
			}
		}
		for _, a := range hestiaList(r["ALIAS"]) {
			a = strings.TrimSuffix(a, "@"+domain)
			if validLocal(a) && a != local {
				aliases = append(aliases, alias{a, local + "@" + domain})
			}
		}
		if r["AUTOREPLY"] == "yes" {
			warn("mail:"+local+"@"+domain, "The import does not copy autoresponders.")
		}
	}
	// An alias of a mailbox is a forwarder to it, unless the address is a mailbox too.
	for _, a := range aliases {
		if !locals[a.local] {
			addAlias(a.local, []string{a.dest})
		}
	}
	if newPasswords > 0 {
		warn("mail:"+domain, "%d mailboxes of %s have a password hash that the mail server cannot use. The import gives these mailboxes a new password.", newPasswords, domain)
	}
	if ca := strings.TrimSpace(hestiaValues(h.files["mail/"+dir+"/"+conf+"/mail.conf"])["CATCHALL"]); ca != "" {
		warn("mail:*@"+domain, "The import does not copy the catch-all address (%s).", ca)
	}
}

// hestiaZone gives a zone file of the records of a DNS domain, as HestiaCP writes it.
func hestiaZone(records []map[string]string, ttl string) string {
	var b strings.Builder
	if isDigits(ttl) {
		fmt.Fprintf(&b, "$TTL %s\n", ttl)
	}
	for _, r := range records {
		if r["SUSPENDED"] == "yes" || r["RECORD"] == "" || r["TYPE"] == "" {
			continue
		}
		value := hestiaText(r["VALUE"])
		if strings.EqualFold(r["TYPE"], "TXT") && !strings.HasPrefix(value, `"`) {
			value = `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
		}
		fields := []string{r["RECORD"]}
		if isDigits(r["TTL"]) {
			fields = append(fields, r["TTL"])
		}
		fields = append(fields, "IN", strings.ToUpper(r["TYPE"]))
		if p := strings.TrimSpace(r["PRIORITY"]); p != "" {
			fields = append(fields, p)
		}
		fields = append(fields, value)
		b.WriteString(strings.Join(fields, "\t") + "\n")
	}
	return b.String()
}

// compareBool orders false before true.
func compareBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case !a:
		return -1
	}
	return 1
}
