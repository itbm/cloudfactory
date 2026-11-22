package actions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// Branch checks for the deploys on a schedule (FR-1004).
//
// The panel asks for the last commit of the branch of each Git source. The agent runs
// "git ls-remote" in a build container of each website, with the deploy key and the list of
// known hosts of the website, as for the step "Get the code" of a deploy. The host runs no
// Git. The panel compares the commit with the commit of the last release, and starts a
// deploy when they are different.

const (
	// MaxBranchChecks is the largest number of checks in one request.
	MaxBranchChecks = 20
	// branchCheckParallel is the number of checks that run at the same time.
	branchCheckParallel = 4
	// branchCheckLimit is the time limit of one check.
	branchCheckLimit = 45 * time.Second
)

// lsRemoteScript writes the commit of a branch. "$1" is the branch and "$2" is the repository.
const lsRemoteScript = `set -e
git ls-remote --heads -- "$2" "refs/heads/$1"`

// CheckBranches gives the last commit of the branch of each check, in the order of the
// checks. A check that fails has an error and no commit. The other checks continue.
func (d *Deps) CheckBranches(ctx context.Context, req *agentv1.CheckBranchesRequest) (*agentv1.BranchHeads, error) {
	checks := req.GetChecks()
	if len(checks) > MaxBranchChecks {
		return nil, fmt.Errorf("a request can have %d branch checks", MaxBranchChecks)
	}
	heads := make([]*agentv1.BranchHead, len(checks))
	slots := make(chan struct{}, branchCheckParallel)
	var wg sync.WaitGroup
	for i, c := range checks {
		heads[i] = &agentv1.BranchHead{WebsiteId: c.GetWebsiteId()}
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			commit, err := d.checkBranch(ctx, c)
			if err != nil {
				heads[i].Error = cleanText(err.Error())
				return
			}
			heads[i].Commit = commit
		}()
	}
	wg.Wait()
	return &agentv1.BranchHeads{Heads: heads}, nil
}

// checkBranch gives the last commit of the branch of one check.
func (d *Deps) checkBranch(ctx context.Context, c *agentv1.BranchCheck) (string, error) {
	if err := errors.Join(validate.ID(c.GetWebsiteId()), validate.GitRepository(c.GetRepository()), validate.GitBranch(c.GetBranch()), checkDeployKey(c.GetDeployKey())); err != nil {
		return "", err
	}
	site, err := d.State.Get(c.GetWebsiteId())
	if err != nil {
		return "", err
	}
	if site == nil || site.Website == nil {
		return "", errors.New("the agent does not know this website")
	}
	// The build container uses the image and the limits that the website has on the server.
	w := site.Website
	user := w.GetUnixUser()
	if user != site.UnixUser {
		return "", errors.New("the Unix user of the website is not correct")
	}
	ctx, cancel := context.WithTimeout(ctx, branchCheckLimit+15*time.Second)
	defer cancel()
	options := []string{"--env=GIT_TERMINAL_PROMPT=0"}
	if key := c.GetDeployKey(); key != "" {
		// A separate directory from the files of a deploy: a deploy removes its directory
		// when it ends, also while a check runs.
		dir := filepath.Join(d.Host.Home(user), ".cf-check")
		file, err := d.writeUserFile(ctx, user, dir, "deploy.key", strings.TrimRight(key, "\n")+"\n")
		defer func() {
			if _, err := d.asUser(context.WithoutCancel(ctx), user, "", "rm", "-rf", "--", dir); err != nil {
				slog.Warn("remove the files of a branch check", "website", w.GetId(), "err", err)
			}
		}()
		if err != nil {
			return "", err
		}
		ssh, err := d.sshOptions(ctx, user, file, false)
		if err != nil {
			return "", err
		}
		options = append(options, ssh...)
	}
	out, err := d.buildFor(ctx, branchCheckLimit, w, "/tmp", options, lsRemoteScript, c.GetBranch(), c.GetRepository())
	if err != nil {
		if keyErr := hostKeyError(err); keyErr != nil {
			return "", errors.New(strings.Replace(keyErr.Error(), "start the deploy again", "start a deploy", 1))
		}
		return "", commandError("the agent cannot read the branch", err)
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == "refs/heads/"+c.GetBranch() && commitID.MatchString(fields[0]) {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("the branch %s is not in the repository", c.GetBranch())
}
