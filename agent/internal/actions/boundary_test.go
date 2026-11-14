package actions

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/itbm/cloudfactory/agent/internal/execx"
	"github.com/itbm/cloudfactory/agent/internal/gate"
	"github.com/itbm/cloudfactory/agent/internal/hostupdate"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
	"github.com/itbm/cloudfactory/agent/internal/selfupdate"
)

// Second audit, the privilege boundary. The other tests of this package show that the worker
// can do its work through the root helper. These tests show what the worker cannot do: a
// worker that an attacker controls gets no rights of root from the helper.

// boundary gives a worker with the Unix users of a server: two website users, the service
// users, and three users that are not for the worker.
func boundary(t *testing.T) (*Deps, *execx.Fake) {
	t.Helper()
	d, f, _ := deps(t)
	ids := map[string]int{
		"acme_x7k2": 20001, "bolt_k3m9": 20002, DBUser: 990, "cf-edge": 991,
		// A user of an administrator whose name has the form of a website user.
		"admin_ab12": 1000,
		"root":       0, "cf-agent": 989, "cf-panel": 988,
	}
	both(d, func(d *Deps) {
		d.Host.LookupUID = func(name string) (int, error) {
			if id, ok := ids[name]; ok {
				return id, nil
			}
			return 0, user.UnknownUserError(name)
		}
		d.EdgeImage = "ghcr.io/itbm/edge:0.2.0"
		d.ResticPasswordFile = "/etc/cloudfactory/restic.key"
	})
	return d, f
}

const (
	dbPodman   = "runuser -u cf-db -- env XDG_RUNTIME_DIR=/run/user/990 podman "
	edgePodman = "runuser -u cf-edge -- env XDG_RUNTIME_DIR=/run/user/991 podman "
	mariaName  = "cf-mariadb-" + mariaServerKey
	resticHead = "restic --repo s3:b/c --password-file /etc/cloudfactory/restic.key --json --retry-lock 30m "
	tagOne     = "website=" + websiteID
)

func TestTheRootHelperRunsTheCommandsOfThePolicy(t *testing.T) {
	d, f := boundary(t)
	root := rootOf(d)
	app, dumps, stage := root.Host.AppDir("acme_x7k2"), root.dumpDir("acme_x7k2"), root.stageDir(jobID)
	for _, command := range []string{
		// A website user can run each program.
		"runuser -u acme_x7k2 -- sh -c id",
		"runuser -u acme_x7k2 -- env XDG_RUNTIME_DIR=/run/user/20001 podman run --rm registry.example/acme/app:1",
		// The service users: a database client in a database container, and the images of this
		// agent version.
		dbPodman + "exec -i " + mariaName + " mariadb --batch",
		dbPodman + "exec cf-postgresql pg_isready",
		dbPodman + "exec --env-file " + filepath.Join(root.secretsDir(), "env-12") + " " + mariaName + " mariadb --sandbox",
		dbPodman + "pull --quiet " + root.serviceImage("mariadb"),
		edgePodman + "image exists ghcr.io/itbm/edge:0.2.0",
		// The user managers.
		"systemctl --user --machine=acme_x7k2@ daemon-reload",
		"systemctl --user --machine=acme_x7k2@ restart cf-acme_x7k2.service",
		"systemctl --user --machine=acme_x7k2@ set-property --runtime cf-acme_x7k2.service CPUQuota=100% MemoryMax=1073741824 TasksMax=100",
		"systemctl --user --machine=cf-db@ stop " + mariaName + ".service",
		"systemctl --user --machine=cf-edge@ is-active cf-edge.service",
		// Root.
		"systemctl is-active ssh",
		"systemctl reboot",
		"nft list table inet cloudfactory",
		"apt-get -qq update",
		resticHead + "init",
		resticHead + "snapshots --tag " + tagOne,
		resticHead + "forget --tag " + tagOne + ",kind=scheduled --group-by  --keep-daily 7 --keep-weekly 4",
		resticHead + "backup " + app + " " + dumps + " --tag " + tagOne + " --tag kind=manual",
		resticHead + "restore 1a2b3c4d --target " + stage + " --include " + app + "/public/index.php",
	} {
		if _, err := d.Run.Run(context.Background(), words(command)[0], words(command)[1:]...); err != nil {
			t.Errorf("%s: %v", command, err)
		}
	}
	if len(f.Calls) == 0 {
		t.Error("the fake runner got no command")
	}
}

