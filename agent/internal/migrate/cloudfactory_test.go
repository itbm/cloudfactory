package migrate

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"testing"
)

// cfAccountJSON is the description of an account export with five websites, a DNS zone and a
// mail domain, as the panel writes it (api/app/Services/Exports/Description.php).
const cfAccountJSON = `{
  "format": "cf-account/1",
  "source": {"panel": "cloudfactory", "version": "0.1.0", "account": "01jbexampleaccount", "exportedAt": "2026-11-22T21:03:00+00:00"},
  "customer": {"name": "Acme Ltd", "email": "ops@acme.example", "role": "owner"},
  "plan": {"name": "Business"},
  "users": [{"name": "Ada", "email": "ada@acme.example", "role": "owner"}],
  "websites": [
    {"name": "blog.example.com", "kind": "public", "application": "WordPress", "diskBytes": 10,
     "runtime": {"family": "apache-php-fpm", "name": "Apache + PHP-FPM", "image": "ghcr.io/itbm/apache-php8.2:1", "custom": false, "phpVersion": "8.2", "version": null, "extensions": [], "phpIni": {}, "workerMode": false, "documentRoot": ".", "command": null, "installCommand": null},
     "limits": null, "domains": [{"name": "blog.example.com", "kind": "primary"}], "environment": {}, "cronJobs": [], "deploy": null, "sftpKeys": [],
     "databases": [{"engine": "mariadb", "name": "blog_q9z1_wp", "server": null, "content": {"path": "websites/blog.example.com/databases/mariadb-blog_q9z1_wp.sql.gz", "bytes": 40}}],
     "databaseUsers": [{"engine": "mariadb", "name": "blog_q9z1_wp", "access": "website", "grants": [{"database": "blog_q9z1_wp", "privileges": "readwrite"}]}],
     "content": {"files": {"path": "websites/blog.example.com/files.tar.gz", "bytes": 100, "exclude": []}}},
    {"name": "internal.example.com", "kind": "private", "runtime": {"family": "frankenphp", "phpVersion": "8.4", "documentRoot": "public"}, "domains": [], "content": {"files": null}},
    {"name": "node.example.com", "kind": "public", "runtime": {"family": "node", "name": "Node.js 22", "phpVersion": null, "version": "22", "documentRoot": "."}, "domains": [], "content": {"files": null}},
    {"name": "shop.example.com", "kind": "public", "application": "Laravel", "diskBytes": 5000,
     "runtime": {"family": "frankenphp", "phpVersion": "8.4", "extensions": ["intl"], "phpIni": {"memory_limit": "256M"}, "documentRoot": "public"},
     "domains": [{"name": "old-shop.example.net", "kind": "redirect"}, {"name": "shop.example.com", "kind": "primary"}, {"name": "www.shop.example.com", "kind": "alias"}],
     "environment": {"APP_KEY": "base64:x"},
     "cronJobs": [{"schedule": "* * * * *", "command": "php artisan schedule:run", "enabled": true}, {"schedule": "bad", "command": "x", "enabled": true}],
     "deploy": {"repository": "git@github.com:acme/shop.git", "branch": "main", "secrets": ["NPM_TOKEN"]},
     "sftpKeys": ["ssh-ed25519 AAAAC3Nza ada@laptop"],
     "databases": [
       {"engine": "mariadb", "name": "shop_x7k2_shop", "server": null, "content": {"path": "websites/shop.example.com/databases/mariadb-shop_x7k2_shop.sql.gz", "bytes": 1}},
       {"engine": "mariadb", "name": "shop_x7k2_empty", "server": null, "content": null},
       {"engine": "postgresql", "name": "shop_x7k2_reports", "server": "pg", "content": {"path": "websites/shop.example.com/databases/postgresql-shop_x7k2_reports.pgdump", "bytes": 1}},
       {"engine": "valkey", "name": "shop_x7k2_cache", "server": null, "content": null}],
     "databaseUsers": [
       {"engine": "mariadb", "name": "shop_x7k2_shop", "access": "website", "grants": [{"database": "shop_x7k2_shop", "privileges": "readwrite"}]},
       {"engine": "mariadb", "name": "shop_x7k2_ro", "access": "network", "grants": [{"database": "shop_x7k2_shop", "privileges": "readonly"}, {"database": "shop_x7k2_reports", "privileges": "readonly"}]},
       {"engine": "postgresql", "name": "shop_x7k2_reports", "access": "website", "grants": [{"database": "shop_x7k2_reports", "privileges": "readwrite"}]}],
     "content": {"files": {"path": "websites/shop.example.com/files.tar.gz", "bytes": 300, "exclude": ["storage/logs"]}}},
    {"name": "static.example.com", "kind": "public", "runtime": {"family": "static", "documentRoot": "."}, "domains": [{"name": "static.example.com", "kind": "primary"}], "content": {"files": null}}
  ],
  "databaseServers": [{"name": "pg", "engine": "postgresql", "plan": null, "limits": null}],
  "networks": [{"name": "default", "default": true, "members": {"websites": [], "databaseServers": []}}],
  "dnsZones": [{"name": "example.com", "dnssec": true, "content": {"path": "dns/example.com.zone", "bytes": 0}, "records": [
    {"name": "@", "type": "A", "ttl": 3600, "value": "203.0.113.5", "priority": null, "managed": "website"},
    {"name": "@", "type": "MX", "ttl": 3600, "value": "mail.example.com", "priority": 10, "managed": null},
    {"name": "@", "type": "NS", "ttl": 3600, "value": "ns1.panel.example", "priority": null, "managed": "ns"},
    {"name": "@", "type": "TXT", "ttl": 300, "value": "v=spf1 \"a\" -all", "priority": null, "managed": null},
    {"name": "_sip._tcp", "type": "SRV", "ttl": 3600, "value": "5 5060 sip.example.com", "priority": 20, "managed": null},
    {"name": "bad", "type": "A", "ttl": 3600, "value": "192.0.2.1\n$INCLUDE /etc/passwd", "priority": null, "managed": null},
    {"name": "docs", "type": "CNAME", "ttl": 3600, "value": "docs.host.example", "priority": null, "managed": null},
    {"name": "shop", "type": "A", "ttl": 3600, "value": "203.0.113.5", "priority": null, "managed": "website"}]}],
  "mail": {"domains": [{"name": "example.com",
    "mailboxes": [{"localPart": "ops", "quotaBytes": 1073741824, "usedBytes": 5, "publicKey": "x", "content": null}, {"localPart": "sales", "quotaBytes": 0, "usedBytes": 0, "publicKey": null, "content": null}],
    "aliases": [{"localPart": "info", "kind": "alias", "destinations": ["ops@example.com"]}, {"localPart": "partner", "kind": "forwarder", "destinations": ["Bob@Partner.example", "not an address"]}]}]},
  "warnings": [
    {"item": "websites/shop.example.com/databases/shop_x7k2_cache", "message": "The export does not have the data of Valkey databases."},
    {"item": "mail", "message": "The export does not have the messages of the mailboxes or their passwords."}]
}`

