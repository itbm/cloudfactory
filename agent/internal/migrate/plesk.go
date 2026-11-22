package migrate

import (
	"bytes"
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// plesk reads a Plesk backup: the file that the Backup Manager of Plesk or "pleskbackup"
// exports, for example "backup_example.com_2311221200.tar". XML files, for example
// "backup_info_2311221200.xml" with the root element "migration-dump", describe the
// subscriptions. The content of each part is in an archive next to them, for example
// "backup_user-data_2311221200.tgz". The parts that the import uses:
//
//	domain                              a subscription, with its name
//	domain/preferences/domain-alias     the domain aliases
//	domain/properties/dns-zone/dnsrec   the records of the DNS zone
//	domain/phosting                     the hosting: www-root, the system user, the content
//	                                    (cid "user-data" or "docroot") and the PHP handler
//	domain/phosting/sites/site          the additional domains and the subdomains
//	domain/databases/database           the databases (cid "sqldump") and their dbuser
//	domain/mailsystem/mailusers/mailuser  the mailboxes (cid "mailbox"), with the alias,
//	                                    redirect and mailgroup-member elements
//	client/preferences/pinfo            the name and the email of the customer
//
// A content element is <cid type="..." path="DIR" offset="SUBDIR"><content-file>FILE
// </content-file></cid>. The archive FILE is in DIR, which is relative to the root of the
// backup or to the XML file.
type plesk struct {
	xml   map[string][]byte
	found bool
	// files are the names of the members that are not in a nested archive. nested are the
	// nested archives that the walk read.
	files  map[string]bool
	nested map[string]bool
	// dirs has the size of each directory in a nested archive, up to pleskDepth levels.
	dirs map[string]int64
	mail *maildirs
	wp   map[string][]byte
}

// pleskDepth is the number of directory levels in a nested archive that the adapter records.
const pleskDepth = 4

// pleskContent agrees with the name of a content archive that an inspection reads: the files
// of the websites and the mailboxes. The database dumps are not read.
var pleskContent = regexp.MustCompile(`(?:^|_)(user-data|docroot|mailbox|domainmail)_\d{10}(?:_\d{10})?\.`)

// pleskContentArchive reports whether a member is a content archive that an inspection reads.
func pleskContentArchive(name string) bool {
	base := path.Base(name)
	return IsTarName(base) && pleskContent.MatchString(base)
}

func newPlesk() *plesk {
	return &plesk{xml: map[string][]byte{}, files: map[string]bool{}, nested: map[string]bool{}, dirs: map[string]int64{}, mail: newMaildirs(), wp: map[string][]byte{}}
}

func (p *plesk) name() string { return "plesk" }

// nestedOf gives the nested archive of a member and the path of the member in it.
func nestedOf(name string) (archive, rel string, ok bool) {
	parts := strings.Split(name, "/")
	for i := 0; i < len(parts)-1; i++ {
		if IsTarName(parts[i]) {
			return strings.Join(parts[:i+1], "/"), strings.Join(parts[i+1:], "/"), true
		}
	}
	return "", "", false
}

func (p *plesk) wants(e Entry) bool {
	if e.Type != TypeFile {
		return false
	}
	archive, rel, ok := nestedOf(e.Name)
	if !ok {
		return strings.HasSuffix(strings.ToLower(e.Name), ".xml") && strings.Count(e.Name, "/") <= 10
	}
	return pleskContentArchive(archive) && path.Base(rel) == "wp-config.php" && strings.Count(rel, "/") <= 6 && e.Size <= 256<<10
}

func (p *plesk) add(e Entry, content []byte) {
	archive, rel, ok := nestedOf(e.Name)
	if !ok {
		if e.Type == TypeFile && len(p.files) < maxDirs {
			p.files[e.Name] = true
		}
		if content != nil && bytes.Contains(content, []byte("<migration-dump")) {
			p.xml[e.Name] = content
			p.found = true
		}
		return
	}
	p.nested[archive] = true
	if content != nil {
		p.wp[e.Name] = content
	}
	if e.Type != TypeFile {
		return
	}
	// The size of the archive and of each directory in it.
	dir := archive
	parts := strings.Split(rel, "/")
	p.dirs[dir] += e.Size
	for i := 0; i < len(parts)-1 && i < pleskDepth; i++ {
		dir += "/" + parts[i]
		if _, ok := p.dirs[dir]; ok || len(p.dirs) < maxDirs {
			p.dirs[dir] += e.Size
		}
	}
	// A message of a Maildir: BOX/cur/FILE, BOX/new/FILE or BOX/.FOLDER/cur/FILE.
	if n := len(parts); n >= 2 && (parts[n-2] == "cur" || parts[n-2] == "new") {
		box := n - 2
		if box > 0 && strings.HasPrefix(parts[box-1], ".") && len(parts[box-1]) > 1 {
			box--
		}
		root := archive
		if box > 0 {
			root += "/" + strings.Join(parts[:box], "/")
		}
		p.mail.add(root, strings.Join(parts[box:], "/"), e)
	}
}

func (p *plesk) detected() bool { return p.found }

// exists reports whether the archive has a member or read a nested archive with the name.
func (p *plesk) exists(name string) bool { return p.files[name] || p.nested[name] }

// pleskSite is a web site of a subscription before it becomes a website.
type pleskSite struct {
	name    string
	docroot string
	php     string
	aliases []string
	// files is a "docroot" content archive of the site, when the backup has one.
	files string
	cron  []CronJob
}

func (p *plesk) account() (Account, error) {
	var acc Account
	warn := func(item, format string, args ...any) {
		acc.Warnings = append(acc.Warnings, Warning{Item: item, Message: fmt.Sprintf(format, args...)})
	}

	// The subscriptions of all XML files. A higher level can repeat a domain of a lower level:
	// the element with the most content is used.
	type sub struct {
		n   *node
		dir string
	}
	subs := map[string]sub{}
	var order []string
	var client *node
	for _, name := range sortedKeys(p.xml) {
		root, err := parseXML(p.xml[name])
		if err != nil || root.name != "migration-dump" {
			continue
		}
		if acc.Source.Version == "" {
			acc.Source.Version = root.value("dump-version")
		}
		if client == nil {
			if c := root.find("client", "domain"); len(c) > 0 {
				client = c[0]
			}
		}
		for _, d := range root.find("domain", "domain") {
			dn := domainName(d.attr["name"])
			if dn == "" {
				continue
			}
			old, ok := subs[dn]
			if !ok {
				order = append(order, dn)
			}
			if !ok || len(d.kids) > len(old.n.kids) {
				subs[dn] = sub{n: d, dir: path.Dir(name)}
			}
		}
	}
	if len(subs) == 0 {
		return acc, fmt.Errorf("the Plesk backup has no subscription")
	}
	if client != nil {
		acc.Source.Account = client.attr["name"]
		info := pinfo(client)
		acc.Customer = &Customer{Name: info["name"], Email: info["email"]}
		if acc.Customer.Name == "" {
			acc.Customer.Name = info["company"]
		}
	}

	// resolve gives the path of a content archive in the backup.
	resolve := func(dir string, cid *node) string {
		file := path.Base(cid.value("content-file"))
		if file == "." || file == "/" || file == "" {
			return ""
		}
		at := cid.attr["path"]
		for _, candidate := range []string{path.Join(dir, at, file), path.Join(at, file), path.Join(dir, file)} {
			if c, ok := CleanName(candidate); ok && p.exists(c) {
				return withOffset(c, cid.attr["offset"])
			}
		}
		// A different layout: the only archive with the name.
		var found []string
		for _, names := range []map[string]bool{p.files, p.nested} {
			for name := range names {
				if path.Base(name) == file {
					found = append(found, name)
				}
			}
		}
		if len(found) == 1 {
			return withOffset(found[0], cid.attr["offset"])
		}
		return ""
	}
	cidOf := func(n *node, kind string) *node {
		for _, c := range n.at("content").all("cid") {
			if c.attr["type"] == kind {
				return c
			}
		}
		return nil
	}

	seen := map[string]bool{}
	sysusers := []string{}
	mailDomains := map[string]*MailDomain{}
	newPasswords := map[string]int{}
	cronFound := false
	for _, name := range order {
		d, dir := subs[name].n, subs[name].dir
		hosting := d.child("phosting")

		// The web sites: the subscription and each site of it.
		var sites []pleskSite
		userData := ""
		if hosting != nil {
			if u := hosting.at("preferences", "sysuser").get("name"); u != "" {
				sysusers = append(sysusers, u)
			}
			if c := cidOf(hosting, "user-data"); c != nil {
				userData = resolve(dir, c)
			}
			main := pleskSite{name: name, docroot: wwwRoot(hosting), php: pleskPHP(hosting), aliases: pleskAliases(d)}
			if c := cidOf(hosting, "docroot"); c != nil {
				main.files = resolve(dir, c)
			}
			sites = append(sites, main)
			for _, s := range append(hosting.at("sites").all("site"), hosting.at("subdomains").all("subdomain")...) {
				sn := s.attr["name"]
				if s.name == "subdomain" && !strings.HasSuffix(sn, "."+name) {
					sn += "." + name
				}
				sn = domainName(sn)
				if sn == "" {
					continue
				}
				sh := s.child("phosting")
				if sh == nil {
					sh = s
				}
				site := pleskSite{name: sn, docroot: wwwRoot(sh), php: pleskPHP(sh), aliases: pleskAliases(s)}
				if site.php == "" {
					site.php = main.php
				}
				if c := cidOf(sh, "docroot"); c != nil {
					site.files = resolve(dir, c)
				}
				sites = append(sites, site)
			}
			for _, t := range hosting.find("crontab") {
				for _, line := range lines([]byte(t.content())) {
					schedule, command, ok, reason := cronLine(line)
					if !ok {
						if reason != "" {
							warn("cron", "%s Line: %s", reason, short(line))
						}
						continue
					}
					cronFound = true
					sites[0].cron = append(sites[0].cron, CronJob{Schedule: schedule, Command: command})
				}
			}
		}

		// Two sites with the same document root are one website.
		first := len(acc.Websites)
		byRoot := map[string]int{}
		var roots []pleskSite
		for _, s := range sites {
			if i, ok := byRoot[s.docroot]; ok && s.files == "" {
				addDomain(&acc.Websites[i], seen, s.name, "alias")
				for _, a := range s.aliases {
					addDomain(&acc.Websites[i], seen, a, "alias")
				}
				continue
			}
			if seen[s.name] {
				continue
			}
			w := Website{Name: s.name, Kind: "public", Runtime: Runtime{Family: "php", PHPVersion: s.php, DocumentRoot: "."}, CronJobs: s.cron}
			addDomain(&w, seen, s.name, "primary")
			for _, a := range s.aliases {
				addDomain(&w, seen, a, "alias")
			}
			byRoot[s.docroot] = len(acc.Websites)
			acc.Websites = append(acc.Websites, w)
			roots = append(roots, s)
		}

		// The files of each website: its document root, without the document roots of the
		// other websites in it.
		for i, s := range roots {
			w := &acc.Websites[first+i]
			var content *Content
			switch {
			case s.files != "":
				content = &Content{Path: s.files, Bytes: p.dirs[s.files]}
			case userData != "" && s.docroot != "":
				dir := userData + "/" + s.docroot
				content = &Content{Path: dir, Bytes: p.dirs[dir]}
				for j, o := range roots {
					if j == i || o.files != "" || !Within(o.docroot, s.docroot) || o.docroot == s.docroot {
						continue
					}
					content.Exclude = append(content.Exclude, Rel(o.docroot, s.docroot))
					content.Bytes -= p.dirs[userData+"/"+o.docroot]
				}
				sort.Strings(content.Exclude)
				content.Bytes = max(content.Bytes, 0)
			default:
				warn("website:"+w.Name, "The backup has no files of this website. The import makes the website with no files.")
				continue
			}
			w.Content = &WebsiteContent{Files: content}
			w.DiskBytes = content.Bytes
			if b := p.wp[content.Path+"/wp-config.php"]; b != nil {
				if db := wordpressDatabase(b); db != "" {
					w.AppConfig = &AppConfig{Kind: "wordpress", File: "wp-config.php", Database: db}
				}
			}
		}

		// The databases of the subscription and of its sites. A website that names a database
		// in its wp-config.php gets it. The first website of the subscription gets the others.
		var dbHolders []*node
		dbHolders = append(dbHolders, d)
		if hosting != nil {
			dbHolders = append(dbHolders, hosting.at("sites").all("site")...)
		}
		for _, holder := range dbHolders {
			for _, db := range holder.at("databases").all("database") {
				dbName := db.attr["name"]
				if dbName == "" {
					continue
				}
				if db.attr["type"] != "" && db.attr["type"] != "mysql" {
					warn("database:"+dbName, "The import does not copy PostgreSQL databases yet. Make the database in the panel, and restore its dump.")
					continue
				}
				if len(acc.Websites) == first {
					warn("database:"+dbName, "The subscription has no website for this database. The import does not copy it.")
					continue
				}
				owner := first
				for i := first; i < len(acc.Websites); i++ {
					if c := acc.Websites[i].AppConfig; c != nil && c.Database == dbName {
						owner = i
						break
					}
				}
				database := Database{Engine: "mariadb", Name: dbName}
				if c := cidOf(db, "sqldump"); c != nil {
					if dump := resolve(dir, c); dump != "" {
						size, _ := strconv.ParseInt(c.child("content-file").get("size"), 10, 64)
						database.Content = &Content{Path: dump, Bytes: max(size, 0)}
					}
				}
				if database.Content == nil {
					warn("database:"+dbName, "The backup has no dump of this database. The import makes an empty database.")
				}
				w := &acc.Websites[owner]
				w.Databases = append(w.Databases, database)
				for _, u := range db.all("dbuser") {
					if u.attr["name"] != "" {
						w.DatabaseUsers = append(w.DatabaseUsers, DatabaseUser{Engine: "mariadb", Name: u.attr["name"], Access: "website",
							Grants: []Grant{{Database: dbName, Privileges: "readwrite"}}})
					}
				}
			}
		}

		// The mail and the DNS zones of the subscription, of its sites and of its domain aliases.
		holders := []*node{d}
		if hosting != nil {
			holders = append(holders, hosting.at("sites").all("site")...)
		}
		for _, h := range holders {
			hn := domainName(h.attr["name"])
			if hn == "" {
				continue
			}
			if ms := h.child("mailsystem"); ms != nil && len(ms.at("mailusers").all("mailuser")) > 0 {
				md := mailDomains[hn]
				if md == nil {
					md = &MailDomain{Name: hn}
					mailDomains[hn] = md
				}
				p.mailOf(md, ms, dir, resolve, cidOf, warn, newPasswords)
			}
			// A subdomain has its records in the zone of the subscription.
			if z := h.at("properties", "dns-zone"); z != nil && !strings.HasSuffix(hn, "."+name) {
				acc.DNSZones = append(acc.DNSZones, Zone{Name: hn, ZoneFile: zoneFile(hn, z)})
			}
			for _, a := range h.at("preferences").all("domain-alias") {
				an := domainName(a.attr["name"])
				if z := a.child("dns-zone"); an != "" && z != nil {
					acc.DNSZones = append(acc.DNSZones, Zone{Name: an, ZoneFile: zoneFile(an, z)})
				}
				if an != "" && a.attr["mail"] == "true" && h.child("mailsystem") != nil {
					warn("mail:"+an, "The domain alias %s gets the mail of %s on the old panel. The import does not copy this. Add %s as a mail domain with aliases after the import.", an, hn, an)
				}
			}
		}
		if hosting != nil && len(hosting.find("ftpuser")) > 0 {
			warn("ftp", "The panel has no FTP accounts. Use SFTP with an SSH key in place of FTP.")
		}
	}
	if acc.Source.Account == "" && len(sysusers) > 0 {
		acc.Source.Account = sysusers[0]
	}
	if acc.Source.Account == "" {
		acc.Source.Account = order[0]
	}

	for _, name := range sortedKeys(mailDomains) {
		md := mailDomains[name]
		sort.Slice(md.Mailboxes, func(i, j int) bool { return md.Mailboxes[i].LocalPart < md.Mailboxes[j].LocalPart })
		sort.Slice(md.Aliases, func(i, j int) bool { return md.Aliases[i].LocalPart < md.Aliases[j].LocalPart })
		acc.Mail.Domains = append(acc.Mail.Domains, *md)
		if n := newPasswords[name]; n > 0 {
			warn("mail:"+name, "The backup has the passwords of %d mailboxes of %s encrypted with a key of the old server. The import gives these mailboxes a new password.", n, name)
		}
	}

	warn("files", "The import copies the document root of each website. Copy other files of the subscription with SFTP.")
	if !cronFound {
		warn("cron", "The import did not find scheduled tasks in the backup. Add the scheduled tasks of Plesk again as cron jobs.")
	}
	warn("certificates", "The import does not copy the TLS certificates. The panel gets a certificate for each domain when its DNS points to the new server.")
	return acc, nil
}

// mailOf adds the mailboxes and the addresses of a Plesk mail system to a mail domain.
func (p *plesk) mailOf(md *MailDomain, ms *node, dir string, resolve func(string, *node) string, cidOf func(*node, string) *node, warn func(string, string, ...any), newPasswords map[string]int) {
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
	for _, u := range ms.at("mailusers").all("mailuser") {
		local := strings.ToLower(u.attr["name"])
		if !validLocal(local) {
			warn("mail:"+local+"@"+domain, "The panel cannot use this mailbox name.")
			continue
		}
		if hasMailbox(md, local) {
			continue
		}
		prefs := u.child("preferences")
		var dests []string
		for _, r := range append(prefs.all("redirect"), prefs.all("mailgroup-member")...) {
			if a := strings.ToLower(r.content()); a != "" && !slices.Contains(dests, a) && validate.MailAddress(a) == nil {
				dests = append(dests, a)
			}
		}
		if mb := prefs.child("mailbox"); mb != nil && mb.attr["enabled"] != "false" {
			box := Mailbox{LocalPart: local}
			if q, err := strconv.ParseInt(u.attr["mailbox-quota"], 10, 64); err == nil && q > 0 {
				box.QuotaBytes = q
			}
			switch pw := u.at("properties", "password"); pw.get("type") {
			case "plain":
				if pw.content() != "" {
					box.PasswordHash = hashPassword(pw.content())
				}
			case "encrypted", "crypt":
				box.PasswordHash = passwordHash(pw.content())
			}
			if box.PasswordHash == "" {
				newPasswords[domain]++
			}
			if c := cidOf(mb, "mailbox"); c != nil {
				if root := resolve(dir, c); root != "" {
					for _, r := range []string{root, root + "/Maildir"} {
						if b, ok := p.mail.bytes[r]; ok {
							box.Content = &Content{Path: r, Bytes: b}
							box.Messages = p.mail.messages[r]
							break
						}
					}
				}
			}
			md.Mailboxes = append(md.Mailboxes, box)
			if len(dests) > 0 {
				warn("mail:"+local+"@"+domain, "The address is a mailbox and forwards mail on the old panel. The import copies the mailbox only.")
			}
		} else if len(dests) > 0 {
			addAlias(local, dests)
		}
		for _, a := range prefs.all("alias") {
			if al := strings.ToLower(a.content()); validLocal(al) && al != local {
				addAlias(al, []string{local + "@" + domain})
			}
		}
		if len(prefs.find("autoresponder")) > 0 {
			warn("mail:"+local+"@"+domain, "The import does not copy autoresponders.")
		}
	}
	if ca := ms.at("preferences", "catch-all").content(); ca != "" && !strings.HasPrefix(strings.ToLower(ca), "reject") && !strings.HasPrefix(strings.ToLower(ca), "bounce") {
		warn("mail:*@"+domain, "The import does not copy the catch-all address.")
	}
}

// withOffset gives the path of a sub-directory of a content archive.
func withOffset(p, offset string) string {
	if o, ok := CleanName(offset); ok && offset != "" {
		return p + "/" + o
	}
	return p
}

// wwwRoot gives the document root of a site, relative to the directory of the subscription,
// for example "httpdocs" or "blog.example.com". An absolute path loses the part up to the
// directory of the subscription ("/var/www/vhosts/example.com/").
func wwwRoot(n *node) string {
	root := strings.TrimSpace(n.value("www-root"))
	if root == "" {
		root = "httpdocs"
	}
	if i := strings.Index(root, "/vhosts/"); i >= 0 {
		rest := root[i+len("/vhosts/"):]
		if _, after, ok := strings.Cut(rest, "/"); ok {
			root = after
		} else {
			root = ""
		}
	}
	if c, ok := CleanName(root); ok {
		return c
	}
	return ""
}

// pleskPHPVersion agrees with the version in a PHP handler of Plesk, for example
// "plesk-php82-fpm" or "plesk-php7.4-fastcgi".
var pleskPHPVersion = regexp.MustCompile(`php-?(\d)\.?(\d{1,2})(?:\D|$)`)

// pleskPHP gives the PHP version of the hosting of a site, from an attribute or an element with
// "handler" in its name. It does not read the sites of a subscription.
func pleskPHP(n *node) string {
	var found string
	var walk func(n *node)
	walk = func(n *node) {
		for _, k := range sortedKeys(n.attr) {
			if found == "" && strings.Contains(k, "handler") {
				found = matchPHP(n.attr[k])
			}
		}
		for _, k := range n.kids {
			if found != "" || k.name == "sites" || k.name == "site" || k.name == "subdomains" {
				continue
			}
			if strings.Contains(k.name, "handler") {
				found = matchPHP(k.content())
			}
			walk(k)
		}
	}
	walk(n)
	return found
}

func matchPHP(s string) string {
	if m := pleskPHPVersion.FindStringSubmatch(strings.ToLower(s)); m != nil {
		return m[1] + "." + m[2]
	}
	return ""
}

// pleskAliases gives the domain aliases of a subscription or a site with the web service on,
// and the "www" name of each.
func pleskAliases(n *node) []string {
	var out []string
	if n.attr["www"] != "false" {
		if name := domainName(n.attr["name"]); name != "" {
			out = append(out, "www."+name)
		}
	}
	for _, a := range n.at("preferences").all("domain-alias") {
		if a.attr["web"] == "false" {
			continue
		}
		if name := domainName(a.attr["name"]); name != "" {
			out = append(out, name, "www."+name)
		}
	}
	return out
}

// pinfo gives the personal information of a client: <pinfo name="email">...</pinfo>.
func pinfo(n *node) map[string]string {
	out := map[string]string{}
	for _, p := range n.at("preferences").all("pinfo") {
		out[p.attr["name"]] = p.content()
	}
	return out
}

// zoneFile writes the records of a Plesk DNS zone in the format of RFC 1035. Each record is
// <dnsrec type="MX" src="example.com." dst="mail.example.com." opt="10"/>. The panel reads the
// file with its own rules, and leaves out the records that it does not use.
func zoneFile(zone string, z *node) string {
	var b strings.Builder
	fmt.Fprintf(&b, "$ORIGIN %s.\n", zone)
	for _, r := range z.all("dnsrec") {
		typ := strings.ToUpper(strings.TrimSpace(r.attr["type"]))
		src, dst, opt := strings.TrimSpace(r.attr["src"]), strings.TrimSpace(r.attr["dst"]), strings.TrimSpace(r.attr["opt"])
		if typ == "" || dst == "" || strings.ContainsAny(src+dst+opt, "\n\r") {
			continue
		}
		name := src
		switch {
		case name == "" || strings.EqualFold(strings.TrimSuffix(name, "."), zone):
			name = "@"
		case strings.HasSuffix(strings.ToLower(strings.TrimSuffix(name, ".")), "."+zone):
			name = strings.TrimSuffix(name, ".") + "."
		}
		value := dst
		switch typ {
		case "MX":
			if opt == "" {
				opt = "10"
			}
			value = opt + " " + absolute(dst, zone)
		case "SRV":
			value = opt + " " + absolute(dst, zone)
		case "CNAME", "NS", "PTR":
			value = absolute(dst, zone)
		case "TXT":
			value = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(dst) + `"`
		}
		fmt.Fprintf(&b, "%s\tIN\t%s\t%s\n", name, typ, value)
	}
	return b.String()
}

// absolute gives a host name with a final dot. A name with no dot is in the zone.
func absolute(host, zone string) string {
	if strings.HasSuffix(host, ".") {
		return host
	}
	if !strings.Contains(host, ".") {
		return host + "." + zone + "."
	}
	return host + "."
}
