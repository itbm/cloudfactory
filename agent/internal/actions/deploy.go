package actions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/jobs"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
	"github.com/itbm/cloudfactory/agent/internal/sitelogs"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// Deploys from Git.
//
// Each deploy makes a new release directory, "releases/<release ID>" in the directory of the
// website. The agent gets the code and runs the build commands in a build container: a
// second container from the image of the website, as the Unix user of the website, in a
// gVisor sandbox, with the limits of the website. The host runs no Git and no build command.
//
// Then the agent starts the website from the new release as a change of the runtime with no
// stop (SetRuntime): a new container serves the new release, the agent checks it, and only
// then the traffic goes to it. If a step fails, the website continues from the release that
// it had, and the agent removes the new release.
//
// The link "current" in the directory of the website points to the release that the website
// serves, for example for a cron job. The directory "shared" is for the files that all
// releases use. The agent makes it and never changes it.

const (
	// ReleasesDir is the directory of the releases in the directory of a website.
	ReleasesDir = "releases"
	// maxDeployKey is the largest size of a private SSH key.
	maxDeployKey = 16 << 10
)

// fetchScript gets one commit of a branch into the working directory and writes its ID and
// its subject. "$1" is the branch and "$2" is the repository.
const fetchScript = `set -e
git clone --quiet --depth 1 --single-branch --branch "$1" -- "$2" .
git log -1 --format='cf-commit:%H %s'
rm -rf .git`

// deployKeyPath is the place of the private SSH key in the build container.
const deployKeyPath = "/run/cf-deploy-key"

// knownHostsDir is the place of the list of known hosts in the build container.
const knownHostsDir = "/run/cf-ssh"

// gitSSH is the SSH command of Git in the build container. SSH accepts the key of a Git host
// at the first deploy of the website and keeps it in the list of known hosts of the website
// (Deps.knownHosts). At each later deploy, SSH refuses a host with a different key. Before,
// each deploy had a new list, so each deploy accepted each key, and an attacker in the
// network could give the website different code (adversarial review, finding G5-05).
const gitSSH = "ssh -i " + deployKeyPath + " -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=" + knownHostsDir + "/known_hosts"

// releaseRoot checks that the document root of the website is in the release.
func releaseRoot(w *agentv1.Website, releaseID string) error {
	dir := ReleasesDir + "/" + releaseID
	if root := w.GetDocumentRoot(); root != dir && !strings.HasPrefix(root, dir+"/") {
		return fmt.Errorf("the document root must be in %s", dir)
	}
	return nil
}

// releaseOf gives the release ID of a document root, or "".
func releaseOf(documentRoot string) string {
	parts := strings.Split(documentRoot, "/")
	if len(parts) < 2 || parts[0] != ReleasesDir || validate.ID(parts[1]) != nil {
		return ""
	}
	return parts[1]
}

func checkDeploy(req *agentv1.DeployRequest) error {
	w := req.GetWebsite()
	errs := []error{CheckWebsite(w), validate.ID(req.GetReleaseId()), validate.GitRepository(req.GetRepository()), validate.GitBranch(req.GetBranch())}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	if err := releaseRoot(w, req.GetReleaseId()); err != nil {
		return err
	}
	if len(req.GetBuildCommands()) > validate.MaxBuildCommands {
		return fmt.Errorf("a deploy can have %d build commands", validate.MaxBuildCommands)
	}
	for _, c := range req.GetBuildCommands() {
		if err := validate.CommandLine(c); err != nil {
			return err
		}
	}
	if c := req.GetMigrationCommand(); c != "" {
		if err := validate.CommandLine(c); err != nil {
			return err
		}
	}
	if len(req.GetSecrets()) > validate.MaxDeploySecrets {
		return fmt.Errorf("a deploy can have %d secrets", validate.MaxDeploySecrets)
	}
	for name, value := range req.GetSecrets() {
		if err := errors.Join(validate.SecretName(name), validate.SecretValue(value)); err != nil {
			return err
		}
	}
	if err := checkDeployKey(req.GetDeployKey()); err != nil {
		return err
	}
	for _, id := range req.GetKeepReleases() {
		if err := validate.ID(id); err != nil {
			return err
		}
	}
	return nil
}

