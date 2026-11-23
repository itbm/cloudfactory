// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

// fakeRunner records each client program with its stdin, and gives the answer of the first
// rule whose text is in the SQL.
type fakeRunner struct {
	calls   []string
	answers map[string]string
	fail    map[string]error
	// options is the content of the option file of the last call.
	options string
}

func (f *fakeRunner) run(_ context.Context, stdin io.Reader, stdout io.Writer, name string, args ...string) error {
	in := ""
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		in = string(b)
	}
	for _, a := range args {
		if path, ok := strings.CutPrefix(a, "--defaults-extra-file="); ok {
			b, _ := os.ReadFile(path)
			f.options = string(b)
		}
	}
	call := name + " " + strings.Join(args, " ")
	if in != "" {
		call += "\n" + in
	}
	f.calls = append(f.calls, call)
	for text, err := range f.fail {
		if strings.Contains(call, text) {
			return err
		}
	}
	for text, out := range f.answers {
		if strings.Contains(call, text) {
			_, _ = io.WriteString(stdout, out)
			return nil
		}
	}
	return nil
}

const password = "0123456789abcdef0123456789abcdef0123456789abcdef"

func manageWith(t *testing.T, f *fakeRunner) func(op, stdin string) (string, error) {
	t.Helper()
	m := &manager{run: f, password: password, tmp: t.TempDir()}
	return func(op, stdin string) (string, error) {
		var out bytes.Buffer
		err := m.do(context.Background(), op, strings.NewReader(stdin), &out)
		return out.String(), err
	}
}

func TestDescribeAndReady(t *testing.T) {
	f := &fakeRunner{answers: map[string]string{"mysqld --version": "/usr/sbin/mysqld  Ver 8.4.13 for Linux on x86_64 (MySQL Community Server - GPL)\n"}}
	manage := manageWith(t, f)
	out, err := manage("describe", "{}\n")
	if err != nil || !strings.Contains(out, `"contract":"cf-database/1"`) || !strings.Contains(out, `"version":"8.4.13"`) || !strings.Contains(out, `"restore"`) {
		t.Fatalf("describe = %q, %v", out, err)
	}
	if out, err := manage("ready", "{}\n"); err != nil || out != "{\"ready\":true}\n" {
		t.Errorf("ready = %q, %v", out, err)
	}
	// The ping goes to the TCP port: the server of the first setup has no network.
	if got := f.calls[len(f.calls)-1]; got != "mysqladmin --no-defaults --protocol=tcp --host=127.0.0.1 --port=3306 --connect-timeout=5 ping" {
		t.Errorf("call = %q", got)
	}
	f.fail = map[string]error{"ping": errors.New("exit status 1")}
	if out, err := manage("ready", ""); err != nil || out != "{\"ready\":false}\n" {
		t.Errorf("ready = %q, %v", out, err)
	}
}

func TestSizesHasEachDatabase(t *testing.T) {
	f := &fakeRunner{answers: map[string]string{"information_schema": "shop_a1b2_app\t49152\nshop_a1b2_empty\t0\n"}}
	manage := manageWith(t, f)
	out, err := manage("sizes", "{}\n")
	if err != nil || out != `{"sizes":[{"name":"shop_a1b2_app","bytes":49152},{"name":"shop_a1b2_empty","bytes":0}]}`+"\n" {
		t.Fatalf("sizes = %q, %v", out, err)
	}
	if !strings.Contains(f.calls[0], "information_schema_stats_expiry = 0") || !strings.Contains(f.calls[0], "LEFT JOIN") {
		t.Errorf("call = %q", f.calls[0])
	}
	// The password is in the option file, not in the arguments.
	if strings.Contains(f.calls[0], password) || f.options != "[client]\nuser=root\npassword="+password+"\nsocket=/tmp/mysqld.sock\n" {
		t.Errorf("call = %q, options = %q", f.calls[0], f.options)
	}
	f.answers = map[string]string{"information_schema": "shop\tmany\n"}
	if _, err := manage("sizes", ""); err == nil {
		t.Error("a size that is not a number must be an error")
	}
	f.answers = map[string]string{"information_schema": ""}
	if out, err := manage("sizes", ""); err != nil || out != "{\"sizes\":[]}\n" {
		t.Errorf("sizes = %q, %v", out, err)
	}
}

