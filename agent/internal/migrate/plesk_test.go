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

// pleskXML is the description of a Plesk backup of one customer with one subscription. The
// subscription has two sites, a domain alias, three mail users and two databases.
const pleskXML = `<?xml version="1.0" encoding="UTF-8"?>
<migration-dump content-included="true" agent-name="PleskX" dump-version="18.0.60" dump-format="panel">
  <client name="jdoe" guid="c-1">
    <preferences>
      <pinfo name="name">Jane Doe</pinfo>
      <pinfo name="email">jane@example.org</pinfo>
    </preferences>
    <domains>
      <domain name="Example.com" www="true" guid="d-1">
        <preferences>
          <domain-alias name="example.net" web="true" mail="true" dns="true">
            <dns-zone type="master"><dnsrec type="A" src="example.net." dst="203.0.113.5" opt=""/></dns-zone>
          </domain-alias>
        </preferences>
        <properties>
          <ip><ip-type>shared</ip-type><ip-address>203.0.113.5</ip-address></ip>
          <dns-zone email="admin@example.com" type="master">
            <dnsrec type="A" src="example.com." dst="203.0.113.5" opt=""/>
            <dnsrec type="CNAME" src="www.example.com." dst="example.com." opt=""/>
            <dnsrec type="MX" src="example.com." dst="mail.example.com." opt="10"/>
            <dnsrec type="TXT" src="_dmarc.example.com." dst="v=DMARC1; p=none" opt=""/>
            <dnsrec type="NS" src="example.com." dst="ns1.example.com." opt=""/>
          </dns-zone>
        </properties>
        <mailsystem>
          <properties><status><enabled/></status></properties>
          <mailusers>
            <mailuser name="info" mailbox-quota="1073741824" forwarding-enabled="false">
              <properties><password type="plain">s3cret-pass</password></properties>
              <preferences>
                <mailbox enabled="true" type="mdir">
                  <content><cid type="mailbox" path="clients/jdoe/domains/example.com/mailnames/info"><content-file size="300">backup_mailbox_2311221200.tgz</content-file></cid></content>
                </mailbox>
                <alias>contact</alias>
              </preferences>
            </mailuser>
            <mailuser name="sales" mailbox-quota="-1">
              <properties><password type="sym">AbCdEf==</password></properties>
              <preferences>
                <mailbox enabled="true" type="mdir">
                  <content><cid type="mailbox" path="clients/jdoe/domains/example.com/mailnames/sales"><content-file>backup_mailbox_2311221200.tgz</content-file></cid></content>
                </mailbox>
                <autoresponders><autoresponder status="on"/></autoresponders>
              </preferences>
            </mailuser>
            <mailuser name="team" mailbox-quota="-1" forwarding-enabled="true">
              <properties><password type="sym">x</password></properties>
              <preferences>
                <mailbox enabled="false" type="mdir"/>
                <redirect>jane@example.org</redirect>
                <mailgroup-member>info@example.com</mailgroup-member>
              </preferences>
            </mailuser>
          </mailusers>
          <preferences><catch-all>bounce:This address no longer accepts mail.</catch-all></preferences>
        </mailsystem>
        <databases>
          <database name="wp_db" type="mysql" version="10.6">
            <db-server type="mysql"><host>localhost</host><port>3306</port></db-server>
            <content><cid type="sqldump" path="clients/jdoe/domains/example.com/databases/wp_db_1"><content-file size="1234">backup_sqldump_2311221200.tzst</content-file></cid></content>
            <dbuser name="wp_user"><password type="sym">x</password></dbuser>
          </database>
          <database name="reports" type="postgresql"/>
        </databases>
        <phosting www-root="httpdocs">
          <preferences>
            <sysuser name="exampleuser"><password type="sym">x</password><crontab>*/10 * * * * php /var/www/vhosts/example.com/httpdocs/cron.php
MAILTO=""
@reboot /bin/true</crontab></sysuser>
          </preferences>
          <limits-and-permissions><scripting php="true" php_handler_id="plesk-php82-fpm"/></limits-and-permissions>
          <content><cid type="user-data" path="clients/jdoe/domains/example.com"><content-file>backup_user-data_2311221200.tgz</content-file></cid></content>
          <ftpusers><ftpuser name="extra"/></ftpusers>
          <sites>
            <site name="blog.example.com" parent-domain-name="example.com" www="true">
              <phosting www-root="blog.example.com"><limits-and-permissions><scripting php_handler_id="plesk-php7.4-fastcgi"/></limits-and-permissions></phosting>
            </site>
            <site name="shop.example.org" parent-domain-name="example.com" www="false">
              <phosting www-root="/var/www/vhosts/example.com/httpdocs/shop"/>
              <properties><dns-zone><dnsrec type="A" src="shop.example.org." dst="203.0.113.5"/></dns-zone></properties>
            </site>
          </sites>
        </phosting>
      </domain>
    </domains>
  </client>
</migration-dump>
`