// words splits a command of a test at each space. Two spaces give an empty argument.
func words(command string) []string { return strings.Split(command, " ") }

func TestTheRootHelperRefusesCommandsOutsideThePolicy(t *testing.T) {
	d, f := boundary(t)
	root := rootOf(d)
	app, dumps, stage := root.Host.AppDir("acme_x7k2"), root.dumpDir("acme_x7k2"), root.stageDir(jobID)
	secret := filepath.Join(root.secretsDir(), "env-12")
	for _, command := range []string{
		// Root runs no program that the worker selects.
		"sh -c id",
		"bash",
		"id",
		"install -d -o cf-agent /etc/cloudfactory",
		"chown cf-agent /etc/shadow",
		"cat /etc/shadow",
		"podman run --rm --privileged docker.io/library/alpine",
		"podman pull --quiet docker.io/library/alpine",
		"nft -f /tmp/rules.nft",
		"nft flush ruleset",
		"nft list ruleset",
		"systemctl start ssh",
		"systemctl stop cf-agent",
		"systemctl daemon-reload",
		"systemctl is-active nginx",
		"systemctl is-active ssh --host=other",
		"systemctl reboot --force",
		"systemctl edit cf-agent",
		"apt-get install -y netcat-openbsd",
		"apt-get -qq update -o APT::Update::Pre-Invoke::=id",
		"apt-mark unhold podman",
		"useradd -o -u 0 second",
		"usermod --password x root",

		// runuser has one form, and only for the users of the websites and of the services.
		"runuser -u root -- id",
		"runuser -u cf-agent -- id",
		"runuser -u cf-panel -- id",
		"runuser -u admin_ab12 -- id",
		"runuser -u ghost_ab12 -- id",
		"runuser -u acme_x7k2 id",
		"runuser -l acme_x7k2 -c id",
		"runuser --user=root -- id",
		"runuser -u acme_x7k2 -g root -- id",
		"runuser -u acme_x7k2 --",
		"runuser - root",

		// The worker cannot run a program of its choice as a service user.
		"runuser -u cf-db -- sh -c id",
		"runuser -u cf-db -- podman ps",
		"runuser -u cf-db -- env LD_PRELOAD=/tmp/x.so podman ps",
		"runuser -u cf-db -- env XDG_RUNTIME_DIR=/run/user/20001 podman exec " + mariaName + " mariadb",
		dbPodman + "run --rm --privileged -v /:/host docker.io/library/alpine",
		dbPodman + "exec " + mariaName + " sh -c id",
		dbPodman + "exec " + mariaName + " bash",
		dbPodman + "exec --privileged " + mariaName + " mariadb",
		dbPodman + "exec --user root " + mariaName + " mariadb",
		dbPodman + "exec -i -i " + mariaName + " mariadb",
		dbPodman + "exec --env-file /etc/shadow " + mariaName + " mariadb",
		dbPodman + "exec --env-file " + secret + "/../../x " + mariaName + " mariadb",
		dbPodman + "exec cf-edge mariadb",
		dbPodman + "exec cf-mariadb-../x mariadb",
		dbPodman + "exec " + mariaName,
		dbPodman + "cp " + mariaName + ":/etc/passwd /tmp/x",
		dbPodman + "pull --quiet docker.io/library/alpine",
		dbPodman + "pull --quiet --root=/tmp/x " + root.serviceImage("mariadb"),
		dbPodman + "image exists ",
		edgePodman + "pull --quiet docker.io/library/alpine",
		edgePodman + "exec cf-edge sh",
		edgePodman + "run --rm docker.io/library/alpine",

		// systemctl goes only to the user manager of a website or of a service.
		"systemctl --user --machine=root@ start x.service",
		"systemctl --user --machine=cf-agent@ start x.service",
		"systemctl --user --machine=admin_ab12@ start x.service",
		"systemctl --user --machine=acme_x7k2@other start x.service",
		"systemctl --user --machine=acme_x7k2@ --system start ssh.service",
		"systemctl --user --machine=acme_x7k2@ start --host=x.service",
		"systemctl --user --machine=acme_x7k2@ start x.service y.service",
		"systemctl --user --machine=acme_x7k2@ start x.socket",
		"systemctl --user --machine=acme_x7k2@ enable x.service",
		"systemctl --user --machine=acme_x7k2@ edit x.service",
		"systemctl --user --machine=acme_x7k2@ set-environment A=b",
		"systemctl --user --machine=acme_x7k2@ set-property --runtime x.service ExecStart=/bin/sh",
		"systemctl --user --machine=acme_x7k2@ set-property x.service MemoryMax=1",
		"systemctl --user --machine=cf-db@ set-property --runtime " + mariaName + ".service MemoryMax=1",
		"systemctl --user start x.service",
		"systemctl --user",

		// restic reads and writes only the paths of a website and of a restore job.
		"restic --repo /etc --json --retry-lock 30m init",
		"restic --repo sftp:a@b:/c --json --retry-lock 30m init",
		"restic --repo rclone:x --json --retry-lock 30m init",
		"restic --repo s3:b/c --password-file /etc/shadow --json --retry-lock 30m init",
		"restic --repo s3:b/c --password-command id --json --retry-lock 30m init",
		"restic --repo s3:b/c -o rclone.program=id --json --retry-lock 30m init",
		"restic --repo s3:b/c --json --retry-lock 30m init --copy-chunker-params",
		resticHead + "self-update",
		resticHead + "mount /mnt",
		resticHead + "dump latest /etc/shadow",
		resticHead + "cat masterkey",
		resticHead + "snapshots --tag website=x",
		resticHead + "forget --prune " + "1a2b3c4d",
		resticHead + "forget --tag " + tagOne + ",kind=scheduled --group-by  --keep-daily 7 --prune",
		resticHead + "forget --tag " + tagOne + ",kind=scheduled --group-by ",
		resticHead + "backup /etc " + dumps + " --tag " + tagOne + " --tag kind=manual",
		resticHead + "backup " + app + " /etc --tag " + tagOne + " --tag kind=manual",
		resticHead + "backup " + app + " " + root.dumpDir("bolt_k3m9") + " --tag " + tagOne + " --tag kind=manual",
		resticHead + "backup " + root.Host.AppDir("admin_ab12") + " " + root.dumpDir("admin_ab12") + " --tag " + tagOne + " --tag kind=manual",
		resticHead + "backup " + app + " " + dumps + " --tag " + tagOne + " --tag kind=manual --files-from /etc/shadow",
		resticHead + "restore 1a2b3c4d --target / --include " + app,
		resticHead + "restore 1a2b3c4d --target " + app + " --include " + app,
		resticHead + "restore 1a2b3c4d --target " + stage + "/../" + jobID + " --include " + app,
		resticHead + "restore 1a2b3c4d --target " + stage + " --include /etc/shadow",
		resticHead + "restore 1a2b3c4d --target " + stage + " --include " + root.Host.SitesDir + "/../etc",
		resticHead + "restore latest:/etc --target " + stage + " --include " + app,
		resticHead + "restore 1a2b3c4d --target " + stage + " --include " + app + " --overwrite always",
	} {
		argv := words(command)
		if _, err := d.Run.Run(context.Background(), argv[0], argv[1:]...); err == nil {
			t.Errorf("the root helper ran: %s", command)
		}
	}
	// The other kinds of a command request have the same policy.
	if _, err := d.Run.RunInput(context.Background(), "x", "sh"); err == nil {
		t.Error("the root helper ran sh with an input")
	}
	if err := d.Run.Pipe(context.Background(), []string{"runuser", "-u", "acme_x7k2", "--", "tar", "--create", "."}, []string{"tar", "--extract", "--directory=/"}); err == nil {
		t.Error("the root helper ran tar as root at the end of a pipe")
	}
	if err := d.Run.Pipe(context.Background(), []string{"tar", "--create", "/etc"}, []string{"runuser", "-u", "acme_x7k2", "--", "tar", "--extract"}); err == nil {
		t.Error("the root helper ran tar as root at the start of a pipe")
	}
	if err := d.Run.Stream(context.Background(), nil, nil, "cat", "/etc/shadow"); err == nil {
		t.Error("the root helper ran cat with a stream")
	}
	// Only restic gets environment variables, and only the credentials of a backup target.
	for _, tc := range []struct {
		env  string
		argv []string
	}{
		{"LD_PRELOAD=/tmp/x.so", []string{"systemctl", "is-active", "ssh"}},
		{"LD_PRELOAD=/tmp/x.so", words(resticHead + "init")},
		{"RESTIC_PASSWORD_COMMAND=id", words(resticHead + "init")},
		{"PATH=/tmp", words(resticHead + "init")},
		{"AWS_SHARED_CREDENTIALS_FILE=/etc/shadow", words(resticHead + "init")},
	} {
		if _, err := d.Run.RunEnv(context.Background(), []string{tc.env}, "", tc.argv[0], tc.argv[1:]...); err == nil {
			t.Errorf("the root helper ran %v with %s", tc.argv, tc.env)
		}
	}
	if len(f.Calls) != 0 {
		t.Errorf("the root helper ran: %q", f.Calls)
	}
}