// checkDeployKey accepts an empty key or a private SSH key in the OpenSSH format.
func checkDeployKey(key string) error {
	if key != "" && (len(key) > maxDeployKey || !strings.HasPrefix(key, "-----BEGIN OPENSSH PRIVATE KEY-----")) {
		return errors.New("the deploy key is not a private SSH key")
	}
	return nil
}

// asUser runs a program as the Unix user of a website.
func (d *Deps) asUser(ctx context.Context, unixUser, stdin string, args ...string) (string, error) {
	return d.Run.RunInput(ctx, stdin, "runuser", append([]string{"-u", unixUser, "--"}, args...)...)
}

// commandError gives the error of a command with no command line: only the exit status and
// the end of the output. The command line of a build container is long, and the owner of the
// website reads this error.
func commandError(what string, err error) error {
	text := err.Error()
	if i := strings.LastIndex(text, "exit status "); i >= 0 {
		text = text[i:]
	} else if errors.Is(err, context.DeadlineExceeded) || strings.Contains(text, "signal: killed") {
		text = "the time limit ended"
	}
	return fmt.Errorf("%s: %s", what, sitelogs.Clean(text))
}

// commitID is the ID of a Git commit: SHA-1 or SHA-256.
var commitID = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// build runs a script in a build container of the website: a container from the image of the
// website with the directory of the website at /app, which stops when the script ends. The
// working directory is the release. systemd-run puts the container in a unit with the limits
// of the website, as for the service of the website (quadlet.Runtime.GlobalArgs).
func (d *Deps) build(ctx context.Context, w *agentv1.Website, releaseID string, options []string, script string, args ...string) (string, error) {
	return d.buildIn(ctx, w, path.Join("/app", ReleasesDir, releaseID), options, script, args...)
}

// buildIn runs a script in a build container of the website (build), with the working
// directory workdir in the container.
func (d *Deps) buildIn(ctx context.Context, w *agentv1.Website, workdir string, options []string, script string, args ...string) (string, error) {
	return d.buildFor(ctx, orDefault(d.BuildLimit, 20*time.Minute), w, workdir, options, script, args...)
}

// buildFor runs a script in a build container of the website (build) with a time limit.
func (d *Deps) buildFor(ctx context.Context, limit time.Duration, w *agentv1.Website, workdir string, options []string, script string, args ...string) (string, error) {
	user := w.GetUnixUser()
	uid, err := d.Host.LookupUID(user)
	if err != nil {
		return "", err
	}
	u := d.unit(w, 0, "")
	// systemd stops the unit, and thus the build container, at the time limit. The unit does
	// not stop when the agent stops systemd-run (adversarial review, finding G5-02).
	argv := []string{"env", "XDG_RUNTIME_DIR=/run/user/" + strconv.Itoa(uid), "systemd-run", "--user", "--wait", "--collect", "--pipe", "--quiet",
		"-p", "RuntimeMaxSec=" + strconv.Itoa(int(limit.Seconds())), "-p", "KillMode=control-group", "-p", "TimeoutStopSec=15"}
	for _, p := range quadlet.LimitProperties(u.Limits) {
		argv = append(argv, "-p", p)
	}
	argv = append(argv, "--", "podman")
	argv = append(argv, strings.Fields(u.Gvisor.GlobalArgs())...)
	argv = append(argv, "run", "--rm", "--pull=never", "--network=pasta", "--userns=keep-id", "--cap-drop=all",
		"--security-opt=no-new-privileges", "--read-only", "--tmpfs=/tmp", "--env=HOME=/tmp",
		"--volume="+u.SiteDir+":/app:Z", "--workdir="+workdir)
	if quadlet.IsApp(w.GetRuntime()) {
		// The start script of the image puts the programs of the dependencies in the PATH
		// (images/node/cf-app, images/python/cf-app), as in the container of the website.
		argv = append(argv, "--env=CF_APP_DIR="+workdir, "--entrypoint=/usr/local/bin/cf-app")
	}
	if u.DatabaseAddress != "" {
		// A build command, for example a database migration, can connect to the database
		// servers of the customer, as the website can.
		for _, e := range quadlet.Engines() {
			argv = append(argv, "--add-host="+e.Host+":"+u.DatabaseAddress)
		}
	}
	argv = append(argv, options...)
	argv = append(argv, u.Image, "sh", "-c", script, "sh")
	argv = append(argv, args...)
	ctx, cancel := context.WithTimeout(ctx, limit+30*time.Second)
	defer cancel()
	return d.asUser(ctx, user, "", argv...)
}

