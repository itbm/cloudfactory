package migrate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

// member is one member of a test archive.
type member struct {
	name string
	body string
	// link is the target of a symbolic link. dir makes a directory.
	link string
	dir  bool
	mode int64
	// flag is a different tar type, for example a FIFO.
	flag byte
}

func tarOf(t *testing.T, members []member) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, m := range members {
		h := &tar.Header{Name: m.name, Mode: 0o644, ModTime: time.Unix(1700000000, 0)}
		if m.mode != 0 {
			h.Mode = m.mode
		}
		switch {
		case m.flag != 0:
			h.Typeflag = m.flag
		case m.dir:
			h.Typeflag, h.Mode = tar.TypeDir, 0o755
		case m.link != "":
			h.Typeflag, h.Linkname = tar.TypeSymlink, m.link
		default:
			h.Typeflag, h.Size = tar.TypeReg, int64(len(m.body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := io.WriteString(tw, m.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func gzipOf(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(b)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zstdOf(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	zw.Write(b)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writeArchive(t *testing.T, name string, b []byte) *Archive {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return &Archive{Path: p}
}

var (
	sha512Hash = "$6$saltsalt$" + strings.Repeat("a", 86)
	md5Hash    = "$1$abcdefgh$abcdefghijklmnopqrstuv"
)

const zone = `$TTL 14400
example.com. 86400 IN SOA ns1.host.net. admin.host.net. 2024010101 3600 1800 1209600 86400
example.com. 86400 IN NS ns1.host.net.
example.com. 14400 IN A 192.0.2.10
www 14400 IN CNAME example.com.
`

const wpConfig = `<?php
define( 'DB_NAME', 'alice_wp' );
define( 'DB_USER', 'alice_wpuser' );
define( 'DB_PASSWORD', 'old\'secret' );
define( 'DB_HOST', 'localhost' );
$table_prefix = 'wp_';
`

// cpanelMembers is a small cPanel account backup. homeTop is the directory of the home
// directory: "homedir" or "homedir.tar" for a nested archive.
func cpanelMembers(home string) []member {
	top := "cpmove-alice/"
	return []member{
		{name: top, dir: true},
		{name: top + "version", body: "pkgacct version: 10\n"},
		{name: top + "cp/alice", body: "USER=alice\nCONTACTEMAIL=alice@example.org\nDNS=example.com\n"},
		{name: top + "homedir_paths", body: "/home/alice\n"},
		{name: top + "userdata/main", body: "---\naddon_domains:\n  shop.net: shop.example.com\nmain_domain: example.com\nparked_domains:\n  - example.org\nsub_domains:\n  - shop.example.com\n  - blog.example.com\n"},
		{name: top + "userdata/example.com", body: "---\ndocumentroot: /home/alice/public_html\nhomedir: /home/alice\nphpversion: ea-php81\nserveralias: www.example.com mail.example.com\n"},
		{name: top + "userdata/shop.example.com", body: "---\ndocumentroot: /home/alice/public_html/shop\nserveralias: shop.net www.shop.net www.shop.example.com\n"},
		{name: top + "userdata/blog.example.com", body: "---\ndocumentroot: /home/alice/blog\nphpversion: alt-php74\n"},
		{name: top + "userdata/cache", body: "ignored"},
		{name: top + "mysql/alice_wp.sql", body: "CREATE TABLE posts (id int);\n"},
		{name: top + "mysql/alice_other.sql", body: "CREATE TABLE t (id int);\n"},
		{name: top + "mysql.sql", body: "GRANT USAGE ON *.* TO 'alice'@'localhost';\nGRANT ALL PRIVILEGES ON `alice\\_wp`.* TO 'alice_wpuser'@'localhost';\nGRANT SELECT ON `alice\\_other`.* TO 'alice_ro'@'localhost';\nGRANT ALL PRIVILEGES ON `alice\\_wp`.* TO 'alice'@'localhost';\n"},
		{name: top + "dnszones/example.com.db", body: zone},
		{name: top + "va/example.com", body: "sales: info@example.com, bob@gmail.com\nhelp: \"|/usr/local/bin/ticket\"\n*: :fail: No Such User Here\n"},
		{name: top + "cron/alice", body: "MAILTO=\"alice@example.org\"\n*/5 * * * * php /home/alice/public_html/cron.php\n@reboot /bin/true\n"},
		{name: top + "sslcerts/example.com.crt", body: "cert"},
		{name: top + "proftpdpasswd", body: "alice:x:1:1::/home/alice:/bin/ftp\n"},
	}
}

// homeMembers are the members of the home directory, with names relative to it.
func homeMembers() []member {
	return []member{
		{name: "public_html/", dir: true},
		{name: "public_html/index.php", body: "<?php echo 'hi';"},
		{name: "public_html/wp-config.php", body: wpConfig},
		{name: "public_html/setuid", body: "x", mode: 0o4755},
		{name: "public_html/link", link: "/etc"},
		{name: "public_html/link/passwd", body: "planted"},
		{name: "public_html/fifo", flag: tar.TypeFifo},
		{name: "public_html/shop/", dir: true},
		{name: "public_html/shop/index.html", body: "shop"},
		{name: "blog/index.php", body: "blog"},
		{name: "etc/example.com/passwd", body: "info:x:1:1::/home/alice/mail/example.com/info:/home/alice\nbad name:x:1:1::/x:/x\n"},
		{name: "etc/example.com/shadow", body: "info:" + sha512Hash + ":19000::::::\n"},
		{name: "etc/example.com/quota", body: "info:1073741824\n"},
		{name: "mail/example.com/info/cur/1700000100.M1P1.host:2,S", body: "Subject: one\r\n\r\nbody\r\n"},
		{name: "mail/example.com/info/new/1700000200.M2P2.host", body: "Subject: two\r\n\r\nbody\r\n"},
		{name: "mail/example.com/info/.Sent/cur/1700000300.M3P3.host:2,SR", body: "Subject: three\r\n\r\nbody\r\n"},
		{name: "mail/example.com/info/.Entw&APw-rfe/cur/1700000400.M4P4.host:2,D", body: "Subject: four\r\n\r\nbody\r\n"},
		{name: "mail/example.com/info/cur/1700000500.M5P5.host:2,ST", body: "Subject: removed\r\n\r\nbody\r\n"},
		{name: "mail/example.com/info/dovecot.index", body: "index"},
		{name: "mail/cur/1700000600.M6.host:2,S", body: "Subject: default\r\n\r\n"},
	}
}

func cpanelArchive(t *testing.T) *Archive {
	t.Helper()
	members := cpanelMembers("homedir")
	for _, m := range homeMembers() {
		m.name = "cpmove-alice/homedir/" + m.name
		members = append(members, m)
	}
	return writeArchive(t, "cpmove-alice.tar.gz", gzipOf(t, tarOf(t, members)))
}

func TestInspectCpanel(t *testing.T) {
	acc, err := Inspect(context.Background(), cpanelArchive(t))
	if err != nil {
		t.Fatal(err)
	}
	checkCpanel(t, acc, "cpmove-alice/homedir")
}

func checkCpanel(t *testing.T, acc Account, home string) {
	t.Helper()
	if acc.Format != Format || acc.Source.Panel != "cpanel" || acc.Source.Account != "alice" || acc.Customer.Email != "alice@example.org" {
		t.Fatalf("source = %+v, customer = %+v", acc.Source, acc.Customer)
	}
	var names []string
	for _, w := range acc.Websites {
		names = append(names, w.Name)
	}
	if !slices.Equal(names, []string{"example.com", "blog.example.com", "shop.net"}) {
		t.Fatalf("websites = %v", names)
	}
	main := acc.Websites[0]
	if main.Runtime != (Runtime{Family: "php", PHPVersion: "8.1", DocumentRoot: "."}) {
		t.Errorf("runtime = %+v", main.Runtime)
	}
	var domains []string
	for _, d := range main.Domains {
		domains = append(domains, d.Name+":"+d.Kind)
	}
	if !slices.Equal(domains, []string{"example.com:primary", "www.example.com:alias", "example.org:alias", "www.example.org:alias"}) {
		t.Errorf("domains = %v", domains)
	}
	if main.Content == nil || main.Content.Files.Path != home+"/public_html" || !slices.Equal(main.Content.Files.Exclude, []string{"shop"}) {
		t.Fatalf("content = %+v", main.Content.Files)
	}
	if main.AppConfig == nil || main.AppConfig.Database != "alice_wp" || main.AppConfig.File != "wp-config.php" {
		t.Errorf("app config = %+v", main.AppConfig)
	}
	if len(main.Databases) != 2 || main.Databases[0].Name != "alice_other" || main.Databases[1].Name != "alice_wp" || main.Databases[1].Content.Path != "cpmove-alice/mysql/alice_wp.sql" {
		t.Errorf("databases = %+v", main.Databases)
	}
	users := map[string]DatabaseUser{}
	for _, u := range main.DatabaseUsers {
		users[u.Name] = u
	}
	if _, ok := users["alice"]; ok || len(users) != 2 {
		t.Errorf("database users = %+v", main.DatabaseUsers)
	}
	if g := users["alice_wpuser"].Grants; len(g) != 1 || g[0] != (Grant{Database: "alice_wp", Privileges: "readwrite"}) {
		t.Errorf("grants of alice_wpuser = %+v", g)
	}
	if g := users["alice_ro"].Grants; len(g) != 1 || g[0].Privileges != "readonly" {
		t.Errorf("grants of alice_ro = %+v", g)
	}
	if len(main.CronJobs) != 1 || main.CronJobs[0].Schedule != "*/5 * * * *" || main.CronJobs[0].Command != "php /home/alice/public_html/cron.php" || main.CronJobs[0].Enabled {
		t.Errorf("cron jobs = %+v", main.CronJobs)
	}
	if blog := acc.Websites[1]; blog.Runtime.PHPVersion != "7.4" || blog.Content.Files.Path != home+"/blog" {
		t.Errorf("blog = %+v %+v", blog.Runtime, blog.Content.Files)
	}
	shop := acc.Websites[2]
	if shop.Content.Files.Path != home+"/public_html/shop" || shop.Domains[0].Name != "shop.net" {
		t.Errorf("shop = %+v %+v", shop.Content.Files, shop.Domains)
	}
	if len(acc.Mail.Domains) != 1 {
		t.Fatalf("mail = %+v", acc.Mail)
	}
	md := acc.Mail.Domains[0]
	if len(md.Mailboxes) != 1 {
		t.Fatalf("mailboxes = %+v", md.Mailboxes)
	}
	box := md.Mailboxes[0]
	if box.LocalPart != "info" || box.PasswordHash != sha512Hash || box.QuotaBytes != 1<<30 || box.Content.Path != home+"/mail/example.com/info" || box.Messages != 5 {
		t.Errorf("mailbox = %+v %+v", box, box.Content)
	}
	if len(md.Aliases) != 1 || md.Aliases[0].LocalPart != "sales" || md.Aliases[0].Kind != "forwarder" || !slices.Equal(md.Aliases[0].Destinations, []string{"info@example.com", "bob@gmail.com"}) {
		t.Errorf("aliases = %+v", md.Aliases)
	}
	if len(acc.DNSZones) != 1 || acc.DNSZones[0].Name != "example.com" || acc.DNSZones[0].ZoneFile != zone {
		t.Errorf("zones = %+v", acc.DNSZones)
	}
	warnings := map[string]bool{}
	for _, w := range acc.Warnings {
		warnings[w.Item] = true
	}
	for _, item := range []string{"cron", "mail:help@example.com", "mail:bad name@example.com", "mail", "files", "certificates", "ftp"} {
		if !warnings[item] {
			t.Errorf("no warning for %q: %+v", item, acc.Warnings)
		}
	}
}

func TestInspectCpanelNestedHome(t *testing.T) {
	members := cpanelMembers("homedir.tar")
	members = append(members, member{name: "cpmove-alice/homedir.tar", body: string(tarOf(t, homeMembers()))})
	a := writeArchive(t, "cpmove-alice.tar", tarOf(t, members))
	acc, err := Inspect(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	checkCpanel(t, acc, "cpmove-alice/homedir.tar")

	var out bytes.Buffer
	st, err := WriteFiles(context.Background(), a, acc.Websites[1].Content.Files.Path, FileOptions{}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if names := tarNames(t, out.Bytes()); !slices.Equal(names, []string{"index.php"}) || st.Files != 1 {
		t.Errorf("files of the nested archive = %v, %+v", names, st)
	}
}

func daMembers() []member {
	return []member{
		{name: "backup/user.conf", body: "username=bob\ndomain=example.net\nemail=bob@example.org\nname=Bob Smith\n"},
		{name: "backup/example.net/domain.conf", body: "domain=example.net\n"},
		{name: "backup/example.net/domain.pointers", body: "example.info=type=alias\nexample.biz=type=pointer\n"},
		{name: "backup/example.net/subdomain.list", body: "dev\n"},
		{name: "backup/example.net/example.net.db", body: strings.ReplaceAll(zone, "example.com", "example.net")},
		{name: "backup/example.net/email/passwd", body: "info:" + md5Hash + ":1:1:info@example.net:/x:/bin/false\nold:{PLAIN}secret:1:1::/x:/x\n"},
		{name: "backup/example.net/email/quota", body: "info:524288000\n"},
		{name: "backup/example.net/email/aliases", body: "bob:bob\nsales:info,other@gmail.com\n"},
		{name: "backup/example.net/email/autoresponder.conf", body: "info=1\n"},
		{name: "backup/bob_shop.conf", body: "accesshosts=0=localhost\nbob=alter_priv=Y&insert_priv=Y\nbob_shop=alter_priv=Y&create_priv=Y&select_priv=Y\nbob_ro=select_priv=Y&insert_priv=N\ndb_collation=utf8mb4_general_ci\n"},
		{name: "backup/bob_shop.sql.gz", body: "dump"},
		{name: "backup/crontab.conf", body: "1=0 3 * * * /usr/local/bin/php /home/bob/domains/example.net/public_html/cron.php\n"},
		{name: "backup/ftp.passwd", body: "bob:x\n"},
		{name: "domains/example.net/public_html/index.php", body: "home"},
		{name: "domains/example.net/public_html/wp-config.php", body: strings.ReplaceAll(wpConfig, "alice_wp'", "bob_shop'")},
		{name: "domains/example.net/public_html/dev/index.html", body: "dev"},
		{name: "domains/example.net/private_html/", dir: true},
		{name: "imap/example.net/info/Maildir/cur/1700000100.M1.host:2,S", body: "Subject: one\r\n\r\n"},
		{name: "imap/example.net/info/Maildir/.Sent/cur/1700000200.M2.host:2,S", body: "Subject: two\r\n\r\n"},
	}
}

func TestInspectDirectAdmin(t *testing.T) {
	a := writeArchive(t, "user.admin.bob.tar.zst", zstdOf(t, tarOf(t, daMembers())))
	acc, err := Inspect(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if acc.Source.Panel != "directadmin" || acc.Source.Account != "bob" || acc.Customer.Name != "Bob Smith" {
		t.Fatalf("source = %+v %+v", acc.Source, acc.Customer)
	}
	if len(acc.Websites) != 2 || acc.Websites[0].Name != "example.net" || acc.Websites[1].Name != "dev.example.net" {
		t.Fatalf("websites = %+v", acc.Websites)
	}
	w := acc.Websites[0]
	var domains []string
	for _, d := range w.Domains {
		domains = append(domains, d.Name+":"+d.Kind)
	}
	if !slices.Equal(domains, []string{"example.net:primary", "www.example.net:alias", "example.info:alias", "www.example.info:alias", "example.biz:redirect", "www.example.biz:redirect"}) {
		t.Errorf("domains = %v", domains)
	}
	if w.Content.Files.Path != "domains/example.net/public_html" || !slices.Equal(w.Content.Files.Exclude, []string{"dev"}) {
		t.Errorf("content = %+v", w.Content.Files)
	}
	if w.Content.Files.Bytes != int64(len("home")+len(strings.ReplaceAll(wpConfig, "alice_wp'", "bob_shop'"))) {
		t.Errorf("content bytes = %d", w.Content.Files.Bytes)
	}
	if len(w.Databases) != 1 || w.Databases[0].Name != "bob_shop" || w.Databases[0].Content.Path != "backup/bob_shop.sql.gz" {
		t.Errorf("databases = %+v", w.Databases)
	}
	users := map[string]string{}
	for _, u := range w.DatabaseUsers {
		users[u.Name] = u.Grants[0].Privileges
	}
	if len(users) != 2 || users["bob_shop"] != "readwrite" || users["bob_ro"] != "readonly" {
		t.Errorf("database users = %+v", w.DatabaseUsers)
	}
	if len(w.CronJobs) != 1 || w.CronJobs[0].Schedule != "0 3 * * *" {
		t.Errorf("cron = %+v", w.CronJobs)
	}
	if acc.Websites[1].Content.Files.Path != "domains/example.net/public_html/dev" {
		t.Errorf("dev content = %+v", acc.Websites[1].Content)
	}
	md := acc.Mail.Domains[0]
	if len(md.Mailboxes) != 2 || md.Mailboxes[0].LocalPart != "info" || md.Mailboxes[0].PasswordHash != md5Hash || md.Mailboxes[0].Messages != 2 || md.Mailboxes[0].Content.Path != "imap/example.net/info/Maildir" {
		t.Errorf("mailboxes = %+v", md.Mailboxes)
	}
	if md.Mailboxes[1].LocalPart != "old" || md.Mailboxes[1].PasswordHash != "" || md.Mailboxes[1].Content != nil {
		t.Errorf("mailbox with no usable hash = %+v", md.Mailboxes[1])
	}
	if len(md.Aliases) != 1 || md.Aliases[0].LocalPart != "sales" || !slices.Equal(md.Aliases[0].Destinations, []string{"info@example.net", "other@gmail.com"}) {
		t.Errorf("aliases = %+v", md.Aliases)
	}
	if len(acc.DNSZones) != 1 || acc.DNSZones[0].Name != "example.net" {
		t.Errorf("zones = %+v", acc.DNSZones)
	}
	items := map[string]bool{}
	for _, w := range acc.Warnings {
		items[w.Item] = true
	}
	for _, item := range []string{"website:example.net", "mail:old@example.net", "mail", "ftp", "files"} {
		if !items[item] {
			t.Errorf("no warning for %q: %+v", item, acc.Warnings)
		}
	}
}

func TestInspectDirectAdminWithTopDirectory(t *testing.T) {
	var members []member
	for _, m := range daMembers() {
		m.name = "user.admin.bob/" + m.name
		members = append(members, m)
	}
	acc, err := Inspect(context.Background(), writeArchive(t, "b.tar.gz", gzipOf(t, tarOf(t, members))))
	if err != nil {
		t.Fatal(err)
	}
	if acc.Websites[0].Content.Files.Path != "user.admin.bob/domains/example.net/public_html" || acc.Websites[0].Databases[0].Content.Path != "user.admin.bob/backup/bob_shop.sql.gz" {
		t.Errorf("paths = %+v %+v", acc.Websites[0].Content.Files, acc.Websites[0].Databases)
	}
}

func TestInspectUnknownArchives(t *testing.T) {
	other := writeArchive(t, "other.tar", tarOf(t, []member{{name: "README", body: "hello"}}))
	if _, err := Inspect(context.Background(), other); !errors.Is(err, ErrUnknown) {
		t.Errorf("a different archive: %v", err)
	}
	plain := writeArchive(t, "notes.txt", []byte(strings.Repeat("not a tar archive\n", 100)))
	if _, err := Inspect(context.Background(), plain); !errors.Is(err, ErrFormat) {
		t.Errorf("a text file: %v", err)
	}
	broken := writeArchive(t, "broken.tar.gz", []byte{0x1f, 0x8b, 0, 0})
	if _, err := Inspect(context.Background(), broken); err == nil {
		t.Error("a broken gzip file: no error")
	}
}

func TestCleanName(t *testing.T) {
	for in, want := range map[string]string{
		"a/b":      "a/b",
		"./a/b/":   "a/b",
		"/a//b":    "a/b",
		".//./a":   "a",
		"a/./b":    "a/b",
		"../a":     "",
		"a/../../": "",
		"a/../b":   "",
		".":        "",
		"":         "",
		"a\x00b":   "",
	} {
		got, ok := CleanName(in)
		if got != want || ok != (want != "") {
			t.Errorf("CleanName(%q) = %q %v, want %q", in, got, ok, want)
		}
	}
}

func TestArchiveSkipsNamesOutside(t *testing.T) {
	a := writeArchive(t, "a.tar", tarOf(t, []member{{name: "../escape", body: "x"}, {name: "/abs/file", body: "y"}, {name: "ok", body: "z"}}))
	var names []string
	if err := a.Walk(context.Background(), func(e Entry, _ io.Reader) error {
		names = append(names, e.Name)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(names, []string{"abs/file", "ok"}) {
		t.Errorf("names = %v", names)
	}
}

func tarNames(t *testing.T, b []byte) []string {
	t.Helper()
	var names []string
	tr := tar.NewReader(bytes.NewReader(b))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return names
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
	}
}

func TestWriteFiles(t *testing.T) {
	a := cpanelArchive(t)
	values := map[string]string{"DB_NAME": "site_a1b2_wp", "DB_USER": "site_a1b2_wp", "DB_PASSWORD": "new'pass", "DB_HOST": "mariadb.internal"}
	var out bytes.Buffer
	st, err := WriteFiles(context.Background(), a, "cpmove-alice/homedir/public_html", FileOptions{
		Exclude: []string{"shop"},
		Rewrite: map[string]func([]byte) ([]byte, bool){"wp-config.php": func(old []byte) ([]byte, bool) { return RewriteWordPress(old, values) }},
	}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if st.Files != 3 || st.Skipped != 2 || !slices.Equal(st.Rewritten, []string{"wp-config.php"}) {
		t.Errorf("stats = %+v", st)
	}
	tr := tar.NewReader(&out)
	got := map[string]*tar.Header{}
	contents := map[string]string{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got[h.Name] = h
		b, _ := io.ReadAll(tr)
		contents[h.Name] = string(b)
	}
	var names []string
	for n := range got {
		names = append(names, n)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"index.php", "link", "setuid", "wp-config.php"}) {
		t.Fatalf("names = %v", names)
	}
	if got["setuid"].Mode != 0o755 {
		t.Errorf("mode of setuid = %o", got["setuid"].Mode)
	}
	if got["link"].Typeflag != tar.TypeSymlink || got["link"].Linkname != "/etc" {
		t.Errorf("link = %+v", got["link"])
	}
	cfg := contents["wp-config.php"]
	if !strings.Contains(cfg, `define( 'DB_PASSWORD', 'new\'pass' )`) || !strings.Contains(cfg, `define( 'DB_HOST', 'mariadb.internal' )`) || !strings.Contains(cfg, "$table_prefix = 'wp_';") || strings.Contains(cfg, "old") {
		t.Errorf("wp-config.php = %s", cfg)
	}
	if got["wp-config.php"].Size != int64(len(cfg)) {
		t.Errorf("size of wp-config.php = %d, content %d", got["wp-config.php"].Size, len(cfg))
	}

	if _, err := WriteFiles(context.Background(), a, "cpmove-alice/homedir/missing", FileOptions{}, io.Discard); !errors.Is(err, ErrNoMember) {
		t.Errorf("a missing directory: %v", err)
	}
}

func TestWithMember(t *testing.T) {
	a := writeArchive(t, "a.tar", tarOf(t, []member{
		{name: "backup/db.sql.gz", body: string(gzipOf(t, []byte("CREATE TABLE t (id int);\n")))},
		{name: "backup/plain.sql", body: "SELECT 1;\n"},
	}))
	for name, want := range map[string]string{"backup/db.sql.gz": "CREATE TABLE t (id int);\n", "backup/plain.sql": "SELECT 1;\n"} {
		var got []byte
		if err := WithMember(context.Background(), a, name, func(r io.Reader) error {
			var err error
			got, err = io.ReadAll(r)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("%s = %q", name, got)
		}
	}
	if err := WithMember(context.Background(), a, "backup/none.sql", func(io.Reader) error { return nil }); !errors.Is(err, ErrNoMember) {
		t.Errorf("a missing member: %v", err)
	}
}

func TestWalkMaildir(t *testing.T) {
	a := cpanelArchive(t)
	got := map[string]Message{}
	if err := WalkMaildir(context.Background(), a, "cpmove-alice/homedir/mail/example.com/info", func(m Message, r io.Reader) error {
		b, _ := io.ReadAll(r)
		if !strings.HasPrefix(string(b), "Subject:") {
			t.Errorf("%s: content %q", m.Name, b)
		}
		got[strings.Join(m.Folder, "/")] = m
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("folders = %v", got)
	}
	if m := got["Sent"]; !slices.Equal(m.Keywords, []string{"$seen", "$answered"}) || m.Received.Unix() != 1700000300 {
		t.Errorf("Sent = %+v", m)
	}
	if m := got["Entwürfe"]; !slices.Equal(m.Keywords, []string{"$draft"}) {
		t.Errorf("Entwürfe = %+v", m)
	}
	// The inbox has one message in "cur" and one in "new"; the message with "T" is not given.
	count := 0
	WalkMaildir(context.Background(), a, "cpmove-alice/homedir/mail/example.com/info", func(m Message, _ io.Reader) error {
		if len(m.Folder) == 0 {
			count++
			if strings.HasPrefix(m.Name, "new/") && slices.Contains(m.Keywords, "$seen") {
				t.Errorf("a new message is seen: %+v", m)
			}
		}
		return nil
	})
	if count != 2 {
		t.Errorf("inbox messages = %d", count)
	}
}

func TestDecodeFolder(t *testing.T) {
	for in, want := range map[string]string{
		"Sent":            "Sent",
		"Entw&APw-rfe":    "Entwürfe",
		"&-":              "&",
		"A&-B":            "A&B",
		"&ZeVnLIqe-":      "日本語",
		"broken&abc":      "broken&abc",
		"&!!!-":           "&!!!-",
		"Ma&AO4-tre&AOk-": "Maître" + "é",
	} {
		if got := DecodeFolder(in); got != want {
			t.Errorf("DecodeFolder(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRewriteWordPress(t *testing.T) {
	values := map[string]string{"DB_NAME": "n", "DB_USER": "u", "DB_PASSWORD": `p\'`, "DB_HOST": "h"}
	out, ok := RewriteWordPress([]byte(wpConfig), values)
	if !ok {
		t.Fatal("not all four settings")
	}
	if !strings.Contains(string(out), `define( 'DB_PASSWORD', 'p\\\'' )`) {
		t.Errorf("config = %s", out)
	}
	if _, ok := RewriteWordPress([]byte("<?php define('DB_NAME', 'x');"), values); ok {
		t.Error("a file with one setting: ok")
	}
}

func TestHelpers(t *testing.T) {
	for in, want := range map[string]string{"ea-php81": "8.1", "alt-php74": "7.4", "php80": "8.0", "8.2": "8.2", "inherit": "", "ea-php": ""} {
		if got := phpVersion(in); got != want {
			t.Errorf("phpVersion(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		sha512Hash:                    sha512Hash,
		"{SHA512-CRYPT}" + sha512Hash: sha512Hash,
		md5Hash:                       md5Hash,
		"{PLAIN}secret":               "",
		"!" + sha512Hash:              "",
		"*":                           "",
	} {
		if got := passwordHash(in); got != want {
			t.Errorf("passwordHash(%q) = %q, want %q", in, got, want)
		}
	}
	if s, c, ok, _ := cronLine("0  3 * * 1   /usr/bin/php  -q x.php"); !ok || s != "0 3 * * 1" || c != "/usr/bin/php  -q x.php" {
		t.Errorf("cronLine = %q %q %v", s, c, ok)
	}
	if _, _, ok, reason := cronLine("@daily /bin/true"); ok || reason == "" {
		t.Errorf("@daily: ok %v reason %q", ok, reason)
	}
	if _, _, ok, reason := cronLine("SHELL=/bin/bash"); ok || reason != "" {
		t.Errorf("a variable: ok %v reason %q", ok, reason)
	}
	if !IsTarName("a/homedir.TAR") || !IsTarName("x.tar.zst") || IsTarName("x.sql.gz") {
		t.Error("IsTarName")
	}
}

func TestBound(t *testing.T) {
	acc := Account{}
	for range maxWebsites + 3 {
		acc.Websites = append(acc.Websites, Website{Name: "a"})
	}
	acc.DNSZones = []Zone{{Name: "big.example", ZoneFile: strings.Repeat("x", maxZoneFile+1)}}
	bound(&acc)
	if len(acc.Websites) != maxWebsites || len(acc.DNSZones) != 0 || len(acc.Warnings) != 2 {
		t.Errorf("websites %d, zones %d, warnings %+v", len(acc.Websites), len(acc.DNSZones), acc.Warnings)
	}
	if acc.Websites[0].Domains == nil || acc.Mail.Domains == nil {
		t.Error("nil lists stay nil")
	}
}
