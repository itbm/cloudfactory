package migrate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// ExportFormat is the name of the format of an account export of Cloudfactory.
const ExportFormat = "cf-export/1"

// cfExport reads an account export of Cloudfactory (docs/account-export.md). The account.json
// file has the description in the format of the import (cf-account/1), with more fields. The
// adapter keeps the parts that the import can make, and gives a warning for each other part.
// The parts that the import uses:
//
//	manifest.json                                    the format of the archive ("cf-export/1")
//	account.json                                     the description of the account
//	websites/WEBSITE/files.tar.gz                    the files of a website
//	websites/WEBSITE/databases/mariadb-DB.sql.gz     the dump of a MariaDB database
//
// The DNS zones come from the records in account.json, with no records that the old panel made
// (the "managed" records): the new panel makes its own name servers and the records of its
// websites. The export has no mail messages and no mailbox passwords.
type cfExport struct {
	manifest, description []byte
	// members are the files of the archive in "websites/", with their sizes.
	members map[string]int64
	// files is the size of the content of each archive of the files of a website.
	files map[string]int64
	wp    map[string][]byte
}

func newCfExport() *cfExport {
	return &cfExport{members: map[string]int64{}, files: map[string]int64{}, wp: map[string][]byte{}}
}

func (c *cfExport) name() string { return "cloudfactory" }

// cfFiles agrees with the archive of the files of a website in an export.
var cfFiles = regexp.MustCompile(`^websites/[^/]+/files\.tar\.gz$`)

// cfFilesArchive reports whether a member is an archive of the files of a website that an
// inspection reads.
func cfFilesArchive(name string) bool { return cfFiles.MatchString(name) }

func (c *cfExport) wants(e Entry) bool {
	if e.Type != TypeFile {
		return false
	}
	if archive, rel, ok := nestedOf(e.Name); ok {
		return cfFiles.MatchString(archive) && path.Base(rel) == "wp-config.php" && strings.Count(rel, "/") <= 3 && e.Size <= 256<<10
	}
	return e.Name == "manifest.json" || e.Name == "account.json"
}

func (c *cfExport) add(e Entry, content []byte) {
	if archive, _, ok := nestedOf(e.Name); ok {
		if cfFiles.MatchString(archive) {
			c.files[archive] += e.Size
			if content != nil {
				c.wp[e.Name] = content
			}
		}
		return
	}
	switch {
	case e.Name == "manifest.json":
		c.manifest = content
	case e.Name == "account.json":
		c.description = content
	case e.Type == TypeFile && strings.HasPrefix(e.Name, "websites/") && len(c.members) < maxDirs:
		c.members[e.Name] = e.Size
	}
}

func (c *cfExport) detected() bool {
	var m struct {
		Format string `json:"format"`
	}
	return c.manifest != nil && json.Unmarshal(c.manifest, &m) == nil && m.Format == ExportFormat
}

// The parts of an account export that the adapter reads. A program that reads the format
// ignores the fields that it does not know.
type (
	cfAccount struct {
		Format string `json:"format"`
		Source struct {
			Version string `json:"version"`
			Account string `json:"account"`
		} `json:"source"`
		Customer *struct {
			Name  string `json:"name"`
			Email string `json:"email"`
		} `json:"customer"`
		Users           []json.RawMessage `json:"users"`
		DatabaseServers []json.RawMessage `json:"databaseServers"`
		Networks        []struct {
			Default bool `json:"default"`
		} `json:"networks"`
		Websites []cfWebsite `json:"websites"`
		DNSZones []struct {
			Name    string     `json:"name"`
			Records []cfRecord `json:"records"`
		} `json:"dnsZones"`
		Mail struct {
			Domains []struct {
				Name      string `json:"name"`
				Mailboxes []struct {
					LocalPart  string `json:"localPart"`
					QuotaBytes int64  `json:"quotaBytes"`
				} `json:"mailboxes"`
				Aliases []struct {
					LocalPart    string   `json:"localPart"`
					Destinations []string `json:"destinations"`
				} `json:"aliases"`
			} `json:"domains"`
		} `json:"mail"`
		Warnings []Warning `json:"warnings"`
	}
	cfWebsite struct {
		Name      string `json:"name"`
		Kind      string `json:"kind"`
		DiskBytes int64  `json:"diskBytes"`
		Runtime   struct {
			Family       string            `json:"family"`
			Name         string            `json:"name"`
			PHPVersion   string            `json:"phpVersion"`
			DocumentRoot string            `json:"documentRoot"`
			Extensions   []string          `json:"extensions"`
			PHPIni       map[string]string `json:"phpIni"`
		} `json:"runtime"`
		Domains       []Domain          `json:"domains"`
		Environment   map[string]string `json:"environment"`
		CronJobs      []CronJob         `json:"cronJobs"`
		Deploy        json.RawMessage   `json:"deploy"`
		SFTPKeys      []string          `json:"sftpKeys"`
		Databases     []Database        `json:"databases"`
		DatabaseUsers []DatabaseUser    `json:"databaseUsers"`
		Content       struct {
			Files *Content `json:"files"`
		} `json:"content"`
	}
	cfRecord struct {
		Name     string `json:"name"`
		Type     string `json:"type"`
		TTL      int    `json:"ttl"`
		Value    string `json:"value"`
		Priority *int   `json:"priority"`
		Managed  string `json:"managed"`
	}
)