func pleskArchive(t *testing.T) *Archive {
	t.Helper()
	dir := "clients/jdoe/domains/example.com/"
	userData := tarOf(t, []member{
		{name: "httpdocs/", dir: true},
		{name: "httpdocs/index.php", body: "<?php echo 1;"},
		{name: "httpdocs/wp-config.php", body: strings.ReplaceAll(wpConfig, "alice_wp", "wp_db")},
		{name: "httpdocs/shop/index.php", body: "shop"},
		{name: "blog.example.com/index.html", body: "<h1>Blog</h1>"},
		{name: "logs/access_log", body: "GET /"},
	})
	info := tarOf(t, []member{
		{name: "cur/1700000000.M1.host:2,S", body: "Subject: a\n\nA"},
		{name: "new/1700000001.M2.host", body: "Subject: b\n\nB"},
		{name: ".Sent/cur/1700000002.M3.host:2,S", body: "Subject: c\n\nC"},
		{name: "maildirsize", body: "1 1"},
	})
	sales := tarOf(t, []member{{name: "Maildir/cur/1700000003.M4.host:2,", body: "Subject: d\n\nD"}})
	dump := tarOf(t, []member{{name: "wp_db", body: "CREATE TABLE wp_posts (id int);\n"}})
	return writeArchive(t, "backup_example.com_2311221200.tar", tarOf(t, []member{
		{name: "backup_info_2311221200.xml", body: pleskXML},
		{name: ".discovered/2311221200/status_OK", body: ""},
		{name: dir + "backup_user-data_2311221200.tgz", body: string(gzipOf(t, userData))},
		// An archive with a content name that is not a tar archive: the inspection skips it.
		{name: dir + "backup_docroot_2311221200.tgz", body: "not an archive"},
		{name: dir + "mailnames/info/backup_mailbox_2311221200.tgz", body: string(gzipOf(t, info))},
		{name: dir + "mailnames/sales/backup_mailbox_2311221200.tgz", body: string(gzipOf(t, sales))},
		{name: dir + "databases/wp_db_1/backup_sqldump_2311221200.tzst", body: string(zstdOf(t, dump))},
	}))
}