// knownHosts is the directory on the host with the list of known SSH hosts of a website. It
// is in the home directory of the website user and not in the directory of the website, so
// the website and SFTP cannot change it. Only the step "Get the code" mounts it.
func (d *Deps) knownHosts(unixUser string) string {
	return filepath.Join(d.Host.Home(unixUser), ".cf-ssh")
}

// deployFiles is the directory of the short-lived files of a deploy: the private SSH key and
// the secrets. Only the website user can read it.
func (d *Deps) deployFiles(unixUser string) string {
	return filepath.Join(d.Host.Home(unixUser), ".cf-deploy")
}

// writeDeployFile writes a file of a deploy that only the website user can read.
func (d *Deps) writeDeployFile(ctx context.Context, unixUser, name, content string) (string, error) {
	return d.writeUserFile(ctx, unixUser, d.deployFiles(unixUser), name, content)
}

// writeUserFile writes a file that only the website user can read, in a directory of the
// website user. The content goes on the standard input, so that it is not in the process list.
func (d *Deps) writeUserFile(ctx context.Context, unixUser, dir, name, content string) (string, error) {
	_, err := d.asUser(ctx, unixUser, content, "sh", "-c", `umask 077 && mkdir -p "$1" && cat > "$1/$2"`, "sh", dir, name)
	return filepath.Join(dir, name), err
}

// sshOptions gives the options of a build container that connects to a Git host with the
// deploy key: the key, the list of known hosts of the website and the SSH command of Git.
func (d *Deps) sshOptions(ctx context.Context, unixUser, keyFile string, forget bool) ([]string, error) {
	hosts := d.knownHosts(unixUser)
	if forget {
		if _, err := d.asUser(ctx, unixUser, "", "rm", "-rf", "--", hosts); err != nil {
			return nil, err
		}
	}
	if _, err := d.asUser(ctx, unixUser, "", "mkdir", "-p", "-m", "0700", "--", hosts); err != nil {
		return nil, err
	}
	return []string{"--volume=" + keyFile + ":" + deployKeyPath + ":ro,Z", "--volume=" + hosts + ":" + knownHostsDir + ":Z", "--env=GIT_SSH_COMMAND=" + gitSSH}, nil
}

// hostKeyError gives a clear error when SSH refused the key of the Git host.
func hostKeyError(err error) error {
	if strings.Contains(err.Error(), "HOST IDENTIFICATION HAS CHANGED") || strings.Contains(err.Error(), "Host key verification failed") {
		return errors.New("the agent cannot get the code: the SSH key of the Git host is not the key of the first deploy. If the Git host has a new key, start the deploy again and select to forget the key of the Git host")
	}
	return nil
}