func cfArchive(t *testing.T) *Archive {
	t.Helper()
	shop := tarOf(t, []member{
		{name: "public/", dir: true},
		{name: "public/index.php", body: "<?php echo 1;"},
		{name: ".env", body: "APP_KEY=x"},
		{name: "storage/logs/laravel.log", body: "log"},
	})
	blog := tarOf(t, []member{
		{name: "index.php", body: "blog"},
		{name: "wp-config.php", body: strings.ReplaceAll(wpConfig, "alice_wp", "blog_q9z1_wp")},
	})
	return writeArchive(t, "acme-ltd-export-20261122-2103.tar", tarOf(t, []member{
		{name: "manifest.json", body: `{"format": "cf-export/1", "description": "account.json", "files": []}`},
		{name: "account.json", body: cfAccountJSON},
		{name: "README.txt", body: "An account export of Cloudfactory."},
		{name: "dns/example.com.zone", body: "$ORIGIN example.com.\n"},
		{name: "websites/blog.example.com/databases/mariadb-blog_q9z1_wp.sql.gz", body: string(gzipOf(t, []byte("CREATE TABLE wp_posts (id int);\n")))},
		{name: "websites/blog.example.com/files.tar.gz", body: string(gzipOf(t, blog))},
		{name: "websites/shop.example.com/databases/mariadb-shop_x7k2_shop.sql.gz", body: string(gzipOf(t, []byte("CREATE TABLE orders (id int);\n")))},
		{name: "websites/shop.example.com/databases/postgresql-shop_x7k2_reports.pgdump", body: "PGDMP"},
		{name: "websites/shop.example.com/files.tar.gz", body: string(gzipOf(t, shop))},
	}))
}