// cfPHP are the runtime families of Cloudfactory with PHP.
var cfPHP = []string{"apache-php-fpm", "frankenphp", "php"}

// cfPrefix agrees with the start of the name of a database of Cloudfactory: the Unix user of
// its website (up to 9 letters and digits, an underscore and 4 letters and digits) and an
// underscore, for example "shop_x7k2_".
var cfPrefix = regexp.MustCompile(`^[a-z0-9]{1,9}_[a-z0-9]{4}_`)

// cfDatabasePrefix gives the start of the names that all the MariaDB databases of a website
// share, or "". The new panel adds the start of the new website to each name.
func cfDatabasePrefix(w cfWebsite) string {
	prefix := ""
	for _, db := range w.Databases {
		if db.Engine != "mariadb" {
			continue
		}
		p := cfPrefix.FindString(db.Name)
		if p == "" || p == db.Name || (prefix != "" && p != prefix) {
			return ""
		}
		prefix = p
	}
	return prefix
}

func (c *cfExport) account() (Account, error) {
	var acc Account
	warn := func(item, format string, args ...any) {
		acc.Warnings = append(acc.Warnings, Warning{Item: item, Message: fmt.Sprintf(format, args...)})
	}
	var d cfAccount
	if c.description == nil {
		return acc, fmt.Errorf("the account export has no account.json")
	}
	if err := json.Unmarshal(c.description, &d); err != nil {
		return acc, fmt.Errorf("read account.json: %w", err)
	}
	if d.Format != Format {
		return acc, fmt.Errorf("the account export has a description in the format %q, and the import reads %q", short(d.Format), Format)
	}
	acc.Source = Source{Version: d.Source.Version, Account: d.Source.Account}
	if d.Customer != nil {
		acc.Customer = &Customer{Name: d.Customer.Name, Email: d.Customer.Email}
		if d.Customer.Name != "" {
			acc.Source.Account = d.Customer.Name
		}
	}

	// The websites. dbWarned has the export items ("websites/WEBSITE/databases/DATABASE") of
	// the databases that are not MariaDB: the import gives its own warning for each one.
	known := map[string]bool{}
	dbWarned := map[string]bool{}
	for _, w := range d.Websites {
		name := domainName(w.Name)
		if name == "" {
			warn("website:"+short(w.Name), "The panel cannot use this website name. The import does not copy the website.")
			continue
		}
		if w.Kind != "" && w.Kind != "public" {
			warn("website:"+name, "The import makes public websites only. Make this %s website again, and copy its files from the export.", short(w.Kind))
			continue
		}
		family := ""
		switch {
		case slices.Contains(cfPHP, w.Runtime.Family) || w.Runtime.PHPVersion != "":
			family = "php"
		case w.Runtime.Family == "static":
			family = "static"
		default:
			runtime := w.Runtime.Name
			if runtime == "" {
				runtime = w.Runtime.Family
			}
			warn("website:"+name, "The import makes PHP and static websites only. Make this website (%s) again, and copy its files from the export.", short(runtime))
			continue
		}
		site := Website{Name: name, Kind: "public", Runtime: Runtime{Family: family, PHPVersion: phpVersion(w.Runtime.PHPVersion), DocumentRoot: "."}}
		if root := w.Runtime.DocumentRoot; root != "" && validate.DocumentRoot(root) == nil {
			site.Runtime.DocumentRoot = root
		}
		seen := map[string]bool{}
		for _, dom := range w.Domains {
			if n := domainName(dom.Name); n != "" && slices.Contains([]string{"primary", "alias", "redirect"}, dom.Kind) {
				addDomain(&site, seen, n, dom.Kind)
			}
		}
		if !seen[name] {
			site.Domains = append([]Domain{{Name: name, Kind: "primary"}}, site.Domains...)
		}
		for _, j := range w.CronJobs {
			if validate.CronSchedule(j.Schedule) == nil && validate.CommandLine(j.Command) == nil {
				site.CronJobs = append(site.CronJobs, CronJob{Schedule: j.Schedule, Command: j.Command})
			}
		}

		// The files, and the WordPress configuration in them.
		prefix := cfDatabasePrefix(w)
		if f := w.Content.Files; f != nil {
			if size, ok := c.files[f.Path]; ok && cfFiles.MatchString(f.Path) {
				content := &Content{Path: f.Path, Bytes: size}
				for _, x := range f.Exclude {
					if x, ok := CleanName(x); ok {
						content.Exclude = append(content.Exclude, x)
					}
				}
				site.Content = &WebsiteContent{Files: content}
				site.DiskBytes = max(size, w.DiskBytes)
				for _, file := range []string{"wp-config.php", path.Join(site.Runtime.DocumentRoot, "wp-config.php")} {
					if db := wordpressDatabase(c.wp[f.Path+"/"+file]); db != "" {
						site.AppConfig = &AppConfig{Kind: "wordpress", File: file, Database: strings.TrimPrefix(db, prefix)}
						break
					}
				}
			} else {
				warn("website:"+name, "The archive does not have the files of this website. The import makes the website with no files.")
			}
		} else {
			warn("website:"+name, "The export has no files of this website. The import makes the website with no files.")
		}

		// The databases: MariaDB only.
		for _, db := range w.Databases {
			dbWarned["websites/"+w.Name+"/databases/"+db.Name] = db.Engine != "mariadb"
			switch {
			case validate.DatabaseName(db.Name) != nil:
				warn("database:"+short(db.Name), "The panel cannot use this database name. The import does not copy the database.")
			case db.Engine == "postgresql":
				warn("database:"+db.Name, "The import does not copy PostgreSQL databases yet. Make the database in the panel, and restore its dump from the export with pg_restore.")
			case db.Engine != "mariadb":
				engine := map[string]string{"valkey": "Valkey"}[db.Engine]
				if engine == "" {
					engine = short(db.Engine)
				}
				warn("database:"+db.Name, "The import does not make %s databases. Make the database again in the panel.", engine)
			default:
				out := Database{Engine: "mariadb", Name: strings.TrimPrefix(db.Name, prefix)}
				if db.Content != nil {
					if size, ok := c.members[db.Content.Path]; ok && strings.HasPrefix(db.Content.Path, "websites/"+w.Name+"/databases/") {
						out.Content = &Content{Path: db.Content.Path, Bytes: size}
					}
				}
				if out.Content == nil {
					warn("database:"+db.Name, "The export has no dump of this database. The import makes an empty database.")
				}
				site.Databases = append(site.Databases, out)
				known[out.Name] = true
			}
		}
		for _, u := range w.DatabaseUsers {
			user := strings.TrimPrefix(u.Name, prefix)
			if u.Engine != "mariadb" || validate.DatabaseName(user) != nil {
				continue
			}
			grants := map[string]string{}
			for _, g := range u.Grants {
				if g.Privileges == "readwrite" || g.Privileges == "readonly" {
					grants[strings.TrimPrefix(g.Database, prefix)] = g.Privileges
				}
			}
			if g := grantsOf(grants, known); len(g) > 0 {
				site.DatabaseUsers = append(site.DatabaseUsers, DatabaseUser{Engine: "mariadb", Name: user, Access: "website", Grants: g})
			}
		}

		// The settings that the import does not copy.
		var other []string
		if len(w.Environment) > 0 {
			other = append(other, "the environment variables")
		}
		if len(bytes.TrimSpace(w.Deploy)) > 0 && string(bytes.TrimSpace(w.Deploy)) != "null" {
			other = append(other, "the deploy from Git")
		}
		if len(w.SFTPKeys) > 0 {
			other = append(other, "the SFTP keys")
		}
		if len(w.Runtime.Extensions) > 0 || len(w.Runtime.PHPIni) > 0 {
			other = append(other, "the PHP extensions and settings")
		}
		if len(other) > 0 {
			warn("website:"+name, "The import does not copy %s of the website. Set them again after the import.", joinAnd(other))
		}
		acc.Websites = append(acc.Websites, site)
	}

	// The DNS zones, with the records of the user only.
	for _, z := range d.DNSZones {
		zone := domainName(z.Name)
		if zone == "" {
			warn("dns:"+short(z.Name), "The panel cannot use this zone name. The import does not copy the zone.")
			continue
		}
		acc.DNSZones = append(acc.DNSZones, Zone{Name: zone, ZoneFile: cfZone(zone, z.Records)})
	}

	// The mail: the export has no messages and no passwords.
	for _, md := range d.Mail.Domains {
		domain := domainName(md.Name)
		if domain == "" {
			warn("mail:"+short(md.Name), "The panel cannot use this domain name. The import does not copy its mail.")
			continue
		}
		out := MailDomain{Name: domain}
		for _, b := range md.Mailboxes {
			if validLocal(b.LocalPart) {
				out.Mailboxes = append(out.Mailboxes, Mailbox{LocalPart: b.LocalPart, QuotaBytes: max(b.QuotaBytes, 0)})
			}
		}
		for _, a := range md.Aliases {
			var dests []string
			for _, dest := range a.Destinations {
				if dest = strings.ToLower(dest); validate.MailAddress(dest) == nil && !slices.Contains(dests, dest) {
					dests = append(dests, dest)
				}
			}
			if validLocal(a.LocalPart) && len(dests) > 0 && !hasMailbox(&out, a.LocalPart) {
				out.Aliases = append(out.Aliases, Alias{LocalPart: a.LocalPart, Kind: aliasKind(domain, dests), Destinations: dests})
			}
		}
		if len(out.Mailboxes) > 0 || len(out.Aliases) > 0 {
			acc.Mail.Domains = append(acc.Mail.Domains, out)
		}
	}
	if len(acc.Mail.Domains) > 0 {
		warn("mail", "The export has no messages and no passwords of the mailboxes. Each mailbox gets a new password.")
	}

	// The warnings of the export, with no second warning for a database, and the parts of the
	// account that the import does not make.
	for _, w := range d.Warnings {
		if w.Item != "mail" && w.Message != "" && !dbWarned[w.Item] {
			warn(short(w.Item), "%s", w.Message)
		}
	}
	networks := 0
	for _, n := range d.Networks {
		if !n.Default {
			networks++
		}
	}
	if len(d.DatabaseServers) > 0 || networks > 0 {
		warn("account", "The import does not make the database servers and the private networks of the account. Make them again if the websites need them.")
	}
	if len(d.Users) > 0 {
		warn("users", "The import does not make the users of the account. Invite them again.")
	}
	return acc, nil
}

