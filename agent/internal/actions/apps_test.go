package actions

import (
	"errors"
	"os"
	"strings"
	"testing"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/execx"
)

// The tests of the Node.js and Python runtimes and of the environment variables of a website.

const nodeImage = "ghcr.io/cloudfactory/node24:0.2.0"

func nodeWebsite() *agentv1.Website {
	w := website(nodeImage)
	w.Runtime, w.Extensions, w.PhpIni = "node", nil, nil
	w.DocumentRoot = "."
	w.Command = []string{"sh", "-c", "npm start"}
	w.Environment = map[string]string{"NODE_ENV": "production", "DATABASE_URL": "postgres://app:s3cret@postgres.internal:5432/app"}
	return w
}

func TestNodeWebsiteRunsItsStartCommandWithItsVariables(t *testing.T) {
	d, f, _ := deps(t)
	if err := run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: nodeWebsite()})); err != nil {
		t.Fatal(err)
	}
	unit := readFile(t, mustUnitDir(t, d)+"/cf-acme_x7k2.container")
	env := d.envPath("acme_x7k2", "")
	for _, want := range []string{
		"Image=" + nodeImage,
		"PublishPort=169.254.77.1:20000:8080",
		"Environment=CF_APP_DIR=/app",
		"Environment=PORT=8080",
		"Environment=HOST=0.0.0.0",
		"WorkingDir=/app",
		"EnvironmentFile=" + env,
		`Exec="sh" "-c" "npm start"`,
		"ReadOnly=true",
		"DropCapability=all",
	} {
		if !strings.Contains(unit, want+"\n") {
			t.Errorf("the unit has no %q:\n%s", want, unit)
		}
	}
	// A Node.js website has no php.ini and no document root of PHP.
	if strings.Contains(unit, "zz-cloudfactory.ini") || strings.Contains(unit, "CF_DOCUMENT_ROOT") {
		t.Errorf("the unit has PHP settings:\n%s", unit)
	}
	// The values are in a file that only the website user can read, not in the unit or in a
	// command line.
	if strings.Contains(unit, "s3cret") {
		t.Error("a value is in the unit")
	}
	for _, c := range f.Calls {
		if strings.Contains(c, "s3cret") {
			t.Errorf("a value is in a command line: %s", c)
		}
	}
	if got := readFile(t, env); got != "DATABASE_URL=postgres://app:s3cret@postgres.internal:5432/app\nNODE_ENV=production\n" {
		t.Errorf("environment file = %q", got)
	}
	if fi, err := os.Stat(env); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("mode of the environment file: %v %v", fi, err)
	}
	// A new Node.js website gets no PHP page.
	if f.Called(asSite + "sh -c [ -n") {
		t.Error("the agent wrote a PHP page for a Node.js website")
	}
}

func TestAWebsiteWithNoVariablesHasNoEnvironmentFile(t *testing.T) {
	d, _, _ := deps(t)
	w := nodeWebsite()
	w.Environment = nil
	if err := run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: w})); err != nil {
		t.Fatal(err)
	}
	if unit := readFile(t, mustUnitDir(t, d)+"/cf-acme_x7k2.container"); strings.Contains(unit, "\nEnvironmentFile=") {
		t.Errorf("unit:\n%s", unit)
	}
	if _, err := os.Stat(d.envPath("acme_x7k2", "")); !os.IsNotExist(err) {
		t.Errorf("environment file: %v", err)
	}
}

func TestPHPWebsiteGetsItsVariables(t *testing.T) {
	d, _, _ := deps(t)
	w := website(siteImage)
	w.Environment = map[string]string{"APP_ENV": "production"}
	if err := run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: w})); err != nil {
		t.Fatal(err)
	}
	unit := readFile(t, mustUnitDir(t, d)+"/cf-acme_x7k2.container")
	if !strings.Contains(unit, "EnvironmentFile="+d.envPath("acme_x7k2", "")+"\n") || !strings.Contains(unit, "zz-cloudfactory.ini") {
		t.Errorf("unit:\n%s", unit)
	}
}

