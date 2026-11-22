package migrate

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// cpanel reads a cPanel account backup: the file that "pkgacct" or the backup of cPanel makes,
// for example "cpmove-USER.tar.gz" or "backup-MM.DD.YYYY_HH-MM-SS_USER.tar.gz". All members
// are in one directory. The parts that the import uses:
//
//	cp/USER                        account settings (CONTACTEMAIL)
//	userdata/main                  main_domain, addon_domains, parked_domains, sub_domains
//	userdata/DOMAIN                documentroot, serveralias, phpversion of each web domain
//	homedir/ or homedir.tar        the home directory of the account
//	homedir/etc/DOMAIN/passwd      the mailboxes of a domain; shadow has the password hashes
//	homedir/etc/DOMAIN/quota       the size limit of each mailbox
//	homedir/mail/DOMAIN/BOX/       the Maildir of each mailbox
//	mysql/DB.sql                   a dump of each MySQL database
//	mysql.sql                      the grants of the database users
//	dnszones/DOMAIN.db             the DNS zones
//	va/DOMAIN                      the forwarders of a domain
//	cron/USER                      the crontab of the account
type cpanel struct {
	top    string
	topSet bool
	mixed  bool
	files  map[string][]byte
	sizes  *sizes
	mail   *maildirs
	dumps  map[string]int64
	psql   []string
	home   string
	ssl    bool
	ftp    bool
}

func newCpanel() *cpanel {
	return &cpanel{files: map[string][]byte{}, mail: newMaildirs(), dumps: map[string]int64{}}
}

func (c *cpanel) name() string { return "cpanel" }

// rel gives the path of a member in the directory of the account.
func (c *cpanel) rel(e Entry) (string, bool) {
	if !c.topSet {
		c.top, _, _ = strings.Cut(e.Name, "/")
		c.topSet = true
		c.sizes = newSizes(c.top, 10)
	}
	if !Within(e.Name, c.top) {
		c.mixed = true
		return "", false
	}
	return Rel(e.Name, c.top), true
}

// homeRel gives the path of a member in the home directory.
func homeRel(rel string) (string, bool) {
	for _, root := range []string{"homedir", "homedir.tar"} {
		if Within(rel, root) && rel != root {
			return Rel(rel, root), true
		}
	}
	return "", false
}

func (c *cpanel) wants(e Entry) bool {
	rel, ok := c.rel(e)
	if !ok || e.Type != TypeFile {
		return false
	}
	dir, base := path.Split(rel)
	switch dir {
	case "":
		return base == "version" || base == "homedir_paths" || base == "mysql.sql"
	case "cp/", "va/", "cron/":
		return true
	case "userdata/":
		return !strings.HasSuffix(base, ".cache") && !strings.HasSuffix(base, "_SSL") && !strings.HasSuffix(base, ".json")
	case "dnszones/":
		return strings.HasSuffix(base, ".db")
	}
	home, ok := homeRel(rel)
	if !ok {
		return false
	}
	parts := strings.Split(home, "/")
	if len(parts) == 3 && parts[0] == "etc" && (parts[2] == "passwd" || parts[2] == "shadow" || parts[2] == "quota") {
		return true
	}
	return base == "wp-config.php" && len(parts) <= 8 && e.Size <= 256<<10
}

func (c *cpanel) add(e Entry, content []byte) {
	rel, ok := c.rel(e)
	if !ok {
		return
	}
	c.sizes.add(e)
	if content != nil {
		c.files[rel] = content
	}
	if strings.HasPrefix(rel, "homedir/") && c.home == "" {
		c.home = c.top + "/homedir"
	} else if strings.HasPrefix(rel, "homedir.tar/") && c.home == "" {
		c.home = c.top + "/homedir.tar"
	}
	if name, ok := strings.CutPrefix(rel, "mysql/"); ok && e.Type == TypeFile && strings.HasSuffix(name, ".sql") && !strings.Contains(name, "/") {
		c.dumps[strings.TrimSuffix(name, ".sql")] = e.Size
	}
	if name, ok := strings.CutPrefix(rel, "psql/"); ok && e.Type == TypeFile && strings.HasSuffix(name, ".tar") {
		c.psql = append(c.psql, strings.TrimSuffix(name, ".tar"))
	}
	if strings.HasPrefix(rel, "apache_tls/") || strings.HasPrefix(rel, "sslcerts/") {
		c.ssl = c.ssl || e.Type == TypeFile
	}
	if rel == "proftpdpasswd" && e.Size > 0 {
		c.ftp = true
	}
	if home, ok := homeRel(rel); ok {
		// homedir/mail/DOMAIN/BOX/... is the Maildir of a mailbox. homedir/mail/cur and the
		// folders homedir/mail/.NAME are the default mailbox of the account.
		parts := strings.SplitN(home, "/", 4)
		switch {
		case len(parts) == 4 && parts[0] == "mail" && strings.Contains(parts[1], ".") && !strings.HasPrefix(parts[1], "."):
			c.mail.add(parts[1]+"/"+parts[2], parts[3], e)
		case len(parts) >= 3 && parts[0] == "mail" && (parts[1] == "cur" || parts[1] == "new" || strings.HasPrefix(parts[1], ".")):
			c.mail.add("", strings.Join(parts[1:], "/"), e)
		}
	}
}

