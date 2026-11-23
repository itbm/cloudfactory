package actions

import (
	"errors"
	"strings"
	"testing"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/execx"
	"google.golang.org/protobuf/proto"
)

// The tests of the staging copy of a WordPress site (FR-205, FR-206).

const (
	asCopy = "runuser -u staging_ab12 -- "
	copyPW = "Copypassword12345678"
)

// liveConfig is wp-config.php of the live site, as WP-CLI writes it, with a second style of
// quotes and spaces for DB_PASSWORD.
const liveConfig = `<?php
define( 'DB_NAME', 'acme_x7k2_wp' );
define( 'DB_USER', 'acme_x7k2_wp' );
define("DB_PASSWORD" , "Live\"password1234");
define( 'DB_HOST', 'mariadb.internal' );
$table_prefix = 'wp_';
`

func wordPressCopyRequest(w *agentv1.Website) *agentv1.WebsiteRequest {
	return &agentv1.WebsiteRequest{JobId: jobID, Website: w, CopyFrom: "01JBQ3Z5K8N6T4W2X9Y7R5P3M9", CopyFromUnixUser: "acme_x7k2",
		CopyDatabases:   []*agentv1.DatabaseCopy{{Engine: "mariadb", From: "acme_x7k2_wp", To: "staging_ab12_wp", FromServer: mariaServerID, ToServer: mariaServerID}},
		CopyUsers:       []*agentv1.DatabaseUserCopy{{Engine: "mariadb", Name: "staging_ab12_app", Password: copyPW, DatabaseServer: mariaServerID}},
		DatabaseServers: []*agentv1.DatabaseServer{databaseServer("mariadb")},
		CopyWordpress:   &agentv1.WordPressCopy{Database: "staging_ab12_wp", User: "staging_ab12_app"}}
}

func TestSetWPConfigDatabase(t *testing.T) {
	got, err := setWPConfigDatabase(liveConfig, map[string]string{"DB_NAME": "staging_ab12_wp", "DB_USER": "staging_ab12_app", "DB_PASSWORD": copyPW})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"define( 'DB_NAME', 'staging_ab12_wp' );", "define( 'DB_USER', 'staging_ab12_app' );",
		`define("DB_PASSWORD" , '` + copyPW + `');`, "define( 'DB_HOST', 'mariadb.internal' );", "$table_prefix = 'wp_';"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "acme_x7k2") || strings.Contains(got, "Live") {
		t.Errorf("a value of the live site stays:\n%s", got)
	}
	// A value from a function is not a text value: the copy would use the live database.
	_, err = setWPConfigDatabase(strings.Replace(liveConfig, "'acme_x7k2_wp' );\ndefine( 'DB_USER'", "getenv('DB') );\ndefine( 'DB_USER'", 1), map[string]string{})
	if err == nil || !strings.Contains(err.Error(), "does not set DB_NAME") {
		t.Errorf("err = %v", err)
	}
}

func TestStagingCopyConnectsWordPressToItsDatabaseCopy(t *testing.T) {
	d, f, _ := deps(t)
	if _, err := d.useDatabases(customerKey, "01JBQ3Z5K8N6T4W2X9Y7R5P3M9"); err != nil {
		t.Fatal(err)
	}
	w := website("ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1")
	w.UnixUser = "staging_ab12"
	path := d.Host.AppDir("staging_ab12") + "/public/wp-config.php"
	f.Rules = append([]execx.Rule{{Prefix: asCopy + "sh -c for f do", Output: path + "\n" + liveConfig}}, f.Rules...)
	if err := run(t)(d.CreateWebsite(wordPressCopyRequest(w))); err != nil {
		t.Fatal(err)
	}
	// The agent looks in the document root, then in the directory above it.
	if !f.Called(asCopy + "sh -c " + wpConfigScript + " sh " + path + " " + d.Host.AppDir("staging_ab12") + "/wp-config.php") {
		t.Errorf("no read of wp-config.php: %v", f.Calls)
	}
	call, in := called(f, asCopy+`sh -c cat > "$1" sh `+path)
	if call == "" || !strings.Contains(in, "define( 'DB_NAME', 'staging_ab12_wp' );") || !strings.Contains(in, "'"+copyPW+"'") {
		t.Errorf("wp-config.php = %q (call %q)", in, call)
	}
	for _, c := range f.Calls {
		if strings.Contains(c, copyPW) {
			t.Fatalf("the password is in the arguments: %s", c)
		}
	}

	// With no wp-config.php, the job stops before the container of the copy starts: the copy
	// would not use its database copy.
	d, f, _ = deps(t)
	if _, err := d.useDatabases(customerKey, "01JBQ3Z5K8N6T4W2X9Y7R5P3M9"); err != nil {
		t.Fatal(err)
	}
	f.Rules = append([]execx.Rule{{Prefix: asCopy + "sh -c for f do", Err: errors.New("exit status 3")}}, f.Rules...)
	err := run(t)(d.CreateWebsite(wordPressCopyRequest(w)))
	if err == nil || !strings.Contains(err.Error(), "has no wp-config.php in /app/public") {
		t.Fatalf("err = %v", err)
	}
}

