package migrate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

// Format is the name of the description format. The account export of the panel writes the
// same format (docs/imports.md).
const Format = "cf-account/1"

// Account is the description of one account of a different panel.
type Account struct {
	Format   string    `json:"format"`
	Source   Source    `json:"source"`
	Customer *Customer `json:"customer,omitempty"`
	Websites []Website `json:"websites"`
	DNSZones []Zone    `json:"dnsZones"`
	Mail     Mail      `json:"mail"`
	Warnings []Warning `json:"warnings"`
}

// Source tells where the description comes from.
type Source struct {
	// Panel is "cpanel" or "directadmin".
	Panel   string `json:"panel"`
	Version string `json:"version,omitempty"`
	// Account is the name of the account on the old panel.
	Account string `json:"account"`
}

// Customer is the owner of the account on the old panel.
type Customer struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
}

// Website is one website of the account.
type Website struct {
	Name          string         `json:"name"`
	Kind          string         `json:"kind"`
	Runtime       Runtime        `json:"runtime"`
	Domains       []Domain       `json:"domains"`
	CronJobs      []CronJob      `json:"cronJobs"`
	Databases     []Database     `json:"databases"`
	DatabaseUsers []DatabaseUser `json:"databaseUsers"`
	// Content is the content of the website in the archive.
	Content *WebsiteContent `json:"content,omitempty"`
	// DiskBytes is the size of the files after extraction.
	DiskBytes int64 `json:"diskBytes"`
	// AppConfig is a configuration file with the database settings that the import can
	// change, for example wp-config.php.
	AppConfig *AppConfig `json:"appConfig,omitempty"`
}

// WebsiteContent is the content of a website in the archive. The account export of the panel
// writes the same field (docs/account-export.md).
type WebsiteContent struct {
	// Files is the document root of the website in the archive.
	Files *Content `json:"files"`
}

// Runtime is the runtime of a website on the old panel.
type Runtime struct {
	// Family is "php" or "static".
	Family string `json:"family"`
	// PHPVersion is for example "8.1". Empty means the default of the old panel.
	PHPVersion string `json:"phpVersion,omitempty"`
	// DocumentRoot is the web directory, relative to the files.
	DocumentRoot string `json:"documentRoot,omitempty"`
}

// Domain is a domain of a website.
type Domain struct {
	Name string `json:"name"`
	// Kind is "primary", "alias" or "redirect".
	Kind string `json:"kind"`
}

// CronJob is a cron job of the account. An import makes it with Enabled false: its command
// names the paths of the old server.
type CronJob struct {
	Schedule string `json:"schedule"`
	Command  string `json:"command"`
	Enabled  bool   `json:"enabled"`
}

// Database is a database of a website.
type Database struct {
	// Engine is "mariadb" or "postgresql".
	Engine  string   `json:"engine"`
	Name    string   `json:"name"`
	Content *Content `json:"content,omitempty"`
}

// DatabaseUser is a database user of a website.
type DatabaseUser struct {
	Engine string `json:"engine"`
	Name   string `json:"name"`
	// Access is "website".
	Access string  `json:"access"`
	Grants []Grant `json:"grants"`
}

// Grant is the access of a database user to one database.
type Grant struct {
	Database string `json:"database"`
	// Privileges is "readwrite" or "readonly".
	Privileges string `json:"privileges"`
}

// Content is a part of the archive.
type Content struct {
	// Path is a directory or a file in the archive. A path through a member with the name of
	// a tar archive goes into that nested archive.
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
	// Exclude are directories in Path that are not a part of the content, for example the
	// document root of a different website.
	Exclude []string `json:"exclude,omitempty"`
}

// AppConfig is a configuration file of an application with the database settings.
type AppConfig struct {
	// Kind is "wordpress".
	Kind string `json:"kind"`
	// File is the path of the file, relative to the files of the website.
	File string `json:"file"`
	// Database is the name of the database in the file, on the old panel.
	Database string `json:"database"`
}

// Zone is a DNS zone of the account.
type Zone struct {
	Name string `json:"name"`
	// ZoneFile is the zone in the text format of RFC 1035.
	ZoneFile string `json:"zoneFile"`
}

// Mail is the email of the account.
type Mail struct {
	Domains []MailDomain `json:"domains"`
}

// MailDomain is a mail domain with its mailboxes and forwarders.
type MailDomain struct {
	Name      string    `json:"name"`
	Mailboxes []Mailbox `json:"mailboxes"`
	Aliases   []Alias   `json:"aliases"`
}