// cfRecordTypes are the record types that a zone of an import can have.
var cfRecordTypes = []string{"A", "AAAA", "CAA", "CNAME", "MX", "NS", "SRV", "TXT"}

// cfZone gives the zone file of the records that the user made, as the panel writes it
// (ZoneText). A record with a character that can start a new line is not in the file.
func cfZone(zone string, records []cfRecord) string {
	var lines []string
	for _, r := range records {
		if r.Managed != "" || !slices.Contains(cfRecordTypes, r.Type) || strings.ContainsAny(r.Name+r.Value, "\x00\r\n") || strings.ContainsAny(r.Name, " \t;") {
			continue
		}
		var data string
		switch r.Type {
		case "CNAME", "NS":
			data = strings.TrimSuffix(r.Value, ".") + "."
		case "MX", "SRV":
			p := 0
			if r.Priority != nil {
				p = *r.Priority
			}
			data = strconv.Itoa(p) + " " + strings.TrimSuffix(r.Value, ".") + "."
		case "TXT":
			data = quoteTXT(r.Value)
		default:
			data = r.Value
		}
		ttl := r.TTL
		if ttl <= 0 {
			ttl = 3600
		}
		lines = append(lines, fmt.Sprintf("%s %d IN %s %s", r.Name, ttl, r.Type, data))
	}
	slices.Sort(lines)
	lines = slices.Compact(lines)
	return "$ORIGIN " + zone + ".\n$TTL 3600\n" + strings.Join(append(lines, ""), "\n")
}

// quoteTXT gives the text of a TXT record as strings of at most 255 bytes.
func quoteTXT(text string) string {
	escape := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	parts := []string{}
	for len(text) > 0 {
		n := min(len(text), 255)
		parts = append(parts, `"`+escape.Replace(text[:n])+`"`)
		text = text[n:]
	}
	if len(parts) == 0 {
		return `""`
	}
	return strings.Join(parts, " ")
}

// joinAnd gives "a", "a and b" or "a, b and c".
func joinAnd(items []string) string {
	if len(items) <= 1 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}
