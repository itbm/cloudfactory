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

// Password hashes of a HestiaCP mail domain: bcrypt (the default), MD5-crypt (an older
// server) and Argon2id (the mail server cannot check it).
const (
	hestiaBcrypt = "$2y$05$abcdefghijklmnopqrstuu0123456789ABCDEFGHIJKLMNOPQRSTU"
	hestiaMD5    = "$1$abcdefgh$0123456789ABCDEFGHIJKL"
	hestiaArgon  = "{ARGON2ID}$argon2id$v=19$m=65536,t=3,p=1$c2FsdA$aGFzaA"
)

// hestiaArchive is a HestiaCP backup of the user "bakery" with four web domains, four
// databases, one mail domain and two DNS domains. The names start with "./", as HestiaCP
// writes them. The first web domain shows the public_html directory of the second one.
func hestiaArchive(t *testing.T) *Archive {
	t.Helper()
	wp := strings.ReplaceAll(wpConfig, "alice_wp", "bakery_wp")
	main := tarOf(t, []member{
		{name: "./", dir: true},
		{name: "./public_html/", dir: true},
		{name: "./public_html/index.php", body: "<?php echo 1;"},
		{name: "./public_html/wp-config.php", body: wp},
		{name: "./public_html/shop/", dir: true},
		{name: "./public_html/shop/index.php", body: "shop"},
		{name: "./private/notes.txt", body: "private"},
		{name: "./document_errors/404.html", body: "404"},
	})
	shop := tarOf(t, []member{{name: "./public_html/index.html", body: "default"}})
	blog := tarOf(t, []member{
		{name: "./public_html/current/index.php", body: "blog"},
		{name: "./public_html/current/wp-config.php", body: strings.ReplaceAll(wpConfig, "alice_wp", "bakery_blog")},
	})
	accounts := tarOf(t, []member{
		{name: "info/cur/1700000000.M1.host:2,S", body: "Subject: a\n\nA"},
		{name: "info/new/1700000001.M2.host", body: "Subject: b\n\nB"},
		{name: "info/.Sent/cur/1700000002.M3.host:2,S", body: "Subject: c\n\nC"},
		{name: "info/dovecot.index.cache", body: "index"},
		{name: "orders/cur/1700000003.M4.host:2,", body: "Subject: d\n\nD"},
		{name: "team/cur/1700000004.M5.host:2,", body: "Subject: e\n\nE"},
	})
	web := func(domain, values string) string {
		return "DOMAIN='" + domain + "' IP='203.0.113.5' IP6='' " + values + " TPL='default' SSL='yes' SSL_FORCE='yes' LETSENCRYPT='yes' PROXY='default' PROXY_EXT='css,htm,html,js' STATS='' STATS_USER='' STATS_CRYPT='' U_DISK='12' U_BANDWIDTH='0' SUSPENDED='no' TIME='12:00:00' DATE='2026-11-01'\n"
	}
	return writeArchive(t, "bakery.2026-11-22_05-00-00.tar", tarOf(t, []member{
		{name: "./", dir: true},
		{name: "./hestia/user.conf", body: "NAME='Corner Bakery'\nPACKAGE='default'\nCONTACT='hello@bakery.example'\nCRON_REPORTS='yes'\n"},
		{name: "./hestia/ssl/bakery.example.crt", body: "cert"},
		{name: "./pam/passwd", body: "bakery:x:1001:1001:hello@bakery.example:/home/bakery:/bin/bash\n"},
		{name: "./pam/shadow", body: "bakery:$6$x$y:19000:0:99999:7:::\n"},
		{name: "./web/old.bakery.example/hestia/web.conf", body: web("old.bakery.example", "CUSTOM_DOCROOT='/home/bakery/web/bakery.example/public_html/' ALIAS='www.old.bakery.example' FTP_USER='' BACKEND='default' SSL_HOME='same'")},
		{name: "./web/bakery.example/hestia/web.conf", body: web("bakery.example", "CUSTOM_DOCROOT='' ALIAS='www.bakery.example,bakery.example.net' FTP_USER='bakery_ftp' FTP_MD5='x' BACKEND='PHP-8_2' SSL_HOME='same'")},
		{name: "./web/bakery.example/conf/nginx.conf", body: "server {}"},
		{name: "./web/bakery.example/domain_data.tar.zst", body: string(zstdOf(t, main))},
		{name: "./web/shop.bakery.example/hestia/web.conf", body: web("shop.bakery.example", "CUSTOM_DOCROOT='/home/bakery/web/bakery.example/public_html/shop/' CUSTOM_PHPROOT='/home/bakery/web/bakery.example/public_html/shop/' ALIAS='' FTP_USER='' BACKEND='PHP-7_4' SSL_HOME='same'")},
		{name: "./web/shop.bakery.example/domain_data.tar.gz", body: string(gzipOf(t, shop))},
		{name: "./web/blog.bakery.example/hestia/web.conf", body: web("blog.bakery.example", "CUSTOM_DOCROOT='/home/bakery/web/blog.bakery.example/public_html/current/' ALIAS='' FTP_USER='' BACKEND='default' SSL_HOME='single' REDIRECT='https://bakery.example/blog' REDIRECT_CODE='301'")},
		{name: "./web/blog.bakery.example/domain_data.tar.gz", body: string(gzipOf(t, blog))},
		{name: "./db/bakery_wp/hestia/db.conf", body: "DB='bakery_wp' DBUSER='bakery_wpu' MD5='*ABC' HOST='localhost' TYPE='mysql' CHARSET='utf8mb4' U_DISK='1' SUSPENDED='no' TIME='12:00:00' DATE='2026-11-01'\n"},
		{name: "./db/bakery_wp/conf/bakery_wp.mysql.bakery_wpu", body: "GRANT USAGE ON *.* TO `bakery_wpu`@`localhost`\n"},
		{name: "./db/bakery_wp/bakery_wp.mysql.sql.zst", body: string(zstdOf(t, []byte("CREATE TABLE wp_posts (id int);\n")))},
		{name: "./db/bakery_blog/hestia/db.conf", body: "DB='bakery_blog' DBUSER='bakery_blogu' MD5='*DEF' HOST='localhost' TYPE='mysql' CHARSET='utf8mb4'\n"},
		{name: "./db/bakery_blog/bakery_blog.mysql.sql.gz", body: string(gzipOf(t, []byte("CREATE TABLE wp_options (id int);\n")))},
		{name: "./db/bakery_stats/hestia/db.conf", body: "DB='bakery_stats' DBUSER='bakery_stats' MD5='md5x' HOST='localhost' TYPE='pgsql' CHARSET='UTF8'\n"},
		{name: "./db/bakery_stats/bakery_stats.pgsql.sql.zst", body: string(zstdOf(t, []byte("-- pg\n")))},
		{name: "./db/bakery_empty/hestia/db.conf", body: "DB='bakery_empty' DBUSER='bakery_e' MD5='*0' HOST='localhost' TYPE='mysql' CHARSET='utf8'\n"},
		{name: "./mail/bakery.example/hestia/mail.conf", body: "DOMAIN='bakery.example' ANTIVIRUS='yes' ANTISPAM='yes' DKIM='yes' WEBMAIL='roundcube' SSL='no' LETSENCRYPT='no' CATCHALL='orders@bakery.example' ACCOUNTS='4' U_DISK='1' SUSPENDED='no'\n"},
		{name: "./mail/bakery.example/hestia/bakery.example.conf", body: "" +
			"ACCOUNT='info' ALIAS='contact,hello' AUTOREPLY='no' FWD='' FWD_ONLY='' MD5='{BLF-CRYPT}" + hestiaBcrypt + "' QUOTA='1024' U_DISK='1' SUSPENDED='no' TIME='12:00:00' DATE='2026-11-01'\n" +
			"ACCOUNT='orders' ALIAS='' AUTOREPLY='yes' FWD='owner@example.org' FWD_ONLY='' MD5='{MD5}" + hestiaMD5 + "' QUOTA='unlimited' U_DISK='0' SUSPENDED='no'\n" +
			"ACCOUNT='team' ALIAS='' AUTOREPLY='no' FWD='info@bakery.example,Owner@Example.org' FWD_ONLY='yes' MD5='" + hestiaArgon + "' QUOTA='unlimited' U_DISK='0' SUSPENDED='no'\n" +
			"ACCOUNT='old' ALIAS='info' AUTOREPLY='no' FWD='' FWD_ONLY='' MD5='" + hestiaArgon + "' QUOTA='unlimited' U_DISK='0' SUSPENDED='no'\n"},
		{name: "./mail/bakery.example/hestia/bakery.example.pem", body: "dkim key"},
		{name: "./mail/bakery.example/accounts.tar.zst", body: string(zstdOf(t, accounts))},
		{name: "./dns/bakery.example/hestia/dns.conf", body: "DOMAIN='bakery.example' IP='203.0.113.5' TPL='default' TTL='14400' EXP='2027-11-22' SOA='ns1.host.example' SERIAL='3000000001' SRC='' RECORDS='3' DNSSEC='no' SUSPENDED='no'\n"},
		{name: "./dns/bakery.example/hestia/bakery.example.conf", body: "ID='1' RECORD='@' TYPE='A' PRIORITY='' VALUE='203.0.113.5' SUSPENDED='no' TIME='12:00:00' DATE='2026-11-01' TTL=''\n"},
		{name: "./dns/bakery.example/conf/bakery.example.db", body: "$TTL 14400\n@    IN    SOA    ns1.host.example.    root.bakery.example. (\n 3000000001 14400 3600 1209600 3600 )\n\n@\t14400\tIN\tA\t\t203.0.113.5\nwww\t14400\tIN\tCNAME\t\tbakery.example.\n"},
		{name: "./dns/bakery.example.net/hestia/dns.conf", body: "DOMAIN='bakery.example.net' IP='203.0.113.5' TPL='default' TTL='3600' SOA='ns1.host.example' SUSPENDED='no'\n"},
		{name: "./dns/bakery.example.net/hestia/bakery.example.net.conf", body: "" +
			"ID='1' RECORD='@' TYPE='A' PRIORITY='' VALUE='203.0.113.5' SUSPENDED='no' TIME='12:00:00' DATE='2026-11-01' TTL=''\n" +
			"ID='2' RECORD='@' TYPE='MX' PRIORITY='10' VALUE='mail.bakery.example.net.' SUSPENDED='no' TTL='600'\n" +
			"ID='3' RECORD='@' TYPE='TXT' PRIORITY='' VALUE='\"v=spf1 a mx -all\"' SUSPENDED='no'\n" +
			"ID='4' RECORD='_dmarc' TYPE='TXT' PRIORITY='' VALUE='v=DMARC1; p=none' SUSPENDED='no'\n" +
			"ID='5' RECORD='test' TYPE='A' PRIORITY='' VALUE='203.0.113.9' SUSPENDED='yes'\n"},
		{name: "./cron/cron.conf", body: "" +
			"JOB='1' MIN='*/5' HOUR='*' DAY='*' MONTH='*' WDAY='*' CMD='cd /home/bakery/web/bakery.example/public_html && php artisan schedule%dots%run' SUSPENDED='no' TIME='12:00:00' DATE='2026-11-01'\n" +
			"JOB='2' MIN='0' HOUR='3' DAY='*' MONTH='*' WDAY='1' CMD='/usr/bin/wget -q -O /dev/null %quote%https%dots%//bakery.example/cron%quote%' SUSPENDED='yes' TIME='12:00:00' DATE='2026-11-01'\n" +
			"JOB='3' MIN='61' HOUR='*' DAY='*' MONTH='*' WDAY='*' CMD='true' SUSPENDED='no'\n"},
		{name: "./cron/bakery", body: "MAILTO=hello@bakery.example\n"},
		{name: "./user_dir/.bashrc.tar.zst", body: string(zstdOf(t, tarOf(t, []member{{name: ".bashrc", body: "alias ll='ls -l'"}})))},
		{name: "./.zstd", body: ""},
	}))
}