func TestInspectCloudfactoryExport(t *testing.T) {
	a := cfArchive(t)
	acc, err := Inspect(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if acc.Source != (Source{Panel: "cloudfactory", Version: "0.1.0", Account: "Acme Ltd"}) || *acc.Customer != (Customer{Name: "Acme Ltd", Email: "ops@acme.example"}) {
		t.Errorf("source %+v, customer %+v", acc.Source, acc.Customer)
	}
	var names []string
	for _, w := range acc.Websites {
		names = append(names, w.Name)
	}
	if strings.Join(names, " ") != "blog.example.com shop.example.com static.example.com" {
		t.Fatalf("websites: %v", names)
	}
	blog, shop, static := acc.Websites[0], acc.Websites[1], acc.Websites[2]

	if blog.Runtime != (Runtime{Family: "php", PHPVersion: "8.2", DocumentRoot: "."}) || blog.AppConfig == nil || *blog.AppConfig != (AppConfig{Kind: "wordpress", File: "wp-config.php", Database: "wp"}) {
		t.Errorf("blog: %+v %+v", blog.Runtime, blog.AppConfig)
	}
	// The new panel adds the Unix user of the new website to the name of each database.
	if len(blog.Databases) != 1 || blog.Databases[0].Name != "wp" || blog.Databases[0].Content == nil || blog.Databases[0].Content.Path != "websites/blog.example.com/databases/mariadb-blog_q9z1_wp.sql.gz" || blog.Databases[0].Content.Bytes == 0 {
		t.Errorf("blog databases: %+v", blog.Databases)
	}

	if shop.Runtime != (Runtime{Family: "php", PHPVersion: "8.4", DocumentRoot: "public"}) || shop.AppConfig != nil {
		t.Errorf("shop: %+v", shop.Runtime)
	}
	b, _ := json.Marshal(shop.Domains)
	if string(b) != `[{"name":"old-shop.example.net","kind":"redirect"},{"name":"shop.example.com","kind":"primary"},{"name":"www.shop.example.com","kind":"alias"}]` {
		t.Errorf("domains: %s", b)
	}
	if len(shop.CronJobs) != 1 || shop.CronJobs[0] != (CronJob{Schedule: "* * * * *", Command: "php artisan schedule:run"}) {
		t.Errorf("cron: %+v", shop.CronJobs)
	}
	files := shop.Content.Files
	if files.Path != "websites/shop.example.com/files.tar.gz" || !slices.Equal(files.Exclude, []string{"storage/logs"}) || files.Bytes != int64(len("<?php echo 1;")+len("APP_KEY=x")+len("log")) || shop.DiskBytes != 5000 {
		t.Errorf("files: %+v, disk %d", files, shop.DiskBytes)
	}
	b, _ = json.Marshal(shop.Databases)
	if !strings.HasPrefix(string(b), `[{"engine":"mariadb","name":"shop","content":{"path":"websites/shop.example.com/databases/mariadb-shop_x7k2_shop.sql.gz","bytes":`) || !strings.HasSuffix(string(b), `},{"engine":"mariadb","name":"empty"}]`) {
		t.Errorf("databases: %s", b)
	}
	b, _ = json.Marshal(shop.DatabaseUsers)
	if string(b) != `[{"engine":"mariadb","name":"shop","access":"website","grants":[{"database":"shop","privileges":"readwrite"}]},{"engine":"mariadb","name":"ro","access":"website","grants":[{"database":"shop","privileges":"readonly"}]}]` {
		t.Errorf("database users: %s", b)
	}
	if static.Runtime.Family != "static" || static.Content != nil {
		t.Errorf("static: %+v", static)
	}

	// The zone has the records of the user, with no record that the old panel made.
	want := "$ORIGIN example.com.\n$TTL 3600\n" +
		"@ 300 IN TXT \"v=spf1 \\\"a\\\" -all\"\n" +
		"@ 3600 IN MX 10 mail.example.com.\n" +
		"_sip._tcp 3600 IN SRV 20 5 5060 sip.example.com.\n" +
		"docs 3600 IN CNAME docs.host.example.\n"
	if len(acc.DNSZones) != 1 || acc.DNSZones[0].Name != "example.com" || acc.DNSZones[0].ZoneFile != want {
		t.Errorf("zones: %+v\n%s", acc.DNSZones, acc.DNSZones[0].ZoneFile)
	}

	b, _ = json.Marshal(acc.Mail)
	if string(b) != `{"domains":[{"name":"example.com","mailboxes":[{"localPart":"ops","quotaBytes":1073741824,"messages":0},{"localPart":"sales","quotaBytes":0,"messages":0}],"aliases":[{"localPart":"info","kind":"alias","destinations":["ops@example.com"]},{"localPart":"partner","kind":"forwarder","destinations":["bob@partner.example"]}]}]}` {
		t.Errorf("mail: %s", b)
	}

	var warnings []string
	for _, w := range acc.Warnings {
		warnings = append(warnings, w.Item+": "+w.Message)
	}
	all := strings.Join(warnings, "\n")
	for _, s := range []string{
		"website:internal.example.com: The import makes public websites only. Make this private website again",
		"website:node.example.com: The import makes PHP and static websites only. Make this website (Node.js 22) again",
		"website:shop.example.com: The import does not copy the environment variables, the deploy from Git, the SFTP keys and the PHP extensions and settings of the website.",
		"website:static.example.com: The export has no files of this website.",
		"database:shop_x7k2_reports: The import does not copy PostgreSQL databases yet.",
		"database:shop_x7k2_cache: The import does not make Valkey databases.",
		"database:shop_x7k2_empty: The export has no dump of this database.",
		"mail: The export has no messages and no passwords of the mailboxes. Each mailbox gets a new password.",
		"account: The import does not make the database servers and the private networks",
		"users: The import does not make the users of the account.",
	} {
		if !strings.Contains(all, s) {
			t.Errorf("no warning %q:\n%s", s, all)
		}
	}
	if strings.Count(all, "mail: ") != 1 || strings.Contains(all, "websites/shop.example.com/databases/shop_x7k2_cache") {
		t.Errorf("one warning about the mail and the Valkey database:\n%s", all)
	}

	// The jobs read the content with the paths of the description.
	var dump bytes.Buffer
	if err := WithMember(context.Background(), a, shop.Databases[0].Content.Path, func(r io.Reader) error {
		_, err := io.Copy(&dump, r)
		return err
	}); err != nil || dump.String() != "CREATE TABLE orders (id int);\n" {
		t.Errorf("dump %q, %v", dump.String(), err)
	}
	var out bytes.Buffer
	st, err := WriteFiles(context.Background(), a, files.Path, FileOptions{Exclude: files.Exclude}, &out)
	if err != nil || !slices.Equal(tarNames(t, out.Bytes()), []string{"public/", "public/index.php", ".env"}) {
		t.Errorf("files %+v, %v, %v", st, err, tarNames(t, out.Bytes()))
	}
}

func TestInspectCloudfactoryExportFormat(t *testing.T) {
	a := writeArchive(t, "x.tar", tarOf(t, []member{
		{name: "manifest.json", body: `{"format": "cf-export/1"}`},
		{name: "account.json", body: `{"format": "cf-account/2"}`},
	}))
	if _, err := Inspect(context.Background(), a); err == nil || !strings.Contains(err.Error(), `"cf-account/2"`) {
		t.Errorf("a description of a different format: %v", err)
	}
	a = writeArchive(t, "y.tar", tarOf(t, []member{{name: "manifest.json", body: `{"format": "cf-export/2"}`}}))
	if _, err := Inspect(context.Background(), a); err != ErrUnknown {
		t.Errorf("an archive of a different format: %v", err)
	}
}

func TestCloudfactoryDatabasePrefix(t *testing.T) {
	site := func(names ...string) cfWebsite {
		var w cfWebsite
		for _, n := range names {
			w.Databases = append(w.Databases, Database{Engine: "mariadb", Name: n})
		}
		w.Databases = append(w.Databases, Database{Engine: "valkey", Name: "cache"})
		return w
	}
	for _, c := range []struct {
		names []string
		want  string
	}{
		{[]string{"shop_x7k2_wp", "shop_x7k2_my_data"}, "shop_x7k2_"},
		{[]string{"sshop1234_ab12_wp"}, "sshop1234_ab12_"},
		{[]string{"shop_x7k2_wp", "blog_q9z1_wp"}, ""},
		{[]string{"shop_x7k2_wp", "wordpress"}, ""},
		{[]string{"acme_blog"}, ""},
		{nil, ""},
	} {
		if got := cfDatabasePrefix(site(c.names...)); got != c.want {
			t.Errorf("%v: %q, want %q", c.names, got, c.want)
		}
	}
}