// Mailbox is one mailbox.
type Mailbox struct {
	LocalPart string `json:"localPart"`
	// QuotaBytes is the size limit. 0 means no limit on the old panel.
	QuotaBytes int64 `json:"quotaBytes"`
	// PasswordHash is the hash of the password on the old panel: bcrypt, SHA-512-crypt or
	// MD5-crypt. Empty means that the import makes a new password.
	PasswordHash string `json:"passwordHash,omitempty"`
	// Content is the Maildir of the mailbox.
	Content  *Content `json:"content,omitempty"`
	Messages int      `json:"messages"`
}

// Alias is a forwarder: mail to the address goes to the destinations.
type Alias struct {
	LocalPart string `json:"localPart"`
	// Kind is "alias" when all destinations are mailboxes of the same domain, else "forwarder".
	Kind         string   `json:"kind"`
	Destinations []string `json:"destinations"`
}

// Warning tells about a part of the account that the import does not copy, or copies with a
// change.
type Warning struct {
	Item    string `json:"item"`
	Message string `json:"message"`
}

// Limits of a description. The job result goes to the panel in one request.
const (
	maxWebsites  = 200
	maxDatabases = 500
	maxMailboxes = 2000
	maxAliases   = 5000
	maxZones     = 200
	maxCron      = 500
	maxWarnings  = 200
	// maxZoneFile is the largest zone file, maxZoneTotal the sum of all zone files.
	maxZoneFile  = 1 << 20
	maxZoneTotal = 4 << 20
	// maxSmall is the largest file of settings that an adapter reads, and maxSmallTotal the
	// sum of all of them.
	maxSmall      = 4 << 20
	maxSmallTotal = 64 << 20
)

// adapter reads the archive of one panel. Each adapter sees each member of the archive in one
// walk, so the archive is read only one time.
type adapter interface {
	// name is the name of the panel, for example "cpanel".
	name() string
	// wants reports whether the adapter reads the content of a file. The walk reads only
	// small files.
	wants(e Entry) bool
	// add records a member. content is the content of a file that the adapter wants, or nil.
	add(e Entry, content []byte)
	// detected reports whether the archive is a backup of the panel.
	detected() bool
	// account gives the description after the walk.
	account() (Account, error)
}

// ErrUnknown is the error of an archive that no adapter knows.
var ErrUnknown = errors.New("the archive is not a cPanel or DirectAdmin account backup")

func adapters() []adapter {
	return []adapter{newCpanel(), newDirectAdmin()}
}

// Inspect reads the archive one time and gives the description of the account. With no
// Expand, the walk reads the nested archives that the adapters know (InspectExpand).
func Inspect(ctx context.Context, a *Archive) (Account, error) {
	all := adapters()
	var budget int64 = maxSmallTotal
	walk := *a
	if walk.Expand == nil {
		walk.Expand = InspectExpand
	}
	err := walk.Walk(ctx, func(e Entry, r io.Reader) error {
		var content []byte
		var want []adapter
		for _, ad := range all {
			if ad.wants(e) {
				want = append(want, ad)
			}
		}
		if len(want) > 0 && e.Type == TypeFile && e.Size <= maxSmall && e.Size <= budget {
			b, err := readSmall(r, maxSmall)
			if err != nil {
				return fmt.Errorf("%s: %w", e.Name, err)
			}
			budget -= int64(len(b))
			content = b
		}
		for _, ad := range all {
			if content != nil && slices.Contains(want, ad) {
				ad.add(e, content)
			} else {
				ad.add(e, nil)
			}
		}
		return nil
	})
	if err != nil {
		return Account{}, err
	}
	for _, ad := range all {
		if ad.detected() {
			acc, err := ad.account()
			if err != nil {
				return Account{}, err
			}
			acc.Format = Format
			acc.Source.Panel = ad.name()
			bound(&acc)
			return acc, nil
		}
	}
	return Account{}, ErrUnknown
}

// InspectExpand selects the nested archives of the known backup formats: the home directory
// of an older cPanel backup ("cpmove-USER/homedir.tar").
func InspectExpand(name string) bool {
	top, rest, ok := strings.Cut(name, "/")
	return ok && top != "" && rest == "homedir.tar"
}