func TestInstallDependenciesBuildsAndStartsAgainWithNoStop(t *testing.T) {
	d, f, edge := deps(t)
	if err := run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: nodeWebsite()})); err != nil {
		t.Fatal(err)
	}
	f.Calls, f.Inputs = nil, nil
	job, err := d.InstallDependencies(&agentv1.InstallDependenciesRequest{JobId: jobID2, Website: nodeWebsite(), Command: "npm ci --omit=dev"})
	if err != nil {
		t.Fatal(err)
	}
	if err := runSteps(t, job, nil); err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, s := range job.Steps {
		names = append(names, s.Name)
	}
	if got := strings.Join(names, "|"); got != "Install dependencies|Check image signature|Start new container on a copy|Health check|Switch traffic|Stop old container" {
		t.Errorf("steps = %s", got)
	}
	build, _ := called(f, buildStart)
	home := d.Host.Home("acme_x7k2")
	for _, want := range []string{
		"--read-only --tmpfs=/tmp --env=HOME=/tmp",
		"--workdir=/app --env=CF_APP_DIR=/app --entrypoint=/usr/local/bin/cf-app",
		"--env-file=" + home + "/.cf-deploy/install.env",
		nodeImage + " sh -c set -e\nexec 1>&2\nnpm ci --omit=dev sh",
	} {
		if !strings.Contains(build, want) {
			t.Errorf("the install command has no %q:\n%s", want, build)
		}
	}
	if !f.Called(asSite + "rm -rf -- " + home + "/.cf-deploy") {
		t.Error("the agent did not remove the environment file of the install")
	}
	site, _ := d.State.Get(websiteID)
	if site.Suffix != "-b" || edge.sites[websiteID].Port != site.Port {
		t.Errorf("site = %+v", site)
	}
}

func TestInstallDependenciesKeepsTheWebsiteWhenTheCommandFails(t *testing.T) {
	d, f, edge := deps(t)
	if err := run(t)(d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: nodeWebsite()})); err != nil {
		t.Fatal(err)
	}
	f.Rules = append(f.Rules, execx.Rule{Prefix: buildStart, Err: errors.New("runuser -u acme_x7k2 -- env …: exit status 1: npm error missing package-lock.json")})
	job, err := d.InstallDependencies(&agentv1.InstallDependenciesRequest{JobId: jobID2, Website: nodeWebsite(), Command: "npm ci"})
	if err != nil {
		t.Fatal(err)
	}
	err = runSteps(t, job, nil)
	if err == nil || err.Error() != "the install command failed: exit status 1: npm error missing package-lock.json" {
		t.Fatalf("err = %v", err)
	}
	site, _ := d.State.Get(websiteID)
	if site.Suffix != "" || edge.sites[websiteID].Port != 20000 || f.Called("systemctl --user --machine=acme_x7k2@ restart cf-acme_x7k2-b.service") {
		t.Errorf("the website changed: %+v", site)
	}
}

func TestAppRuntimesRefuseBadInput(t *testing.T) {
	d, _, _ := deps(t)
	noCommand := nodeWebsite()
	noCommand.Command = nil
	badName := nodeWebsite()
	badName.Environment = map[string]string{"PORT": "80"}
	badValue := nodeWebsite()
	badValue.Environment = map[string]string{"A": "x\nB=y"}
	badRuntime := nodeWebsite()
	badRuntime.Runtime = "ruby"
	for name, w := range map[string]*agentv1.Website{"no start command": noCommand, "PORT": badName, "a line feed": badValue, "ruby": badRuntime} {
		if _, err := d.CreateWebsite(&agentv1.WebsiteRequest{JobId: jobID, Website: w}); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if _, err := d.InstallDependencies(&agentv1.InstallDependenciesRequest{JobId: jobID, Website: website(siteImage), Command: "npm ci"}); err == nil {
		t.Error("an install command for PHP: no error")
	}
	if _, err := d.InstallDependencies(&agentv1.InstallDependenciesRequest{JobId: jobID, Website: nodeWebsite(), Command: "npm ci\nrm -rf /app"}); err == nil {
		t.Error("an install command on two lines: no error")
	}
}

func TestTheRootHelperWritesOnlyValidEnvironmentFiles(t *testing.T) {
	for _, bad := range []string{"NOEQUALS\n", "PORT=1\n", "CF_X=1\n", "A=1\n\nB=2\n", "1A=b\n"} {
		if err := checkEnvFile(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
	for _, good := range []string{"", "A=\n", "A=b=c\nB=d e\n"} {
		if err := checkEnvFile(good); err != nil {
			t.Errorf("%q: %v", good, err)
		}
	}
}