// Deploy gets the code of a repository into a new release, builds it and starts the website
// from it.
func (d *Deps) Deploy(req *agentv1.DeployRequest) (jobs.Job, error) {
	if err := checkDeploy(req); err != nil {
		return jobs.Job{}, err
	}
	w, id := req.GetWebsite(), req.GetReleaseId()
	user := w.GetUnixUser()
	app := d.Host.AppDir(user)
	release := filepath.Join(app, ReleasesDir, id)
	// healthError is the error of a failed health check after the switch, when the deploy
	// has a migration command. The website then keeps the new release (FR-1009).
	var healthError string
	var keep func(error)
	if req.GetMigrationCommand() != "" {
		keep = func(err error) { healthError = err.Error() }
	}
	inner, err := d.runtimeChange(&agentv1.WebsiteRequest{JobId: req.GetJobId(), Website: w, HealthCheck: req.GetHealthCheck()}, keep)
	if err != nil {
		return jobs.Job{}, err
	}
	var commit, message, previous string
	var removed []string
	cleanup := func(ctx context.Context) {
		if _, err := d.asUser(ctx, user, "", "rm", "-rf", "--", d.deployFiles(user)); err != nil {
			slog.Warn("remove the files of the deploy", "website", w.GetId(), "err", err)
		}
	}
	// The build commands and the migration command get the environment variables of the
	// website and the secrets of the deploy. A secret replaces a variable with the same name.
	// The agent writes the file once, and removes it after the migration step.
	var envOptions []string
	envFor := func(ctx context.Context) ([]string, error) {
		if envOptions != nil {
			return envOptions, nil
		}
		env := maps.Clone(w.GetEnvironment())
		if env == nil {
			env = map[string]string{}
		}
		maps.Copy(env, req.GetSecrets())
		envOptions = []string{}
		if len(env) > 0 {
			file, err := d.writeDeployFile(ctx, user, id+".env", envFile(env))
			if err != nil {
				envOptions = nil
				return nil, err
			}
			envOptions = []string{"--env-file=" + file}
		}
		return envOptions, nil
	}
	steps := []jobs.Step{
		{Name: "Get the code", Do: func(ctx context.Context) error {
			site, err := d.State.Get(w.GetId())
			if err != nil {
				return err
			}
			if site == nil {
				return errors.New("the agent does not know this website; create it first")
			}
			previous = releaseOf(site.Website.GetDocumentRoot())
			if previous == id {
				return errors.New("the website serves this release already")
			}
			// A second try of the job starts with an empty release.
			if _, err := d.asUser(ctx, user, "", "rm", "-rf", "--", release); err != nil {
				return err
			}
			if _, err := d.asUser(ctx, user, "", "mkdir", "-p", "-m", "0750", "--", release, filepath.Join(app, "shared")); err != nil {
				return err
			}
			if err := d.pullImage(ctx, w); err != nil {
				return err
			}
			var options []string
			if key := req.GetDeployKey(); key != "" {
				file, err := d.writeDeployFile(ctx, user, id+".key", strings.TrimRight(key, "\n")+"\n")
				if err != nil {
					return err
				}
				if options, err = d.sshOptions(ctx, user, file, req.GetForgetHostKey()); err != nil {
					return err
				}
			}
			// Git never asks for a name or a password: the job has no terminal.
			options = append(options, "--env=GIT_TERMINAL_PROMPT=0")
			out, err := d.build(ctx, w, id, options, fetchScript, req.GetBranch(), req.GetRepository())
			if err != nil {
				if keyErr := hostKeyError(err); keyErr != nil {
					return keyErr
				}
				return commandError("the agent cannot get the code", err)
			}
			for _, line := range strings.Split(out, "\n") {
				if rest, ok := strings.CutPrefix(line, "cf-commit:"); ok {
					commit, message, _ = strings.Cut(strings.TrimSpace(rest), " ")
				}
			}
			// The website can write in the release during the clone, so the two values are
			// text of the website.
			if !commitID.MatchString(commit) {
				commit = ""
			}
			if len(message) > 200 {
				message = message[:200]
			}
			message = strings.ReplaceAll(sitelogs.Clean(strings.ToValidUTF8(message, "")), "\n", " ")
			return nil
		}},
		{Name: "Build", Do: func(ctx context.Context) error {
			if len(req.GetBuildCommands()) == 0 {
				return nil
			}
			options, err := envFor(ctx)
			if err != nil {
				return err
			}
			// The output of the commands goes to the standard error, so that the end of it is
			// in the error of a command that fails.
			script := "set -e\nexec 1>&2\n" + strings.Join(req.GetBuildCommands(), "\n")
			if _, err := d.build(ctx, w, id, options, script); err != nil {
				return commandError("a build command failed", err)
			}
			return nil
		}},
		{Name: "Migrate the database", Do: func(ctx context.Context) error {
			defer cleanup(context.WithoutCancel(ctx))
			if req.GetMigrationCommand() == "" {
				return nil
			}
			options, err := envFor(ctx)
			if err != nil {
				return err
			}
			// The old container serves the website while the command runs. The command runs in
			// the release, and it can connect to the database servers of the customer.
			if _, err := d.build(ctx, w, id, options, "set -e\nexec 1>&2\n"+req.GetMigrationCommand()); err != nil {
				return commandError("the migration command failed", err)
			}
			return nil
		}},
	}
	steps = append(steps, inner.Steps...)
	steps = append(steps, jobs.Step{Name: "Remove old releases", Do: func(ctx context.Context) error {
		d.linkCurrent(ctx, user, id)
		keep := append(slices.Clone(req.GetKeepReleases()), id, previous)
		removed = d.removeReleases(ctx, user, keep)
		return nil
	}})
	return jobs.Job{ID: req.GetJobId(), Key: w.GetId(), Long: true, Steps: steps, Result: func() map[string]any {
		return map[string]any{"releaseId": id, "commit": commit, "message": message, "removed": removed, "documentRoot": w.GetDocumentRoot(),
			"migrated": req.GetMigrationCommand() != "", "healthError": healthError}
	}, OnFailure: func(ctx context.Context) {
		if inner.OnFailure != nil {
			inner.OnFailure(ctx)
		}
		cleanup(ctx)
		// The release stays when the website serves it: the job can fail after the change.
		if site, err := d.State.Get(w.GetId()); err != nil || site == nil || releaseOf(site.Website.GetDocumentRoot()) == id {
			return
		}
		if _, err := d.asUser(ctx, user, "", "rm", "-rf", "--", release); err != nil {
			slog.Warn("remove the release of a deploy that failed", "website", w.GetId(), "err", err)
		}
	}}, nil
}