func TestDatabases(t *testing.T) {
	f := &fakeRunner{answers: map[string]string{"FROM mysql.db": "shop_a1b2_app\t%\tshop\\_a1b2\\_app\nshop_a1b2_ro\t%\tshop\\_a1b2\\_app\nroot\tlocalhost\tshop\\_a1b2\\_app\n"}}
	manage := manageWith(t, f)
	if out, err := manage("create-database", `{"name":"shop_a1b2_app"}`+"\n"); err != nil || out != "{}\n" {
		t.Fatalf("create-database = %q, %v", out, err)
	}
	if !strings.Contains(f.calls[0], "CREATE DATABASE `shop_a1b2_app` CHARACTER SET utf8mb4") || strings.Contains(f.calls[0], "IF NOT EXISTS") {
		t.Errorf("call = %q", f.calls[0])
	}
	if _, err := manage("drop-database", `{"name":"shop_a1b2_app"}`+"\n"); err != nil {
		t.Fatal(err)
	}
	// The grants of the users go with the database. A grant of a different account stays.
	if !strings.Contains(f.calls[1], `WHERE Db IN ('shop_a1b2_app', 'shop\\_a1b2\\_app')`) {
		t.Errorf("call = %q", f.calls[1])
	}
	want := "REVOKE IF EXISTS ALL PRIVILEGES ON `shop\\_a1b2\\_app`.* FROM 'shop_a1b2_app'@'%';\nREVOKE IF EXISTS ALL PRIVILEGES ON `shop\\_a1b2\\_app`.* FROM 'shop_a1b2_ro'@'%';\nDROP DATABASE IF EXISTS `shop_a1b2_app`;\n"
	if got := f.calls[2]; !strings.HasSuffix(got, "\n"+want) {
		t.Errorf("call = %q, want the SQL %q", got, want)
	}
}

func TestSetUser(t *testing.T) {
	f := &fakeRunner{answers: map[string]string{"FROM mysql.user": "0\n"}}
	manage := manageWith(t, f)
	in := `{"user":"shop_a1b2_app","password":"Abcdefghij0123456789","databases":[{"name":"shop_a1b2_app","access":"readwrite"},{"name":"shop_a1b2_logs","access":"readonly"}]}` + "\n"
	if out, err := manage("set-user", in); err != nil || out != "{}\n" {
		t.Fatalf("set-user = %q, %v", out, err)
	}
	want := "CREATE USER 'shop_a1b2_app'@'%' IDENTIFIED BY 'Abcdefghij0123456789';\nREVOKE ALL PRIVILEGES, GRANT OPTION FROM 'shop_a1b2_app'@'%';\n" +
		"GRANT ALL PRIVILEGES ON `shop\\_a1b2\\_app`.* TO 'shop_a1b2_app'@'%';\nGRANT SELECT, SHOW VIEW ON `shop\\_a1b2\\_logs`.* TO 'shop_a1b2_app'@'%';\n"
	if got := f.calls[1]; !strings.HasSuffix(got, "\n"+want) {
		t.Errorf("call = %q, want the SQL %q", got, want)
	}

	// A user that exists keeps its password when the input has none, and can have no databases.
	f.answers = map[string]string{"FROM mysql.user": "1\n"}
	f.calls = nil
	if _, err := manage("set-user", `{"user":"shop_a1b2_app","databases":[]}`+"\n"); err != nil {
		t.Fatal(err)
	}
	if got := f.calls[1]; strings.Contains(got, "IDENTIFIED") || !strings.HasSuffix(got, "\nREVOKE ALL PRIVILEGES, GRANT OPTION FROM 'shop_a1b2_app'@'%';\n") {
		t.Errorf("call = %q", got)
	}
	f.calls = nil
	if _, err := manage("set-user", `{"user":"shop_a1b2_app","password":"Zyxwvutsrq9876543210","databases":[]}`+"\n"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.calls[1], "ALTER USER 'shop_a1b2_app'@'%' IDENTIFIED BY 'Zyxwvutsrq9876543210';") {
		t.Errorf("call = %q", f.calls[1])
	}

	// A new user needs a password.
	f.answers = map[string]string{"FROM mysql.user": "0\n"}
	if _, err := manage("set-user", `{"user":"shop_a1b2_new","databases":[]}`+"\n"); err == nil || !strings.Contains(err.Error(), "give a password") {
		t.Errorf("err = %v", err)
	}
	if _, err := manage("drop-user", `{"user":"shop_a1b2_app"}`+"\n"); err != nil || !strings.Contains(f.calls[len(f.calls)-1], "DROP USER IF EXISTS 'shop_a1b2_app'@'%';") {
		t.Errorf("drop-user: %v, %q", err, f.calls[len(f.calls)-1])
	}
}