// Each command of the updates of the operating system is in the list of the policy.
func TestTheCommandsOfTheHostUpdatesAreInThePolicy(t *testing.T) {
	d, f := boundary(t)
	apt := hostupdate.New(d.Run)
	ctx := context.Background()
	steps := []func() error{
		func() error { _, err := apt.Pending(ctx); return err },
		func() error { return apt.Hold(ctx) },
		func() error { return apt.Download(ctx) },
		func() error { return apt.Install(ctx, "security") },
		func() error { return apt.Install(ctx, "all") },
		func() error { return apt.Install(ctx, "tools") },
		func() error { return apt.RestartServices(ctx) },
	}
	for i, step := range steps {
		if err := step(); err != nil {
			t.Errorf("step %d: %v", i, err)
		}
	}
	for _, argv := range hostupdate.Commands() {
		if !f.Called(strings.Join(argv, " ")) {
			t.Errorf("the list has a command that the package does not run: %q", argv)
		}
	}
}

// call asks the root helper for one operation and gives the error.
func call(d *Deps, op string, in any) error { return d.Gate.Call(context.Background(), op, in, nil) }

func TestTheRootHelperRefusesValuesOutsideTheLimits(t *testing.T) {
	d, f := boundary(t)
	root := rootOf(d)
	both(d, func(d *Deps) { d.QuotaMount = "/var/lib/cloudfactory" })
	maria := dbServer{ID: mariaServerKey, Engine: "mariadb"}
	record := DatabaseServer{Engine: "mariadb", Port: 3306}
	prepare := func(change func(in *serverIn)) serverIn {
		in := serverIn{Server: maria, Customer: customerKey, Address: customerAddress, Record: record}
		change(&in)
		return in
	}
	dump := []string{"runuser", "-u", "cf-db", "--", "env", "XDG_RUNTIME_DIR=/run/user/990", "podman", "exec", mariaName, "mariadb-dump", "acme_x7k2_shop"}
	for name, tc := range map[string]struct {
		op string
		in any
	}{
		"an operation that is not in the list": {"shell", userIn{User: "acme_x7k2"}},

		"make root":                           {opUserEnsure, userIn{User: "root"}},
		"make a service user":                 {opUserEnsure, userIn{User: "cf-db"}},
		"make a user of an administrator":     {opUserEnsure, userIn{User: "admin_ab12"}},
		"a user name with a path":             {opUserEnsure, userIn{User: "../etc"}},
		"remove a service user":               {opUserRemove, userIn{User: "cf-db"}},
		"remove the user of the worker":       {opUserRemove, userIn{User: "cf-agent"}},
		"remove a user of an administrator":   {opUserRemove, userIn{User: "admin_ab12"}},
		"storage of a user that is not there": {opUserStorage, userIn{User: "ghost_ab12"}},
		"a quota for root":                    {opUserQuota, quotaIn{User: "root", Bytes: 1}},
		"a quota less than 0":                 {opUserQuota, quotaIn{User: "acme_x7k2", Bytes: -1}},

		"a unit of the database user":   {opUnitWrite, unitIn{User: DBUser, Name: mariaName + ".container", Content: "[Container]\n"}},
		"a unit of the edge user":       {opUnitWrite, unitIn{User: "cf-edge", Name: "cf-edge.container", Content: "[Container]\n"}},
		"a unit of root":                {opUnitWrite, unitIn{User: "root", Name: "x.container", Content: "[Container]\n"}},
		"a unit of an administrator":    {opUnitWrite, unitIn{User: "admin_ab12", Name: "x.container", Content: "[Container]\n"}},
		"a unit name with a path":       {opUnitWrite, unitIn{User: "acme_x7k2", Name: "../x.container", Content: "[Container]\n"}},
		"a unit name with a directory":  {opUnitWrite, unitIn{User: "acme_x7k2", Name: "a/x.container", Content: "[Container]\n"}},
		"a service unit":                {opUnitWrite, unitIn{User: "acme_x7k2", Name: "x.service", Content: "[Service]\n"}},
		"a timer unit":                  {opUnitWrite, unitIn{User: "acme_x7k2", Name: "x.timer", Content: "[Timer]\n"}},
		"remove the unit of the edge":   {opUnitRemove, unitIn{User: "cf-edge", Name: "cf-edge.container"}},
		"remove a different cf-db unit": {opUnitRemove, unitIn{User: DBUser, Name: "cf-edge.container"}},
		"remove a cf-db unit by path":   {opUnitRemove, unitIn{User: DBUser, Name: "../" + mariaName + ".container"}},
		"remove a unit of root":         {opUnitRemove, unitIn{User: "root", Name: "x.container"}},

		"an image with no digest":    {opImagesEnsure, imageIn{Ref: "ghcr.io/cloudfactory/frankenphp-php8.4:8.4.0-1"}},
		"an image that is an option": {opImagesEnsure, imageIn{Ref: "--root=/tmp/x@" + sharedDigest}},

		"a release with no root helper": {opAgentReplace, replaceIn{Release: selfupdate.Release{Version: "9.9.9", Manifest: []byte("version=9.9.9\nasset=cf-agent-linux-amd64\nsha256=00\n")}}},
		"a platform that is not valid":  {opPlatform, platformIn{Platform: "ptrace"}},

		"SFTP keys for root":             {opSftpSet, sftpIn{User: "root", Enabled: true}},
		"SFTP keys for an administrator": {opSftpSet, sftpIn{User: "admin_ab12", Enabled: true}},
		"an SFTP key with an option":     {opSftpSet, sftpIn{User: "acme_x7k2", Enabled: true, Keys: []string{`command="id" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGb9ECWmMtLzH8r1Fd1bAQJ5gN1nT3eb0v0f5c0pZzYp`}}},
		"an SFTP key with two lines":     {opSftpSet, sftpIn{User: "acme_x7k2", Enabled: true, Keys: []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGb9ECWmMtLzH8r1Fd1bAQJ5gN1nT3eb0v0f5c0pZzYp\nroot:x:0:0::/:/bin/sh"}}},
		"remove the SFTP keys of a path": {opSftpRemove, userIn{User: "../../etc/passwd"}},

		"a database server with a path":         {opDBPrepare, prepare(func(in *serverIn) { in.Server.ID = "../../etc" })},
		"a database server in upper case":       {opDBPrepare, prepare(func(in *serverIn) { in.Server.ID = mariaServerID })},
		"an engine that is not there":           {opDBPrepare, prepare(func(in *serverIn) { in.Server.Engine = "mongodb" })},
		"a customer with a path":                {opDBPrepare, prepare(func(in *serverIn) { in.Customer = "../x" })},
		"no customer":                           {opDBPrepare, prepare(func(in *serverIn) { in.Customer = "" })},
		"an address outside the network":        {opDBPrepare, prepare(func(in *serverIn) { in.Address = "0.0.0.0" })},
		"an address with a unit line":           {opDBPrepare, prepare(func(in *serverIn) { in.Address = customerAddress + "\nPodmanArgs=--privileged" })},
		"a port of a different service":         {opDBPrepare, prepare(func(in *serverIn) { in.Record.Port = 22 })},
		"a record of a different engine":        {opDBPrepare, prepare(func(in *serverIn) { in.Record.Engine = "postgresql" })},
		"a limit less than 0":                   {opDBPrepare, prepare(func(in *serverIn) { in.Record.DiskBytes = -1 })},
		"a shared container with a customer":    {opDBPrepare, serverIn{Server: dbServer{Engine: "mariadb"}, Customer: customerKey}},
		"a shared Valkey container":             {opDBPrepare, serverIn{Server: dbServer{Engine: "valkey"}}},
		"remove the data of a shared server":    {opDBRemoveData, serverIn{Server: dbServer{Engine: "mariadb"}}},
		"remove the data of a path":             {opDBRemoveData, serverIn{Server: dbServer{ID: "..", Engine: "mariadb"}}},
		"the disk of a path":                    {opDBDisk, serverIn{Server: dbServer{ID: "../..", Engine: "mariadb"}}},
		"the Valkey secret of a MariaDB server": {opDBValkeySecret, serverIn{Server: maria}},
		"database access for root":              {opDBAccess, accessIn{Elements: []accessElement{{UID: 0, Number: 1, Port: 3306}}}},
		"database access for an administrator":  {opDBAccess, accessIn{Elements: []accessElement{{UID: 1000, Number: 1, Port: 3306}}}},
		"database access to the SSH port":       {opDBAccess, accessIn{Elements: []accessElement{{UID: 20001, Number: 1, Port: 22}}}},
		"database access outside the network":   {opDBAccess, accessIn{Elements: []accessElement{{UID: 20001, Number: 1 << 20, Port: 3306}}}},

		"a php.ini with a path":           {opSiteIni, iniIn{User: "acme_x7k2", Suffix: "/../../x", Content: "a=b\n"}},
		"a php.ini of root":               {opSiteIni, iniIn{User: "root", Content: "a=b\n"}},
		"the size of a path":              {opSiteSize, userIn{User: "../.."}},
		"the dumps of a path":             {opDumpsRemove, userIn{User: ".."}},
		"reset the dumps of a path":       {opDumpsReset, userIn{User: "../../etc"}},
		"a dump with a shell":             {opDumpsWrite, dumpIn{User: "acme_x7k2", Engine: "mariadb", Name: "acme_x7k2_shop", Argv: []string{"sh", "-c", "cat /etc/shadow"}}},
		"a dump of a different website":   {opDumpsWrite, dumpIn{User: "acme_x7k2", Engine: "mariadb", Name: "bolt_k3m9_shop", Argv: dump}},
		"a dump with a path in its name":  {opDumpsWrite, dumpIn{User: "acme_x7k2", Engine: "mariadb", Name: "acme_x7k2_../../x", Argv: dump}},
		"a dump of an engine with a path": {opDumpsWrite, dumpIn{User: "acme_x7k2", Engine: "../mariadb", Name: "acme_x7k2_shop", Argv: dump}},

		"a restore job with a path":        {opRestoreStage, jobIn{Job: "../../etc"}},
		"clean a path":                     {opRestoreClean, jobIn{Job: "../.."}},
		"copy from a job with a path":      {opRestoreCopy, copyIn{Job: "..", User: "acme_x7k2", Rel: "."}},
		"copy a path above the website":    {opRestoreCopy, copyIn{Job: jobID, User: "acme_x7k2", Rel: "../bolt_k3m9"}},
		"copy an absolute path":            {opRestoreCopy, copyIn{Job: jobID, User: "acme_x7k2", Rel: "/etc"}},
		"copy a path that is not clean":    {opRestoreCopy, copyIn{Job: jobID, User: "acme_x7k2", Rel: "a/../../b"}},
		"copy a path that is an option":    {opRestoreCopy, copyIn{Job: jobID, User: "acme_x7k2", Rel: "a/../--to-command=id"}},
		"copy to root":                     {opRestoreCopy, copyIn{Job: jobID, User: "root", Rel: "."}},
		"copy to a user that is not there": {opRestoreCopy, copyIn{Job: jobID, User: "ghost_ab12", Rel: "."}},
		"the hash of a different website":  {opRestoreHash, restoreIn{Job: jobID, User: "acme_x7k2", Engine: "mariadb", Name: "bolt_k3m9_shop"}},
		"load a dump with a shell":         {opRestoreLoad, restoreIn{Job: jobID, User: "acme_x7k2", Engine: "mariadb", Name: "acme_x7k2_shop", Argv: []string{"sh"}}},

		"a secret file with a different variable": {opSecretMake, secretIn{Content: "LD_PRELOAD=/tmp/x.so\n"}},
		"a secret file with two lines":            {opSecretMake, secretIn{Content: "MYSQL_PWD=00112233\nLD_PRELOAD=/tmp/x.so\n"}},
		"remove a file that is not a secret":      {opSecretRemove, pathIn{Path: "/etc/passwd"}},
		"remove a secret by a path":               {opSecretRemove, pathIn{Path: root.secretsDir() + "/../secrets/env-1"}},
		"remove a different file of the secrets":  {opSecretRemove, pathIn{Path: filepath.Join(root.secretsDir(), "valkey.acl")}},

		"a reboot mark with a path": {opRebootMark, jobIn{Job: "../x"}},
		"a state that is not JSON":  {opStateWrite, stateIn{Data: []byte("not JSON")}},
	} {
		if err := call(d, tc.op, tc.in); err == nil {
			t.Errorf("%s: the root helper did the operation %s", name, tc.op)
		}
	}
	if len(f.Calls) != 0 {
		t.Errorf("the root helper ran: %q", f.Calls)
	}
	// The helper wrote nothing outside its directories.
	for _, path := range []string{filepath.Join(filepath.Dir(root.DataDir), "x"), filepath.Join(root.DataDir, "x")} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Errorf("%s: %v", path, err)
		}
	}
}

