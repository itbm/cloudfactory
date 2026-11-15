package jail

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestMain lets the test program start a jail of itself: Main replaces the process.
func TestMain(m *testing.M) {
	if os.Getenv("CF_JAIL_TEST") == "1" {
		os.Unsetenv("CF_JAIL_TEST")
		os.Stderr.WriteString(Main(os.Args[1:]).Error() + "\n")
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func TestArgsAndParseAgree(t *testing.T) {
	o := Options{ReadOnly: []string{"/srv/a", "/srv/b"}, Tmp: "/var/tmp/x", Argv: []string{"restic", "backup", "/srv/a"}}
	args := o.Args()
	if args[0] != Command {
		t.Fatalf("the first argument is %q", args[0])
	}
	got, err := Parse(args[1:])
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, o) {
		t.Fatalf("got %+v, want %+v", got, o)
	}
}

func TestParseRefusesBadArguments(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"--"},
		{"--", ""},
		{"--ro", "/srv/a"},
		{"--ro", "relative", "--", "true"},
		{"--ro", "/srv/../etc", "--", "true"},
		{"--ro", "/srv/a/", "--", "true"},
		{"--ro", "/", "--", "true"},
		{"--tmp", "/a", "--tmp", "/b", "--", "true"},
		{"--rw", "/srv/a", "--", "true"},
		{"true"},
	} {
		if _, err := Parse(args); err == nil {
			t.Errorf("Parse(%q) gave no error", args)
		}
	}
}

func TestAPathOfTheCallerCannotReplaceAPartOfTheJail(t *testing.T) {
	for _, p := range []string{"/usr", "/usr/bin", "/dev/null", "/tmp/x", "/bin", "/lib64/x", "/etc", "/etc/passwd", "/etc/ssl/certs", "/.host"} {
		if checkPlace(p) == nil {
			t.Errorf("checkPlace(%q) gave no error", p)
		}
	}
	for _, p := range []string{"/etc/cloudfactory/restic.key", "/srv/cloudfactory/sites/u/app", "/usrlocal"} {
		if err := checkPlace(p); err != nil {
			t.Errorf("checkPlace(%q): %v", p, err)
		}
	}
}

// run starts a shell script in a jail. It skips the test where a jail is not possible.
func run(t *testing.T, o Options, script string) string {
	t.Helper()
	if os.Getuid() != 0 {
		t.Skip("a jail needs root")
	}
	o.Argv = []string{"sh", "-c", script}
	cmd := exec.Command(os.Args[0], o.Args()[1:]...)
	cmd.Env = append(os.Environ(), "CF_JAIL_TEST=1")
	out, err := cmd.CombinedOutput()
	if err != nil && strings.Contains(string(out), "operation not permitted") {
		t.Skipf("this system does not permit a mount namespace: %s", out)
	}
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	return string(out)
}

// dir makes a directory that is not in a directory of the jail: t.TempDir is in /tmp.
func dir(t *testing.T) string {
	t.Helper()
	if os.Getuid() != 0 {
		t.Skip("a jail needs root")
	}
	d, err := os.MkdirTemp("/var/tmp", "cf-jail-test-")
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func TestTheJailHasOnlyThePathsOfTheCaller(t *testing.T) {
	root := dir(t)
	site, secret, tmp := filepath.Join(root, "site"), filepath.Join(root, "secret"), filepath.Join(root, "tmp")
	for _, d := range []string{site, secret, tmp} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(site, "index.php"), []byte("website\n"), 0o600)
	os.WriteFile(filepath.Join(secret, "key"), []byte("SECRET\n"), 0o600)
	// The owner of the website points a link at a secret of the host.
	os.Symlink(secret, filepath.Join(site, "link"))
	os.Symlink("/etc/shadow", filepath.Join(site, "shadow"))

	out := run(t, Options{ReadOnly: []string{site}, Tmp: tmp}, strings.NewReplacer("$0", site, "$1", secret).Replace(`
		cat "$0/index.php"
		cat "$0/link/key" 2>/dev/null && echo LEAK-LINK
		cat "$0/shadow" 2>/dev/null && echo LEAK-SHADOW
		[ -e "$1" ] && echo LEAK-PATH
		[ -e /proc/self ] && echo LEAK-PROC
		[ -e /root ] && echo LEAK-ROOT
		[ -e /etc/shadow ] && echo LEAK-ETC
		touch "$0/new" 2>/dev/null && echo WRITE-SITE
		touch /new 2>/dev/null && echo WRITE-ROOT
		echo temporary > /tmp/file && echo TMP-OK
		echo x > /dev/null && echo DEV-OK
		true
	`))
	for _, bad := range []string{"LEAK", "WRITE", "SECRET"} {
		if strings.Contains(out, bad) {
			t.Errorf("the jail shows or changes a path of the host (%s):\n%s", bad, out)
		}
	}
	for _, good := range []string{"website", "TMP-OK", "DEV-OK"} {
		if !strings.Contains(out, good) {
			t.Errorf("the output has no %q:\n%s", good, out)
		}
	}
	if b, err := os.ReadFile(filepath.Join(tmp, "file")); err != nil || string(b) != "temporary\n" {
		t.Errorf("the temporary file is not in the directory of the host: %q, %v", b, err)
	}
	// The mounts of the jail are not on the host.
	if entries, err := os.ReadDir(base); err == nil && len(entries) != 0 {
		t.Errorf("%s is not empty on the host", base)
	}
}

func TestTheJailRefusesAPathWithASymbolicLink(t *testing.T) {
	root := dir(t)
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	os.Symlink(target, link)
	if os.Getuid() != 0 {
		t.Skip("a jail needs root")
	}
	cmd := exec.Command(os.Args[0], Options{ReadOnly: []string{link}, Argv: []string{"true"}}.Args()[1:]...)
	cmd.Env = append(os.Environ(), "CF_JAIL_TEST=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("the jail started with a link as its path: %s", out)
	}
	if strings.Contains(string(out), "operation not permitted") {
		t.Skipf("this system does not permit a mount namespace: %s", out)
	}
}
