package migrate

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

// directAdmin reads a DirectAdmin user backup, for example "user.admin.USER.tar.gz" or
// "user.admin.USER.tar.zst". The parts that the import uses:
//
//	backup/user.conf                  username, domain (the main domain), email
//	backup/DOMAIN/domain.conf         a domain of the account
//	backup/DOMAIN/domain.pointers     the domain aliases and pointers: "name=type=alias"
//	backup/DOMAIN/subdomain.list      the subdomains, one name on each line
//	backup/DOMAIN/DOMAIN.db           the DNS zone
//	backup/DOMAIN/email/passwd        the mailboxes, with the password hashes
//	backup/DOMAIN/email/quota         the size limit of each mailbox
//	backup/DOMAIN/email/aliases       the forwarders: "name:destination,destination"
//	backup/DB.conf, backup/DB.sql     the database users and the dump of each database
//	backup/crontab.conf               the cron jobs: "N=minute hour day month weekday command"
//	domains/DOMAIN/public_html/       the files; a subdomain is a directory in it
//	imap/DOMAIN/BOX/Maildir/          the Maildir of each mailbox
type directAdmin struct {
	prefix    string
	prefixSet bool
	files     map[string][]byte
	sizes     *sizes
	mail      *maildirs
	dumps     map[string]Content
	other     map[string]bool
}

func newDirectAdmin() *directAdmin {
	return &directAdmin{files: map[string][]byte{}, mail: newMaildirs(), dumps: map[string]Content{}, other: map[string]bool{}, sizes: newSizes("", 7)}
}

func (d *directAdmin) name() string { return "directadmin" }

// daTop are the directories at the top of a DirectAdmin backup.
var daTop = map[string]bool{"backup": true, "domains": true, "imap": true, "Maildir": true}

func (d *directAdmin) rel(e Entry) (string, bool) {
	if !d.prefixSet {
		first, _, _ := strings.Cut(e.Name, "/")
		if !daTop[first] && strings.Contains(e.Name, "/") {
			d.prefix = first
		}
		d.prefixSet = true
	}
	if d.prefix == "" {
		return e.Name, true
	}
	if !Within(e.Name, d.prefix) || e.Name == d.prefix {
		return "", false
	}
	return Rel(e.Name, d.prefix), true
}

// full gives the path in the archive of a path in the account.
func (d *directAdmin) full(rel string) string {
	if d.prefix == "" {
		return rel
	}
	return d.prefix + "/" + rel
}

func (d *directAdmin) wants(e Entry) bool {
	rel, ok := d.rel(e)
	if !ok || e.Type != TypeFile {
		return false
	}
	parts := strings.Split(rel, "/")
	base := parts[len(parts)-1]
	switch {
	case len(parts) == 2 && parts[0] == "backup":
		return strings.HasSuffix(base, ".conf")
	case len(parts) == 3 && parts[0] == "backup":
		return base == "domain.conf" || base == "domain.pointers" || base == "subdomain.list" || strings.HasSuffix(base, ".db")
	case len(parts) == 4 && parts[0] == "backup" && parts[2] == "email":
		return base == "passwd" || base == "quota" || base == "aliases"
	case len(parts) >= 4 && parts[0] == "domains" && parts[2] == "public_html":
		return base == "wp-config.php" && len(parts) <= 8 && e.Size <= 256<<10
	}
	return false
}

// daDump agrees with the dump of a database: backup/DB.sql or backup/DB.sql.gz.
var daDump = regexp.MustCompile(`^backup/([a-z0-9_]+)\.sql(\.gz)?$`)

func (d *directAdmin) add(e Entry, content []byte) {
	rel, ok := d.rel(e)
	if !ok {
		return
	}
	d.sizes.add(e)
	if content != nil {
		d.files[rel] = content
	}
	if m := daDump.FindStringSubmatch(rel); m != nil && e.Type == TypeFile {
		d.dumps[m[1]] = Content{Path: e.Name, Bytes: e.Size}
	}
	parts := strings.SplitN(rel, "/", 6)
	switch {
	case len(parts) == 6 && parts[0] == "imap" && parts[3] == "Maildir":
		d.mail.add(parts[1]+"/"+parts[2], parts[4]+"/"+parts[5], e)
	case len(parts) == 5 && parts[0] == "imap" && parts[3] == "Maildir":
		d.mail.add(parts[1]+"/"+parts[2], parts[4], e)
	case len(parts) >= 2 && parts[0] == "Maildir":
		d.mail.add("", strings.Join(parts[1:], "/"), e)
	case len(parts) == 4 && parts[0] == "backup" && parts[2] == "email" && (parts[3] == "autoresponder.conf" || parts[3] == "vacation.conf") && e.Size > 0:
		d.other["autoresponders"] = true
	case len(parts) == 2 && parts[0] == "backup" && parts[1] == "ftp.passwd" && e.Size > 0:
		d.other["ftp"] = true
	case len(parts) == 3 && parts[0] == "domains" && parts[2] == "private_html" && e.Type == TypeDir:
		d.other["private:"+parts[1]] = true
	}
}