// linkCurrent points the link "current" to a release. An error is only in the log: the
// website does not use the link.
func (d *Deps) linkCurrent(ctx context.Context, unixUser, releaseID string) {
	link := filepath.Join(d.Host.AppDir(unixUser), "current")
	if _, err := d.asUser(ctx, unixUser, "", "ln", "-sfnT", "--", ReleasesDir+"/"+releaseID, link); err != nil {
		slog.Warn("change the link to the current release", "user", unixUser, "err", err)
	}
}

// removeReleases removes each release directory that is not in keep, and gives their IDs. It
// removes only a directory with the name of an ID: the owner can have other files there.
func (d *Deps) removeReleases(ctx context.Context, unixUser string, keep []string) []string {
	dir := filepath.Join(d.Host.AppDir(unixUser), ReleasesDir)
	out, err := d.asUser(ctx, unixUser, "", "ls", "-1", "--", dir)
	if err != nil {
		slog.Warn("list the releases", "user", unixUser, "err", err)
		return nil
	}
	removed := []string{}
	for _, name := range strings.Fields(out) {
		if validate.ID(name) != nil || slices.Contains(keep, name) {
			continue
		}
		if _, err := d.asUser(ctx, unixUser, "", "rm", "-rf", "--", filepath.Join(dir, name)); err != nil {
			slog.Warn("remove an old release", "user", unixUser, "release", name, "err", err)
			continue
		}
		removed = append(removed, name)
	}
	return removed
}