func TestRefusesInputThatIsNotValid(t *testing.T) {
	f := &fakeRunner{}
	manage := manageWith(t, f)
	for _, c := range []struct{ op, in string }{
		{"create-database", `{"name":"shop; DROP DATABASE mysql"}`},
		{"create-database", `{"name":"Shop"}`},
		{"create-database", `{"name":"mysql"}`},
		{"create-database", `{"name":"shop_a1b2_app","owner":"x"}`},
		{"create-database", `{"name":"a1"} {"name":"b2"}`},
		{"create-database", `{}`},
		{"drop-database", `{"name":"shop`},
		{"dump", `{"name":"sys"}`},
		{"set-user", `{"user":"root","password":"Abcdefghij0123456789","databases":[]}`},
		{"set-user", `{"user":"a_user_name_with_more_than_32_chars","password":"Abcdefghij0123456789","databases":[]}`},
		{"set-user", `{"user":"shop_a1b2_app","password":"short","databases":[]}`},
		{"set-user", `{"user":"shop_a1b2_app","password":"Abcdefghij0123456789'","databases":[]}`},
		{"set-user", `{"user":"shop_a1b2_app","databases":[{"name":"shop_a1b2_app","access":"admin"}]}`},
		{"set-user", `{"user":"shop_a1b2_app","databases":[{"name":"shop_a1b2_app","access":"readonly"},{"name":"shop_a1b2_app","access":"readwrite"}]}`},
		{"set-user", `{"user":"shop_a1b2_app","databases":[{"name":"x%","access":"readonly"}]}`},
		{"drop-user", `{"user":"mysql.sys"}`},
		{"describe", `{"x":1}`},
		{"backup", `{}`},
	} {
		if _, err := manage(c.op, c.in+"\n"); err == nil {
			t.Errorf("%s %s: no error", c.op, c.in)
		}
	}
	// The checks come before each client program.
	for _, call := range f.calls {
		if !strings.Contains(call, "FROM mysql.user") {
			t.Errorf("a refused input ran %q", call)
		}
	}
	m := &manager{run: f, password: "", tmp: t.TempDir()}
	if err := m.do(context.Background(), "sizes", strings.NewReader("{}\n"), io.Discard); err == nil || !strings.Contains(err.Error(), "CF_SUPERUSER_PASSWORD") {
		t.Errorf("err = %v", err)
	}
}

func TestDumpAndRestoreStreamTheData(t *testing.T) {
	f := &fakeRunner{answers: map[string]string{"mysqldump": "-- MySQL dump\nCREATE TABLE `t` (`id` int);\n"}}
	manage := manageWith(t, f)
	out, err := manage("dump", `{"name":"shop_a1b2_app"}`+"\n")
	if err != nil || out != "-- MySQL dump\nCREATE TABLE `t` (`id` int);\n" {
		t.Fatalf("dump = %q, %v", out, err)
	}
	if got := f.calls[0]; !strings.HasSuffix(got, " --single-transaction --routines --events --triggers --no-tablespaces --set-gtid-purged=OFF --hex-blob --default-character-set=utf8mb4 shop_a1b2_app") || strings.Contains(got, "--databases") {
		t.Errorf("call = %q", got)
	}
	f.calls = nil
	out, err = manage("restore", `{"name":"shop_a1b2_copy"}`+"\nCREATE TABLE `t` (`id` int);\nINSERT INTO `t` VALUES (1);\n")
	if err != nil || out != "{}\n" {
		t.Fatalf("restore = %q, %v", out, err)
	}
	if !strings.Contains(f.calls[0], "DROP DATABASE IF EXISTS `shop_a1b2_copy`;\nCREATE DATABASE `shop_a1b2_copy`") {
		t.Errorf("call = %q", f.calls[0])
	}
	// The dump after the first line goes to the client, with no change.
	if !strings.HasSuffix(f.calls[1], "--database=shop_a1b2_copy\nCREATE TABLE `t` (`id` int);\nINSERT INTO `t` VALUES (1);\n") {
		t.Errorf("call = %q", f.calls[1])
	}
}