func (d *directAdmin) detected() bool {
	return keyValues(d.files["backup/user.conf"], "=")["username"] != ""
}

func (d *directAdmin) account() (Account, error) {
	var acc Account
	warn := func(item, format string, args ...any) {
		acc.Warnings = append(acc.Warnings, Warning{Item: item, Message: fmt.Sprintf(format, args...)})
	}
	conf := keyValues(d.files["backup/user.conf"], "=")
	user := conf["username"]
	acc.Source = Source{Account: user}
	acc.Customer = &Customer{Name: firstNonEmpty(conf["name"], user), Email: conf["email"]}
	mainDomain := domainName(conf["domain"])

	var domains []string
	for rel := range d.files {
		dir, base := path.Split(rel)
		if base != "domain.conf" {
			continue
		}
		if name := domainName(strings.TrimSuffix(strings.TrimPrefix(dir, "backup/"), "/")); name != "" {
			domains = append(domains, name)
		}
	}
	sort.Slice(domains, func(i, j int) bool {
		if (domains[i] == mainDomain) != (domains[j] == mainDomain) {
			return domains[i] == mainDomain
		}
		return domains[i] < domains[j]
	})
	if len(domains) == 0 {
		return acc, fmt.Errorf("the DirectAdmin backup has no domains")
	}

	var websites []Website
	seen := map[string]bool{}
	content := func(dir string, exclude []string) *Content {
		full := d.full(dir)
		bytes, _ := d.sizes.of(full)
		for _, x := range exclude {
			b, _ := d.sizes.of(full + "/" + x)
			bytes -= b
		}
		return &Content{Path: full, Bytes: max(bytes, 0), Exclude: exclude}
	}
	appConfig := func(dir string) *AppConfig {
		if db := wordpressDatabase(d.files[dir+"/wp-config.php"]); db != "" {
			return &AppConfig{Kind: "wordpress", File: "wp-config.php", Database: db}
		}
		return nil
	}
	for _, domain := range domains {
		root := "domains/" + domain + "/public_html"
		var subs []string
		for _, label := range lines(d.files["backup/"+domain+"/subdomain.list"]) {
			sub := domainName(strings.ToLower(label) + "." + domain)
			if sub != "" && !strings.Contains(label, "/") && !strings.Contains(label, ".") {
				subs = append(subs, strings.ToLower(label))
			}
		}
		sort.Strings(subs)
		w := Website{Name: domain, Kind: "public", Runtime: Runtime{Family: "php", DocumentRoot: "."}, Content: &WebsiteContent{Files: content(root, subs)}, AppConfig: appConfig(root)}
		w.DiskBytes = w.Content.Files.Bytes
		addDomain(&w, seen, domain, "primary")
		addDomain(&w, seen, "www."+domain, "alias")
		for _, line := range lines(d.files["backup/"+domain+"/domain.pointers"]) {
			name, rest, _ := strings.Cut(line, "=")
			kind := "alias"
			if strings.Contains(rest, "pointer") {
				kind = "redirect"
			}
			if alias := domainName(name); alias != "" {
				addDomain(&w, seen, alias, kind)
				addDomain(&w, seen, "www."+alias, kind)
			}
		}
		websites = append(websites, w)
		for _, label := range subs {
			name := label + "." + domain
			if seen[name] {
				continue
			}
			sw := Website{Name: name, Kind: "public", Runtime: Runtime{Family: "php", DocumentRoot: "."}, Content: &WebsiteContent{Files: content(root+"/"+label, nil)}, AppConfig: appConfig(root + "/" + label)}
			sw.DiskBytes = sw.Content.Files.Bytes
			addDomain(&sw, seen, name, "primary")
			websites = append(websites, sw)
		}
		if d.other["private:"+domain] {
			warn("website:"+domain, "The domain has a separate private_html directory for HTTPS. The import copies public_html only.")
		}
	}

	// Databases and their users: backup/DB.conf has a line for each user of the database.
	known := map[string]bool{}
	for _, db := range sortedKeys(d.dumps) {
		known[db] = true
	}
	ownerOf := func(db string) int {
		for i, w := range websites {
			if w.AppConfig != nil && w.AppConfig.Database == db {
				return i
			}
		}
		return 0
	}
	users := map[string]map[string]string{}
	for _, db := range sortedKeys(d.dumps) {
		c := d.dumps[db]
		i := ownerOf(db)
		websites[i].Databases = append(websites[i].Databases, Database{Engine: "mariadb", Name: db, Content: &c})
		for key, value := range keyValues(d.files["backup/"+db+".conf"], "=") {
			if key == "db_collation" || key == "accesshosts" || key == user || !strings.Contains(value, "_priv=") {
				continue
			}
			v := urlValues(value)
			access := "readonly"
			for _, p := range []string{"insert_priv", "update_priv", "delete_priv", "alter_priv", "create_priv"} {
				if strings.EqualFold(v.Get(p), "Y") {
					access = "readwrite"
				}
			}
			if users[key] == nil {
				users[key] = map[string]string{}
			}
			users[key][db] = access
		}
	}
	for _, name := range sortedKeys(users) {
		g := grantsOf(users[name], known)
		if len(g) == 0 {
			continue
		}
		i := 0
		for j, w := range websites {
			for _, db := range w.Databases {
				if db.Name == g[0].Database {
					i = j
				}
			}
		}
		websites[i].DatabaseUsers = append(websites[i].DatabaseUsers, DatabaseUser{Engine: "mariadb", Name: name, Access: "website", Grants: g})
	}

	// Cron jobs: "N=schedule command".
	for _, line := range lines(d.files["backup/crontab.conf"]) {
		if i := strings.Index(line, "="); i > 0 && isDigits(line[:i]) {
			line = line[i+1:]
		}
		schedule, command, ok, reason := cronLine(line)
		if !ok {
			if reason != "" {
				warn("cron", "%s Line: %s", reason, short(line))
			}
			continue
		}
		websites[0].CronJobs = append(websites[0].CronJobs, CronJob{Schedule: schedule, Command: command})
	}
	acc.Websites = websites

	// Mail and DNS of each domain.
	for _, domain := range domains {
		md := MailDomain{Name: domain}
		hashes := keyValuesFirst(d.files["backup/"+domain+"/email/passwd"])
		quotas := keyValuesFirst(d.files["backup/"+domain+"/email/quota"])
		for _, local := range sortedKeys(hashes) {
			if !validLocal(local) {
				warn("mail:"+local+"@"+domain, "The panel cannot use this mailbox name.")
				continue
			}
			box := Mailbox{LocalPart: local, QuotaBytes: sizeOf(quotas[local]), PasswordHash: passwordHash(hashes[local])}
			if box.PasswordHash == "" {
				warn("mail:"+local+"@"+domain, "The backup has no password hash that the mail server can use. The import gives the mailbox a new password.")
			}
			key := domain + "/" + local
			if _, ok := d.mail.bytes[key]; ok {
				box.Content = &Content{Path: d.full("imap/" + key + "/Maildir"), Bytes: d.mail.bytes[key]}
				box.Messages = d.mail.messages[key]
			}
			md.Mailboxes = append(md.Mailboxes, box)
		}
		forwards, skipped := aliasesFile(d.files["backup/"+domain+"/email/aliases"], domain, ":")
		for _, s := range skipped {
			warn("mail:"+s, "The import copies only forwarders to mail addresses. Add a catch-all address or a pipe to a program again.")
		}
		for _, local := range sortedKeys(forwards) {
			// DirectAdmin sends the mail of the account name to the account itself.
			if local == user && len(forwards[local]) == 1 && forwards[local][0] == user+"@"+domain {
				continue
			}
			if !validLocal(local) || hasMailbox(&md, local) {
				warn("mail:"+local+"@"+domain, "The address is a mailbox and a forwarder on the old panel. The import copies the mailbox only.")
				continue
			}
			md.Aliases = append(md.Aliases, Alias{LocalPart: local, Kind: aliasKind(domain, forwards[local]), Destinations: forwards[local]})
		}
		if len(md.Mailboxes) > 0 || len(md.Aliases) > 0 {
			acc.Mail.Domains = append(acc.Mail.Domains, md)
		}
		if zone, ok := d.files["backup/"+domain+"/"+domain+".db"]; ok {
			acc.DNSZones = append(acc.DNSZones, Zone{Name: domain, ZoneFile: string(zone)})
		}
	}
	if b, ok := d.mail.bytes[""]; ok && b > 0 {
		warn("mail", "The default mailbox of the DirectAdmin account (%s@%s) is not in the backup of a domain. The import does not copy it.", user, mainDomain)
	}
	warn("files", "The import copies public_html of each domain. Copy other files of the home directory with SFTP.")
	if d.other["autoresponders"] {
		warn("mail", "The import does not copy autoresponders and vacation messages.")
	}
	if d.other["ftp"] {
		warn("ftp", "The panel has no FTP accounts. Use SFTP with an SSH key in place of FTP.")
	}
	return acc, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}