// ActivateRelease starts the website from a release that is on the server, for example to go
// back to the release before a deploy.
func (d *Deps) ActivateRelease(req *agentv1.ActivateReleaseRequest) (jobs.Job, error) {
	w, id := req.GetWebsite(), req.GetReleaseId()
	if err := errors.Join(CheckWebsite(w), validate.ID(id)); err != nil {
		return jobs.Job{}, err
	}
	if err := releaseRoot(w, id); err != nil {
		return jobs.Job{}, err
	}
	user := w.GetUnixUser()
	inner, err := d.SetRuntime(&agentv1.WebsiteRequest{JobId: req.GetJobId(), Website: w, HealthCheck: req.GetHealthCheck()})
	if err != nil {
		return jobs.Job{}, err
	}
	steps := []jobs.Step{{Name: "Check the release", Do: func(ctx context.Context) error {
		if _, err := d.asUser(ctx, user, "", "test", "-d", filepath.Join(d.Host.AppDir(user), ReleasesDir, id)); err != nil {
			return errors.New("the server does not have this release")
		}
		return nil
	}}}
	steps = append(steps, inner.Steps...)
	last := &steps[len(steps)-1]
	stop := last.Do
	last.Do = func(ctx context.Context) error {
		err := stop(ctx)
		if site, stateErr := d.State.Get(w.GetId()); stateErr == nil && site != nil && releaseOf(site.Website.GetDocumentRoot()) == id {
			d.linkCurrent(ctx, user, id)
		}
		return err
	}
	return jobs.Job{ID: req.GetJobId(), Key: w.GetId(), Steps: steps, OnFailure: inner.OnFailure, Result: func() map[string]any {
		return map[string]any{"releaseId": id, "documentRoot": w.GetDocumentRoot()}
	}}, nil
}

// InstallDependencies runs the install command of a Node.js or Python website in a build
// container, in the application directory, and then starts the website again with no stop
// (SetRuntime). The new container uses the new dependencies. The install command writes in
// the directory that the running container uses, for example node_modules. Thus, for a short
// time, the running container can see new files before it stops.
func (d *Deps) InstallDependencies(req *agentv1.InstallDependenciesRequest) (jobs.Job, error) {
	w := req.GetWebsite()
	if err := errors.Join(CheckWebsite(w), validate.CommandLine(req.GetCommand())); err != nil {
		return jobs.Job{}, err
	}
	if !quadlet.IsApp(w.GetRuntime()) {
		return jobs.Job{}, fmt.Errorf("the runtime %q has no install command", w.GetRuntime())
	}
	user := w.GetUnixUser()
	inner, err := d.SetRuntime(&agentv1.WebsiteRequest{JobId: req.GetJobId(), Website: w, HealthCheck: req.GetHealthCheck()})
	if err != nil {
		return jobs.Job{}, err
	}
	cleanup := func(ctx context.Context) {
		if _, err := d.asUser(ctx, user, "", "rm", "-rf", "--", d.deployFiles(user)); err != nil {
			slog.Warn("remove the files of the install", "website", w.GetId(), "err", err)
		}
	}
	steps := []jobs.Step{{Name: "Install dependencies", Do: func(ctx context.Context) error {
		defer cleanup(context.WithoutCancel(ctx))
		site, err := d.State.Get(w.GetId())
		if err != nil {
			return err
		}
		if site == nil {
			return errors.New("the agent does not know this website; create it first")
		}
		if err := d.pullImage(ctx, w); err != nil {
			return err
		}
		var options []string
		if env := w.GetEnvironment(); len(env) > 0 {
			file, err := d.writeDeployFile(ctx, user, "install.env", envFile(env))
			if err != nil {
				return err
			}
			options = append(options, "--env-file="+file)
		}
		// The output goes to the standard error, so that the end of it is in the error of a
		// command that fails.
		if _, err := d.buildIn(ctx, w, documentRoot(w), options, "set -e\nexec 1>&2\n"+req.GetCommand()); err != nil {
			return commandError("the install command failed", err)
		}
		return nil
	}}}
	steps = append(steps, inner.Steps...)
	return jobs.Job{ID: req.GetJobId(), Key: w.GetId(), Long: true, Steps: steps, OnFailure: func(ctx context.Context) {
		if inner.OnFailure != nil {
			inner.OnFailure(ctx)
		}
		cleanup(ctx)
	}}, nil
}