func (c *cpanel) detected() bool {
	if !c.topSet || c.mixed {
		return false
	}
	if _, ok := c.files["userdata/main"]; !ok {
		return false
	}
	for rel := range c.files {
		if strings.HasPrefix(rel, "cp/") {
			return true
		}
	}
	return false
}

// site is a web domain of cPanel before it becomes a website.
type cpanelSite struct {
	name    string
	docroot string
	aliases []string
	php     string
}

func (c *cpanel) account() (Account, error) {
	var acc Account
	warn := func(item, format string, args ...any) {
		acc.Warnings = append(acc.Warnings, Warning{Item: item, Message: fmt.Sprintf(format, args...)})
	}
	user := ""
	var cp map[string]string
	for rel, b := range c.files {
		if name, ok := strings.CutPrefix(rel, "cp/"); ok && !strings.Contains(name, "/") {
			user, cp = name, keyValues(b, "=")
		}
	}
	if u := cp["USER"]; u != "" {
		user = u
	}
	acc.Source = Source{Account: user, Version: strings.Join(lines(c.files["version"]), "; ")}
	acc.Customer = &Customer{Name: user, Email: strings.TrimSpace(cp["CONTACTEMAIL"])}

	main := parseYAML(c.files["userdata/main"])
	homePath := strings.TrimRight(strings.TrimSpace(firstLine(c.files["homedir_paths"])), "/")
	mainDomain := domainName(main.values["main_domain"])
	if mainDomain == "" {
		mainDomain = domainName(cp["DNS"])
	}
	if mainDomain == "" {
		return acc, fmt.Errorf("the cPanel backup has no main domain")
	}
	vhost := func(name string) yamlDoc { return parseYAML(c.files["userdata/"+name]) }
	if homePath == "" {
		homePath = strings.TrimRight(vhost(mainDomain).values["homedir"], "/")
	}
	if homePath == "" {
		homePath = "/home/" + user
	}

	// The web domains: the main domain, each addon domain (with its subdomain) and each other
	// subdomain.
	var sites []cpanelSite
	addonOf := map[string]string{}
	for addon, sub := range main.maps["addon_domains"] {
		if a, s := domainName(addon), domainName(sub); a != "" && s != "" {
			addonOf[s] = a
		}
	}
	siteOf := func(name, host string) cpanelSite {
		v := vhost(host)
		s := cpanelSite{name: name, docroot: strings.TrimRight(v.values["documentroot"], "/"), php: phpVersion(v.values["phpversion"])}
		for _, alias := range strings.Fields(v.values["serveralias"]) {
			if d := domainName(alias); d != "" && d != name && d != host && !strings.HasPrefix(d, "mail.") {
				s.aliases = append(s.aliases, d)
			}
		}
		return s
	}
	mainSite := siteOf(mainDomain, mainDomain)
	for _, parked := range main.lists["parked_domains"] {
		if d := domainName(parked); d != "" {
			mainSite.aliases = append(mainSite.aliases, d, "www."+d)
		}
	}
	sites = append(sites, mainSite)
	subs := main.lists["sub_domains"]
	sort.Strings(subs)
	for _, sub := range subs {
		s := domainName(sub)
		if s == "" {
			continue
		}
		if addon, ok := addonOf[s]; ok {
			site := siteOf(addon, s)
			site.aliases = append(site.aliases, "www."+addon)
			sites = append(sites, site)
			continue
		}
		sites = append(sites, siteOf(s, s))
	}

	// Two web domains with the same document root are one website.
	var websites []Website
	seen := map[string]bool{}
	byRoot := map[string]int{}
	docroots := []string{}
	for _, s := range sites {
		if i, ok := byRoot[s.docroot]; ok && s.docroot != "" {
			addDomain(&websites[i], seen, s.name, "alias")
			for _, a := range s.aliases {
				addDomain(&websites[i], seen, a, "alias")
			}
			continue
		}
		if seen[s.name] {
			continue
		}
		w := Website{Name: s.name, Kind: "public", Runtime: Runtime{Family: "php", PHPVersion: s.php, DocumentRoot: "."}}
		addDomain(&w, seen, s.name, "primary")
		for _, a := range s.aliases {
			addDomain(&w, seen, a, "alias")
		}
		byRoot[s.docroot] = len(websites)
		websites = append(websites, w)
		docroots = append(docroots, s.docroot)
	}

	// The files of each website: its document root, without the document roots of the other
	// websites in it. The import copies only the document roots.
	archivePath := func(docroot string) (string, bool) {
		rel, ok := strings.CutPrefix(docroot, homePath+"/")
		if !ok || c.home == "" {
			return "", false
		}
		if cleaned, ok := CleanName(rel); ok {
			return c.home + "/" + cleaned, true
		}
		return "", false
	}
	for i := range websites {
		p, ok := archivePath(docroots[i])
		if !ok {
			warn("website:"+websites[i].Name, "The document root %s is not in the home directory of the backup. The import makes the website with no files.", docroots[i])
			continue
		}
		content := &Content{Path: p}
		bytes, _ := c.sizes.of(p)
		for j := range websites {
			other, ok := archivePath(docroots[j])
			if j == i || !ok || !Within(other, p) || other == p {
				continue
			}
			content.Exclude = append(content.Exclude, Rel(other, p))
			b, _ := c.sizes.of(other)
			bytes -= b
		}
		sort.Strings(content.Exclude)
		content.Bytes = max(bytes, 0)
		websites[i].Content = &WebsiteContent{Files: content}
		websites[i].DiskBytes = content.Bytes
		if b := c.files[strings.TrimPrefix(p, c.top+"/")+"/wp-config.php"]; b != nil {
			if db := wordpressDatabase(b); db != "" {
				websites[i].AppConfig = &AppConfig{Kind: "wordpress", File: "wp-config.php", Database: db}
			}
		}
	}

	// The databases: a website that names a database in its wp-config.php gets it. The main
	// website gets the other databases.
	known := map[string]bool{}
	for _, db := range sortedKeys(c.dumps) {
		known[db] = true
		owner := 0
		for i, w := range websites {
			if w.AppConfig != nil && w.AppConfig.Database == db {
				owner = i
				break
			}
		}
		websites[owner].Databases = append(websites[owner].Databases, Database{Engine: "mariadb", Name: db, Content: &Content{Path: c.top + "/mysql/" + db + ".sql", Bytes: c.dumps[db]}})
	}
	ownerOf := func(db string) int {
		for i, w := range websites {
			for _, d := range w.Databases {
				if d.Name == db {
					return i
				}
			}
		}
		return 0
	}
	grants := mysqlGrants(c.files["mysql.sql"])
	for _, name := range sortedKeys(grants) {
		if name == user {
			// The MySQL user of the cPanel account. The panel has no such user.
			continue
		}
		g := grantsOf(grants[name], known)
		if len(g) == 0 {
			continue
		}
		i := ownerOf(g[0].Database)
		websites[i].DatabaseUsers = append(websites[i].DatabaseUsers, DatabaseUser{Engine: "mariadb", Name: name, Access: "website", Grants: g})
	}
	for _, db := range c.psql {
		warn("database:"+db, "The import does not copy PostgreSQL databases yet. Make the database in the panel, and restore its dump.")
	}

	// Cron jobs: the main website gets the crontab of the account.
	for _, line := range lines(c.files["cron/"+user]) {
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

	// Mail.
	mailDomains := map[string]*MailDomain{}
	domainOf := func(name string) *MailDomain {
		if mailDomains[name] == nil {
			mailDomains[name] = &MailDomain{Name: name}
		}
		return mailDomains[name]
	}
	for rel, b := range c.files {
		home, ok := homeRel(rel)
		if !ok {
			continue
		}
		parts := strings.Split(home, "/")
		if len(parts) != 3 || parts[0] != "etc" || parts[2] != "passwd" {
			continue
		}
		domain := domainName(parts[1])
		if domain == "" {
			continue
		}
		base := strings.TrimSuffix(rel, "passwd")
		hashes := keyValuesFirst(c.files[base+"shadow"])
		quotas := keyValuesFirst(c.files[base+"quota"])
		md := domainOf(domain)
		for _, line := range lines(b) {
			local, _, _ := strings.Cut(line, ":")
			local = strings.ToLower(strings.TrimSpace(local))
			if !validLocal(local) {
				warn("mail:"+local+"@"+domain, "The panel cannot use this mailbox name.")
				continue
			}
			box := Mailbox{LocalPart: local, QuotaBytes: sizeOf(quotas[local]), PasswordHash: passwordHash(hashes[local])}
			if box.PasswordHash == "" {
				warn("mail:"+local+"@"+domain, "The backup has no password hash that the mail server can use. The import gives the mailbox a new password.")
			}
			key := domain + "/" + local
			if _, ok := c.mail.bytes[key]; ok {
				box.Content = &Content{Path: c.home + "/mail/" + key, Bytes: c.mail.bytes[key]}
				box.Messages = c.mail.messages[key]
			}
			md.Mailboxes = append(md.Mailboxes, box)
		}
	}
	for rel, b := range c.files {
		name, ok := strings.CutPrefix(rel, "va/")
		domain := domainName(name)
		if !ok || domain == "" {
			continue
		}
		forwards, skipped := aliasesFile(b, domain, ":")
		for _, s := range skipped {
			warn("mail:"+s, "The import copies only forwarders to mail addresses. Add a catch-all address or a pipe to a program again.")
		}
		if len(forwards) == 0 {
			continue
		}
		md := domainOf(domain)
		for _, local := range sortedKeys(forwards) {
			if !validLocal(local) || hasMailbox(md, local) {
				warn("mail:"+local+"@"+domain, "The address is a mailbox and a forwarder on the old panel. The import copies the mailbox only.")
				continue
			}
			md.Aliases = append(md.Aliases, Alias{LocalPart: local, Kind: aliasKind(domain, forwards[local]), Destinations: forwards[local]})
		}
	}
	for _, name := range sortedKeys(mailDomains) {
		md := mailDomains[name]
		sort.Slice(md.Mailboxes, func(i, j int) bool { return md.Mailboxes[i].LocalPart < md.Mailboxes[j].LocalPart })
		acc.Mail.Domains = append(acc.Mail.Domains, *md)
	}
	if b, ok := c.mail.bytes[""]; ok && b > 0 {
		warn("mail", "The default mailbox of the cPanel account is not a mailbox of a domain. The import does not copy it.")
	}

	// DNS zones.
	for _, rel := range sortedKeys(c.files) {
		name, ok := strings.CutPrefix(rel, "dnszones/")
		if !ok {
			continue
		}
		if domain := domainName(strings.TrimSuffix(name, ".db")); domain != "" {
			acc.DNSZones = append(acc.DNSZones, Zone{Name: domain, ZoneFile: string(c.files[rel])})
		}
	}

	warn("files", "The import copies the document root of each website. Copy other files of the home directory with SFTP.")
	if c.ssl {
		warn("certificates", "The import does not copy the TLS certificates. The panel gets a certificate for each domain when its DNS points to the new server.")
	}
	if c.ftp {
		warn("ftp", "The panel has no FTP accounts. Use SFTP with an SSH key in place of FTP.")
	}
	return acc, nil
}

func firstLine(b []byte) string {
	l := lines(b)
	if len(l) == 0 {
		return ""
	}
	return l[0]
}

// keyValuesFirst reads lines of "name:value:..." and gives the second field of each name.
func keyValuesFirst(b []byte) map[string]string {
	out := map[string]string{}
	for _, line := range lines(b) {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) >= 2 {
			out[strings.ToLower(strings.TrimSpace(parts[0]))] = strings.TrimSpace(parts[1])
		}
	}
	return out
}

func hasMailbox(d *MailDomain, local string) bool {
	for _, b := range d.Mailboxes {
		if b.LocalPart == local {
			return true
		}
	}
	return false
}

func short(s string) string {
	if len(s) > 120 {
		return s[:120] + "…"
	}
	return s
}