// A request with values that the helper cannot read gives an error, and does no work.
func TestTheRootHelperRefusesValuesOfADifferentType(t *testing.T) {
	d, f := boundary(t)
	for _, in := range []any{"acme_x7k2", 7, []string{"acme_x7k2"}, map[string]any{"User": []string{"acme_x7k2"}}, map[string]any{"User": "acme_x7k2", "Path": "/etc"}} {
		if err := call(d, opUserEnsure, in); err == nil {
			t.Errorf("the root helper accepted %#v", in)
		}
	}
	if len(f.Calls) != 0 {
		t.Errorf("the root helper ran: %q", f.Calls)
	}
}

// The error of an operation of the helper has the cause, also after the gate.
func TestAnErrorOfTheRootHelperKeepsItsCause(t *testing.T) {
	d, _ := boundary(t)
	err := call(d, opSecretRemove, pathIn{Path: "/etc/passwd"})
	var remote *gate.Error
	if !errors.As(err, &remote) || !strings.Contains(err.Error(), "is not a secret file of the agent") {
		t.Errorf("err = %v", err)
	}
}

// The worker connects to a local address only through the root helper, and only to the
// addresses of the list.
func TestTheRootHelperConnectsOnlyToTheAddressesOfTheList(t *testing.T) {
	d, _ := boundary(t)
	root := rootOf(d)
	const service = quadlet.ServiceAddress
	for _, address := range []string{
		service + ":20000", service + ":29999", // the ports of the websites
		service + ":8443",         // the edge proxy
		service + ":2200",         // the SFTP container
		service + ":19443",        // the agent port of the panel
		customerAddress + ":6379", // a Valkey database server
	} {
		if err := root.allowAddress(address); err != nil {
			t.Errorf("%s: %v", address, err)
		}
	}
	for _, address := range []string{
		service + ":19432", // the database of the panel
		service + ":19080", service + ":22", service + ":3306", service + ":5432", service + ":19999", service + ":30000",
		service + ":08443", service + ":+8443", service + ":https", service,
		// MariaDB and PostgreSQL: the agent uses a client in the container.
		customerAddress + ":3306", customerAddress + ":5432", customerAddress + ":22",
		"127.0.0.1:22", "127.0.0.1:8443", "localhost:8443", "[::1]:8443", "0.0.0.0:8443",
		"169.254.169.254:80", "169.254.80.0:6379", "169.254.1.3:8443", "10.0.0.1:6379", "8.8.8.8:53", "example.com:443", ":8443", "",
	} {
		if err := root.allowAddress(address); err == nil {
			t.Errorf("the root helper connects to %s", address)
		}
	}
	// The worker cannot name a socket.
	for _, name := range []string{"", "docker", "unix:/run/docker.sock", "unix:///run/podman/podman.sock", "tcp:127.0.0.1:22", "tcp:" + service + ":19432", "udp:" + service + ":8443", "edge:/etc/passwd"} {
		if conn, err := d.Gate.Dial(context.Background(), name); err == nil {
			conn.Close()
			t.Errorf("the root helper connected to %q", name)
		}
	}
}