func TestStagingCopyChecksTheWordPressCopy(t *testing.T) {
	d, _, _ := deps(t)
	w := website("ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1")
	w.UnixUser = "staging_ab12"
	for name, change := range map[string]func(r *agentv1.WebsiteRequest){
		"no copy": func(r *agentv1.WebsiteRequest) {
			r.CopyFrom, r.CopyDatabases, r.CopyUsers, r.DatabaseServers = "", nil, nil, nil
		},
		"a database of the source": func(r *agentv1.WebsiteRequest) { r.CopyWordpress.Database = "acme_x7k2_wp" },
		"a database that is not a copy": func(r *agentv1.WebsiteRequest) {
			r.CopyWordpress.Database = "staging_ab12_shop"
		},
		"a user that is not a copy user": func(r *agentv1.WebsiteRequest) { r.CopyWordpress.User = "staging_ab12_other" },
		"a PostgreSQL copy": func(r *agentv1.WebsiteRequest) {
			r.CopyDatabases[0].Engine, r.CopyUsers[0].Engine = "postgresql", "postgresql"
		},
	} {
		bad := proto.Clone(wordPressCopyRequest(w)).(*agentv1.WebsiteRequest)
		change(bad)
		if _, err := d.CreateWebsite(bad); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestStageWordPressReplacesTheAddressOfTheLiveSite(t *testing.T) {
	d, f := withWordPress(t)
	f.Rules = append([]execx.Rule{
		{Prefix: wpCmd + "option get home", Output: "https://www.acme.example\n"},
		{Prefix: wpCmd + "config get WP_HOME", Output: "https://acme.example/blog\n"},
		{Prefix: wpCmd + "config get WP_SITEURL", Err: errors.New("exit status 1: Error: The constant 'WP_SITEURL' is not defined")},
	}, f.Rules...)
	job, err := d.StageWordPress(&agentv1.StageWordPressRequest{JobId: jobID2, WebsiteId: websiteID, Url: "https://staging.acme.example",
		FromUrls: []string{"https://acme.example", "https://staging.acme.example"}})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range job.Steps {
		names = append(names, s.Name)
	}
	if got := strings.Join(names, "|"); got != "Replace the site address|Hide from search engines|Check WordPress" {
		t.Errorf("steps = %s", got)
	}
	if err := runSteps(t, job, nil); err != nil {
		t.Fatal(err)
	}
	// The longest host first. The address of the copy is not a host to replace.
	var replaced []string
	for _, c := range f.Calls {
		if rest, ok := strings.CutPrefix(c, wpCmd+"search-replace "); ok {
			replaced = append(replaced, rest)
		}
	}
	opts := " --all-tables-with-prefix --skip-columns=guid --report-changed-only" + wpFlags
	want := []string{
		"//www.acme.example //staging.acme.example" + opts, `\/\/www.acme.example \/\/staging.acme.example` + opts,
		"//acme.example //staging.acme.example" + opts, `\/\/acme.example \/\/staging.acme.example` + opts,
	}
	if strings.Join(replaced, "\n") != strings.Join(want, "\n") {
		t.Errorf("search-replace:\n%s\nwant:\n%s", strings.Join(replaced, "\n"), strings.Join(want, "\n"))
	}
	for _, c := range []string{
		wpCmd + "config set WP_HOME https://staging.acme.example/blog --type=constant",
		wpCmd + "option update blog_public 0" + wpFlags,
		wpCmd + "config set WP_ENVIRONMENT_TYPE staging --type=constant",
	} {
		if !f.Called(c) {
			t.Errorf("no call %q", c)
		}
	}
	if f.Called(wpCmd + "config set WP_SITEURL") {
		t.Error("the agent set WP_SITEURL, which wp-config.php does not have")
	}
	if wp := job.Result()["wordpress"].(map[string]any); wp["version"] != "6.8.1" {
		t.Errorf("result = %v", wp)
	}
}

func TestStageWordPressChecksTheAddresses(t *testing.T) {
	d, _ := withWordPress(t)
	for _, req := range []*agentv1.StageWordPressRequest{
		{JobId: jobID2, WebsiteId: websiteID, Url: "http://staging.acme.example"},
		{JobId: jobID2, WebsiteId: websiteID, Url: "https://staging.acme.example:8443"},
		{JobId: jobID2, WebsiteId: websiteID, Url: "https://staging.acme.example", FromUrls: []string{"javascript:alert(1)"}},
		{JobId: jobID2, WebsiteId: websiteID, Url: "https://staging.acme.example", FromUrls: []string{"https://-x.example"}},
		{JobId: jobID2, WebsiteId: "../x", Url: "https://staging.acme.example"},
	} {
		if _, err := d.StageWordPress(req); err == nil {
			t.Errorf("accepted %v", req)
		}
	}
}