func TestInspectHestia(t *testing.T) {
	a := hestiaArchive(t)
	acc, err := Inspect(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if acc.Source != (Source{Panel: "hestia", Account: "bakery"}) || acc.Customer == nil || *acc.Customer != (Customer{Name: "Corner Bakery", Email: "hello@bakery.example"}) {
		t.Errorf("source %+v, customer %+v", acc.Source, acc.Customer)
	}
	if len(acc.Websites) != 3 {
		t.Fatalf("websites: %+v", acc.Websites)
	}
	main, shop, blog := acc.Websites[0], acc.Websites[1], acc.Websites[2]
	data := "web/bakery.example/domain_data.tar.zst/public_html"

	// The main domain has its aliases, and the web domain that shows its public_html.
	if main.Name != "bakery.example" || main.Runtime != (Runtime{Family: "php", PHPVersion: "8.2", DocumentRoot: "."}) {
		t.Errorf("main: %+v", main)
	}
	var names []string
	for _, d := range main.Domains {
		names = append(names, d.Name+"="+d.Kind)
	}
	if strings.Join(names, " ") != "bakery.example=primary www.bakery.example=alias bakery.example.net=alias old.bakery.example=alias www.old.bakery.example=alias" {
		t.Errorf("domains: %v", names)
	}
	wp := strings.ReplaceAll(wpConfig, "alice_wp", "bakery_wp")
	if f := main.Content.Files; f.Path != data || !slices.Equal(f.Exclude, []string{"shop"}) || f.Bytes != int64(len("<?php echo 1;")+len(wp)) || main.DiskBytes != f.Bytes {
		t.Errorf("files: %+v", f)
	}
	if main.AppConfig == nil || *main.AppConfig != (AppConfig{Kind: "wordpress", File: "wp-config.php", Database: "bakery_wp"}) {
		t.Errorf("app config: %+v", main.AppConfig)
	}
	if len(main.Databases) != 2 || main.Databases[0].Name != "bakery_empty" || main.Databases[0].Content != nil || main.Databases[1].Name != "bakery_wp" || main.Databases[1].Content.Path != "db/bakery_wp/bakery_wp.mysql.sql.zst" || main.Databases[1].Content.Bytes == 0 {
		t.Errorf("databases: %+v", main.Databases)
	}
	if len(main.DatabaseUsers) != 2 || main.DatabaseUsers[1].Name != "bakery_wpu" || main.DatabaseUsers[1].Grants[0] != (Grant{Database: "bakery_wp", Privileges: "readwrite"}) {
		t.Errorf("database users: %+v", main.DatabaseUsers)
	}
	b, _ := json.Marshal(main.CronJobs)
	if string(b) != `[{"schedule":"*/5 * * * *","command":"cd /home/bakery/web/bakery.example/public_html \u0026\u0026 php artisan schedule:run","enabled":false},{"schedule":"0 3 * * 1","command":"/usr/bin/wget -q -O /dev/null 'https://bakery.example/cron'","enabled":false}]` {
		t.Errorf("cron: %s", b)
	}

	// A document root in a directory of a different web domain.
	if shop.Name != "shop.bakery.example" || shop.Runtime.PHPVersion != "7.4" || shop.Content.Files.Path != data+"/shop" || shop.DiskBytes != 4 || len(shop.Domains) != 1 {
		t.Errorf("shop: %+v %+v", shop, shop.Content.Files)
	}
	// A document root in a directory of the web domain itself.
	if blog.Runtime != (Runtime{Family: "php", DocumentRoot: "current"}) || blog.Content.Files.Path != "web/blog.bakery.example/domain_data.tar.gz/public_html" {
		t.Errorf("blog: %+v %+v", blog, blog.Content.Files)
	}
	if blog.AppConfig == nil || *blog.AppConfig != (AppConfig{Kind: "wordpress", File: "current/wp-config.php", Database: "bakery_blog"}) {
		t.Errorf("blog app config: %+v", blog.AppConfig)
	}
	if len(blog.Databases) != 1 || blog.Databases[0].Content.Path != "db/bakery_blog/bakery_blog.mysql.sql.gz" || len(blog.DatabaseUsers) != 1 {
		t.Errorf("blog databases: %+v", blog.Databases)
	}

	// Mail.
	if len(acc.Mail.Domains) != 1 || len(acc.Mail.Domains[0].Mailboxes) != 3 {
		t.Fatalf("mail: %+v", acc.Mail)
	}
	md := acc.Mail.Domains[0]
	info, orders, old := md.Mailboxes[0], md.Mailboxes[1], md.Mailboxes[2]
	boxes := "mail/bakery.example/accounts.tar.zst/"
	if info.LocalPart != "info" || info.QuotaBytes != 1<<30 || info.PasswordHash != hestiaBcrypt || info.Messages != 3 || info.Content.Path != boxes+"info" || info.Content.Bytes != 3*int64(len("Subject: a\n\nA")) {
		t.Errorf("info: %+v %+v", info, info.Content)
	}
	if orders.LocalPart != "orders" || orders.QuotaBytes != 0 || orders.PasswordHash != hestiaMD5 || orders.Messages != 1 {
		t.Errorf("orders: %+v", orders)
	}
	if old.LocalPart != "old" || old.PasswordHash != "" || old.Content != nil {
		t.Errorf("old: %+v", old)
	}
	b, _ = json.Marshal(md.Aliases)
	if string(b) != `[{"localPart":"team","kind":"forwarder","destinations":["info@bakery.example","owner@example.org"]},{"localPart":"contact","kind":"alias","destinations":["info@bakery.example"]},{"localPart":"hello","kind":"alias","destinations":["info@bakery.example"]}]` {
		t.Errorf("aliases: %s", b)
	}

	// DNS: the zone file of the backup, or a zone file of the records.
	if len(acc.DNSZones) != 2 || acc.DNSZones[0].Name != "bakery.example" || !strings.Contains(acc.DNSZones[0].ZoneFile, "www\t14400\tIN\tCNAME") {
		t.Fatalf("zones: %+v", acc.DNSZones)
	}
	want := "$TTL 3600\n@\tIN\tA\t203.0.113.5\n@\t600\tIN\tMX\t10\tmail.bakery.example.net.\n@\tIN\tTXT\t\"v=spf1 a mx -all\"\n_dmarc\tIN\tTXT\t\"v=DMARC1; p=none\"\n"
	if acc.DNSZones[1].Name != "bakery.example.net" || acc.DNSZones[1].ZoneFile != want {
		t.Errorf("zone file:\n%s", acc.DNSZones[1].ZoneFile)
	}

	var warnings []string
	for _, w := range acc.Warnings {
		warnings = append(warnings, w.Item+": "+w.Message)
	}
	all := strings.Join(warnings, "\n")
	for _, s := range []string{
		"website:blog.bakery.example: The domain redirects to https://bakery.example/blog",
		"website:blog.bakery.example: The domain has a separate public_shtml directory",
		"database:bakery_stats: The import does not copy PostgreSQL databases yet",
		"database:bakery_empty: The backup has no dump of this database",
		"cron: The panel cannot use the schedule",
		"mail:team@bakery.example: The address only forwards mail on the old panel. The import makes a forwarder, and does not copy the 1 messages",
		"mail:orders@bakery.example: The address is a mailbox and forwards mail",
		"mail:orders@bakery.example: The import does not copy autoresponders.",
		"mail:bakery.example: 1 mailboxes of bakery.example have a password hash",
		"mail:*@bakery.example: The import does not copy the catch-all address (orders@bakery.example).",
		"files: ",
		"ftp: ",
		"certificates: ",
	} {
		if !strings.Contains(all, s) {
			t.Errorf("no warning %q:\n%s", s, all)
		}
	}
	if strings.Contains(all, "old@bakery.example") || strings.Contains(all, "mail:info@") {
		t.Errorf("an alias of a mailbox that is a mailbox too is not a forwarder:\n%s", all)
	}

	// The jobs read the content with the paths of the description.
	var dump bytes.Buffer
	if err := WithMember(context.Background(), a, blog.Databases[0].Content.Path, func(r io.Reader) error {
		_, err := io.Copy(&dump, r)
		return err
	}); err != nil || dump.String() != "CREATE TABLE wp_options (id int);\n" {
		t.Errorf("dump %q, %v", dump.String(), err)
	}
	var out bytes.Buffer
	st, err := WriteFiles(context.Background(), a, main.Content.Files.Path, FileOptions{Exclude: main.Content.Files.Exclude}, &out)
	if err != nil || st.Files != 2 || !slices.Equal(tarNames(t, out.Bytes()), []string{"index.php", "wp-config.php"}) {
		t.Errorf("files %+v, %v, %v", st, err, tarNames(t, out.Bytes()))
	}
	var folders []string
	if err := WalkMaildir(context.Background(), a, info.Content.Path, func(m Message, r io.Reader) error {
		folders = append(folders, strings.Join(m.Folder, "/"))
		return nil
	}); err != nil || !slices.Equal(folders, []string{"", "", "Sent"}) {
		t.Errorf("folders %v, %v", folders, err)
	}
}

func TestInspectVesta(t *testing.T) {
	// A VestaCP backup has "vesta" in place of "hestia", gzip archives and no zone file.
	data := tarOf(t, []member{{name: "./public_html/index.html", body: "hi"}})
	a := writeArchive(t, "admin.2026-11-22_05-00-00.tar", tarOf(t, []member{
		{name: "./vesta/user.conf", body: "FNAME='Ada'\nLNAME='Admin'\nCONTACT='ada@example.org'\n"},
		{name: "./pam/passwd", body: "admin:x:1000:1000::/home/admin:/bin/bash\n"},
		{name: "./web/example.org/vesta/web.conf", body: "DOMAIN='example.org' ALIAS='www.example.org' BACKEND='php-7_3'\n"},
		{name: "./web/example.org/domain_data.tar.gz", body: string(gzipOf(t, data))},
		{name: "./mail/example.org/vesta/example.org.conf", body: "ACCOUNT='ada' ALIAS='' FWD='' MD5='{MD5}" + hestiaMD5 + "' QUOTA='50'\n"},
	}))
	acc, err := Inspect(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if acc.Source.Panel != "hestia" || acc.Source.Account != "admin" || acc.Customer.Name != "Ada Admin" {
		t.Errorf("source %+v %+v", acc.Source, acc.Customer)
	}
	if len(acc.Websites) != 1 || acc.Websites[0].Runtime.PHPVersion != "7.3" || acc.Websites[0].Content.Files.Path != "web/example.org/domain_data.tar.gz/public_html" || acc.Websites[0].DiskBytes != 2 {
		t.Errorf("websites: %+v", acc.Websites)
	}
	if len(acc.Mail.Domains) != 1 || acc.Mail.Domains[0].Mailboxes[0].QuotaBytes != 50<<20 || acc.Mail.Domains[0].Mailboxes[0].Content != nil {
		t.Errorf("mail: %+v", acc.Mail)
	}
}
