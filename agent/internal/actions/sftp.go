package actions

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	agentv1 "github.com/itbm/cloudfactory/agent/gen/cloudfactory/agent/v1"
	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/jobs"
	"github.com/itbm/cloudfactory/agent/internal/validate"
)

// SFTP access (spec section 10, release 0.1). The SSH service of the host locks each website
// user in its directory in SitesDir, and allows only SFTP: no shell, no forwarding. The
// installer writes this configuration for the group host.SitesGroup. SFTP accepts SSH keys
// only, so the agent sets the keys of each user and locks the password.

// sftpKeysDir holds the authorized keys of each website user. Root owns the files, so that
// the website cannot change them.
func (d *Deps) sftpKeysDir() string {
	if d.SftpKeysDir != "" {
		return d.SftpKeysDir
	}
	return "/etc/cloudfactory/sftp"
}

// SetSftp sets the SFTP access of a website: on or off, and the SSH keys.
func (d *Deps) SetSftp(req *agentv1.SetSftpRequest) (jobs.Job, error) {
	user := req.GetUnixUser()
	errs := []error{validate.ID(req.GetWebsiteId()), validate.UnixUser(user)}
	for _, k := range req.GetAuthorizedKeys() {
		errs = append(errs, validate.SSHKey(k))
	}
	if err := errors.Join(errs...); err != nil {
		return jobs.Job{}, err
	}
	keysFile := filepath.Join(d.sftpKeysDir(), user+".keys")
	return jobs.Job{ID: req.GetJobId(), Key: req.GetWebsiteId(), Steps: []jobs.Step{
		{Name: "Set SFTP access", Do: func(ctx context.Context) error {
			if _, err := d.Run.Run(ctx, "usermod", "--append", "--groups", host.SitesGroup, user); err != nil {
				return err
			}
			// "restrict" turns off forwarding and terminals for each key.
			var keys strings.Builder
			keys.WriteString("# Managed by the Cloudfactory agent. Change the keys in the panel.\n")
			if req.GetEnabled() {
				for _, k := range req.GetAuthorizedKeys() {
					keys.WriteString("restrict " + k + "\n")
				}
			}
			// sshd reads the file as the user, so the user can read it but not change it.
			if _, err := host.WriteFile(keysFile, keys.String(), 0o644); err != nil {
				return err
			}
			// "!" locks the password, also a password from an earlier version. sshd also
			// refuses passwords for this group (install.sh), so only the keys can sign in.
			_, err := d.Run.Run(ctx, "usermod", "--password", "!", user)
			return err
		}},
	}}, nil
}