// bound keeps a description in the limits, with a warning for each part that it leaves out.
func bound(a *Account) {
	warn := func(item, msg string) { a.Warnings = append(a.Warnings, Warning{Item: item, Message: msg}) }
	if len(a.Websites) > maxWebsites {
		warn("websites", fmt.Sprintf("The account has %d websites. The import shows the first %d.", len(a.Websites), maxWebsites))
		a.Websites = a.Websites[:maxWebsites]
	}
	databases, cron := 0, 0
	for i := range a.Websites {
		w := &a.Websites[i]
		if databases+len(w.Databases) > maxDatabases {
			w.Databases = w.Databases[:max(0, maxDatabases-databases)]
			warn("databases", fmt.Sprintf("The import shows the first %d databases.", maxDatabases))
		}
		databases += len(w.Databases)
		if cron+len(w.CronJobs) > maxCron {
			w.CronJobs = w.CronJobs[:max(0, maxCron-cron)]
			warn("cronJobs", fmt.Sprintf("The import shows the first %d cron jobs.", maxCron))
		}
		cron += len(w.CronJobs)
	}
	total := 0
	zones := a.DNSZones[:0]
	for _, z := range a.DNSZones {
		if len(zones) >= maxZones || total+len(z.ZoneFile) > maxZoneTotal || len(z.ZoneFile) > maxZoneFile {
			warn("dns:"+z.Name, "The zone file is too large for the import, or the account has too many zones.")
			continue
		}
		total += len(z.ZoneFile)
		zones = append(zones, z)
	}
	a.DNSZones = zones
	boxes, aliases := 0, 0
	for i := range a.Mail.Domains {
		d := &a.Mail.Domains[i]
		if boxes+len(d.Mailboxes) > maxMailboxes {
			d.Mailboxes = d.Mailboxes[:max(0, maxMailboxes-boxes)]
			warn("mail:"+d.Name, fmt.Sprintf("The import shows the first %d mailboxes.", maxMailboxes))
		}
		boxes += len(d.Mailboxes)
		if aliases+len(d.Aliases) > maxAliases {
			d.Aliases = d.Aliases[:max(0, maxAliases-aliases)]
			warn("mail:"+d.Name, fmt.Sprintf("The import shows the first %d forwarders.", maxAliases))
		}
		aliases += len(d.Aliases)
	}
	if len(a.Warnings) > maxWarnings {
		n := len(a.Warnings)
		a.Warnings = append(a.Warnings[:maxWarnings-1], Warning{Item: "warnings", Message: fmt.Sprintf("%d more warnings are not shown.", n-maxWarnings+1)})
	}
	for i := range a.Websites {
		w := &a.Websites[i]
		w.Domains = nonNil(w.Domains)
		w.CronJobs = nonNil(w.CronJobs)
		w.Databases = nonNil(w.Databases)
		w.DatabaseUsers = nonNil(w.DatabaseUsers)
		for j := range w.DatabaseUsers {
			w.DatabaseUsers[j].Grants = nonNil(w.DatabaseUsers[j].Grants)
		}
	}
	for i := range a.Mail.Domains {
		a.Mail.Domains[i].Mailboxes = nonNil(a.Mail.Domains[i].Mailboxes)
		a.Mail.Domains[i].Aliases = nonNil(a.Mail.Domains[i].Aliases)
	}
	a.Websites, a.DNSZones, a.Mail.Domains, a.Warnings = nonNil(a.Websites), nonNil(a.DNSZones), nonNil(a.Mail.Domains), nonNil(a.Warnings)
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// sizes adds the size of each file to each directory above it, up to a depth below a root.
// A content part then has its size with no second walk.
type sizes struct {
	root  string
	depth int
	bytes map[string]int64
	count map[string]int
}

// maxDirs is the largest number of directories that sizes records.
const maxDirs = 200000

func newSizes(root string, depth int) *sizes {
	return &sizes{root: root, depth: depth, bytes: map[string]int64{}, count: map[string]int{}}
}

func (s *sizes) add(e Entry) {
	if e.Type != TypeFile || !Within(e.Name, s.root) {
		return
	}
	rel := Rel(e.Name, s.root)
	parts := strings.Split(rel, "/")
	dir := s.root
	for i := 0; i < len(parts)-1 && i < s.depth; i++ {
		if dir == "" {
			dir = parts[i]
		} else {
			dir += "/" + parts[i]
		}
		if _, ok := s.bytes[dir]; !ok && len(s.bytes) >= maxDirs {
			return
		}
		s.bytes[dir] += e.Size
		s.count[dir]++
	}
}

// of gives the size of a directory, and false when sizes does not know it.
func (s *sizes) of(dir string) (int64, bool) {
	b, ok := s.bytes[dir]
	return b, ok
}