// The worker reaches the admin socket of the edge proxy through the root helper.
func TestTheWorkerConnectsToTheEdgeProxyThroughTheRootHelper(t *testing.T) {
	d, _ := boundary(t)
	socket := filepath.Join(t.TempDir(), "admin.sock")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.Write([]byte("edge"))
	}()
	// Only the helper has the path of the socket.
	rootOf(d).EdgeSocket = socket
	conn, err := d.DialEdge(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "edge" {
		t.Errorf("read %q, %v", buf, err)
	}
}

// A fingerprint that is not valid does not go into the settings of the agent.
func TestThePanelCAFingerprintIsChecked(t *testing.T) {
	d, _ := boundary(t)
	var saved string
	rootOf(d).SavePanelCA = func(fingerprint string) error { saved = fingerprint; return nil }
	const pin = "08af88488ce218936d530b80f6f55dc2afe4d0d91f13f9e6ae350a9e7752128e"
	if err := d.SavePanelCAPin(pin); err != nil || saved != pin {
		t.Errorf("saved %q, %v", saved, err)
	}
	for _, bad := range []string{"", "abc", pin + "\",\"agentToken\":\"x", strings.Repeat("z", 64)} {
		if err := call(d, opPanelCA, pinIn{Fingerprint: bad}); err == nil {
			t.Errorf("the root helper saved %q", bad)
		}
	}
	if saved != pin {
		t.Errorf("saved %q", saved)
	}
}