func TestInspectPlesk(t *testing.T) {
	a := pleskArchive(t)
	acc, err := Inspect(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if acc.Source != (Source{Panel: "plesk", Version: "18.0.60", Account: "jdoe"}) || acc.Customer == nil || *acc.Customer != (Customer{Name: "Jane Doe", Email: "jane@example.org"}) {
		t.Errorf("source %+v, customer %+v", acc.Source, acc.Customer)
	}
	dir := "clients/jdoe/domains/example.com/"
	userData := dir + "backup_user-data_2311221200.tgz"
	if len(acc.Websites) != 3 {
		t.Fatalf("websites: %+v", acc.Websites)
	}
	main, blog, shop := acc.Websites[0], acc.Websites[1], acc.Websites[2]
	if main.Name != "example.com" || main.Runtime.PHPVersion != "8.2" {
		t.Errorf("main: %+v", main)
	}
	var names []string
	for _, d := range main.Domains {
		names = append(names, d.Name+"="+d.Kind)
	}
	if strings.Join(names, " ") != "example.com=primary www.example.com=alias example.net=alias www.example.net=alias" {
		t.Errorf("domains: %v", names)
	}
	files := main.Content.Files
	if files.Path != userData+"/httpdocs" || !slices.Equal(files.Exclude, []string{"shop"}) || files.Bytes != int64(len("<?php echo 1;")+len(strings.ReplaceAll(wpConfig, "alice_wp", "wp_db"))) {
		t.Errorf("files: %+v", files)
	}
	if main.AppConfig == nil || main.AppConfig.Database != "wp_db" {
		t.Errorf("app config: %+v", main.AppConfig)
	}
	if len(main.Databases) != 1 || main.Databases[0].Name != "wp_db" || main.Databases[0].Content.Path != dir+"databases/wp_db_1/backup_sqldump_2311221200.tzst" || main.Databases[0].Content.Bytes != 1234 {
		t.Errorf("databases: %+v", main.Databases)
	}
	if len(main.DatabaseUsers) != 1 || main.DatabaseUsers[0].Name != "wp_user" || main.DatabaseUsers[0].Grants[0] != (Grant{Database: "wp_db", Privileges: "readwrite"}) {
		t.Errorf("database users: %+v", main.DatabaseUsers)
	}
	if len(main.CronJobs) != 1 || main.CronJobs[0].Schedule != "*/10 * * * *" || main.CronJobs[0].Enabled {
		t.Errorf("cron: %+v", main.CronJobs)
	}
	if blog.Name != "blog.example.com" || blog.Runtime.PHPVersion != "7.4" || blog.Content.Files.Path != userData+"/blog.example.com" || blog.DiskBytes != int64(len("<h1>Blog</h1>")) {
		t.Errorf("blog: %+v %+v", blog, blog.Content.Files)
	}
	if shop.Name != "shop.example.org" || shop.Runtime.PHPVersion != "8.2" || shop.Content.Files.Path != userData+"/httpdocs/shop" || len(shop.Domains) != 1 {
		t.Errorf("shop: %+v %+v", shop, shop.Content.Files)
	}

	if len(acc.Mail.Domains) != 1 {
		t.Fatalf("mail: %+v", acc.Mail)
	}
	md := acc.Mail.Domains[0]
	if md.Name != "example.com" || len(md.Mailboxes) != 2 {
		t.Fatalf("mail domain: %+v", md)
	}
	info, sales := md.Mailboxes[0], md.Mailboxes[1]
	if info.LocalPart != "info" || info.QuotaBytes != 1<<30 || info.Messages != 3 || info.Content.Path != dir+"mailnames/info/backup_mailbox_2311221200.tgz" {
		t.Errorf("info: %+v %+v", info, info.Content)
	}
	if !strings.HasPrefix(info.PasswordHash, "$6$") || sha512Crypt("s3cret-pass", strings.Split(info.PasswordHash, "$")[2]) != info.PasswordHash {
		t.Errorf("the hash of the plain password: %q", info.PasswordHash)
	}
	if sales.LocalPart != "sales" || sales.QuotaBytes != 0 || sales.PasswordHash != "" || sales.Messages != 1 || sales.Content.Path != dir+"mailnames/sales/backup_mailbox_2311221200.tgz/Maildir" {
		t.Errorf("sales: %+v %+v", sales, sales.Content)
	}
	b, _ := json.Marshal(md.Aliases)
	if string(b) != `[{"localPart":"contact","kind":"alias","destinations":["info@example.com"]},{"localPart":"team","kind":"forwarder","destinations":["jane@example.org","info@example.com"]}]` {
		t.Errorf("aliases: %s", b)
	}

	var zones []string
	for _, z := range acc.DNSZones {
		zones = append(zones, z.Name)
	}
	if strings.Join(zones, " ") != "example.com example.net shop.example.org" {
		t.Errorf("zones: %v", zones)
	}
	want := "$ORIGIN example.com.\n@\tIN\tA\t203.0.113.5\nwww.example.com.\tIN\tCNAME\texample.com.\n@\tIN\tMX\t10 mail.example.com.\n_dmarc.example.com.\tIN\tTXT\t\"v=DMARC1; p=none\"\n@\tIN\tNS\tns1.example.com.\n"
	if acc.DNSZones[0].ZoneFile != want {
		t.Errorf("zone file:\n%s", acc.DNSZones[0].ZoneFile)
	}

	var warnings []string
	for _, w := range acc.Warnings {
		warnings = append(warnings, w.Item)
	}
	for _, item := range []string{"cron", "database:reports", "mail:example.net", "mail:sales@example.com", "ftp", "files", "certificates", "mail:example.com"} {
		if !slices.Contains(warnings, item) {
			t.Errorf("no warning %s: %+v", item, acc.Warnings)
		}
	}
	if slices.Contains(warnings, "mail:*@example.com") {
		t.Errorf("a catch-all that refuses mail is not a warning: %+v", acc.Warnings)
	}

	// The jobs read the content with the paths of the description.
	var dump bytes.Buffer
	if err := WithMember(context.Background(), a, main.Databases[0].Content.Path, func(r io.Reader) error {
		_, err := io.Copy(&dump, r)
		return err
	}); err != nil || dump.String() != "CREATE TABLE wp_posts (id int);\n" {
		t.Errorf("dump %q, %v", dump.String(), err)
	}
	var out bytes.Buffer
	st, err := WriteFiles(context.Background(), a, files.Path, FileOptions{Exclude: files.Exclude}, &out)
	if err != nil || st.Files != 2 {
		t.Errorf("files %+v, %v", st, err)
	}
	var messages []string
	if err := WalkMaildir(context.Background(), a, sales.Content.Path, func(m Message, r io.Reader) error {
		messages = append(messages, m.Name)
		return nil
	}); err != nil || len(messages) != 1 {
		t.Errorf("messages %v, %v", messages, err)
	}
}

func TestInspectPleskWithContentAtTheRoot(t *testing.T) {
	// A backup of one subscription with the content files next to the XML file, and paths
	// in the XML file of a different layout.
	userData := tarOf(t, []member{{name: "httpdocs/index.html", body: "hi"}})
	xml := `<migration-dump dump-version="17.8"><domain name="solo.example"><phosting>
	<preferences><sysuser name="solo"/></preferences>
	<content><cid type="user-data" path="domains/solo.example"><content-file>backup_user-data_2311221200.tgz</content-file></cid></content>
	</phosting></domain></migration-dump>`
	a := writeArchive(t, "b.tar", tarOf(t, []member{
		{name: "solo/backup_info_2311221200.xml", body: xml},
		{name: "solo/backup_user-data_2311221200.tgz", body: string(gzipOf(t, userData))},
	}))
	acc, err := Inspect(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if acc.Source.Account != "solo" || acc.Customer != nil || len(acc.Websites) != 1 || acc.Websites[0].Content.Files.Path != "solo/backup_user-data_2311221200.tgz/httpdocs" || acc.Websites[0].DiskBytes != 2 {
		t.Errorf("account: %+v", acc)
	}
}

func TestSHA512Crypt(t *testing.T) {
	// The test vector of the specification of SHA-crypt, and hashes that PHP crypt() gives.
	for _, c := range [][3]string{
		{"Hello world!", "saltstring", "$6$saltstring$svn8UoSVapNtMuq1ukKS4tPQd8iKwSMHWjl/O817G3uBnIFNjnQJuesI68u4OTLiBFdcbYEdFCoEOfaS35inz1"},
		{"", "abc", "$6$abc$mJP3a6FyA8uCnzRtlnNypPwjnvpi5TP9qOrInzrfDmwxUQG38PkpCPdqfTb8JQfAngapMxeim4AZ..hSdRRzD."},
		{"a very long password that is longer than sixty-four bytes in total, yes it is longer", "0123456789abcdef", "$6$0123456789abcdef$TAsJ/LB/Hh0.D2OV8uB79WPsBEyF/0tFRbsTIy3ppwUdFx2nf.Aa0G1/s3hGps2ykJk0SpX4ZnEv3VYpEKjaB0"},
		{"ü€", "./xY", "$6$./xY$nrs1TeAQ8aw9IeFEqztLHf1CAQKmFz1.gFl7976e6u1JAwkjGfSW8AjTx9PQxHKMTFIj8RjfqHlgSwRtmWOcs/"},
	} {
		if got := sha512Crypt(c[0], c[1]); got != c[2] {
			t.Errorf("sha512Crypt(%q, %q) = %s", c[0], c[1], got)
		}
	}
	h := hashPassword("x")
	if passwordHash(h) != h || len(strings.Split(h, "$")[2]) != 16 {
		t.Errorf("hashPassword gives %s", h)
	}
}

func TestParseXMLLimits(t *testing.T) {
	if _, err := parseXML([]byte(strings.Repeat("<a>", maxXMLDepth+1) + strings.Repeat("</a>", maxXMLDepth+1))); err == nil {
		t.Error("a deep document is an error")
	}
	if _, err := parseXML([]byte("<a/><b/>")); err == nil {
		t.Error("two root elements are an error")
	}
	n, err := parseXML([]byte(`<?xml version="1.0" encoding="windows-1252"?><a x="1"><b>t</b></a>`))
	if err != nil || n.value("x") != "1" || n.value("b") != "t" || n.at("c", "d").get("e") != "" {
		t.Errorf("%+v %v", n, err)
	}
}
