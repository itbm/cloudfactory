package actions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/itbm/cloudfactory/agent/internal/quadlet"
)

// The disk limit of a website.
//
// Each website has its own Unix user, and that user owns the files of the website. The disk
// limit of a website is as the disk limit of a database server (dbdisk.go):
//
//   - On a file system with user quotas, the limit is the user quota of the website user
//     (applyDiskQuota). The files are in a directory.
//   - On a file system with no user quotas, each new website with a disk limit has its own
//     file system: a disk image file with the size of the limit, which the server mounts as
//     a loop device. A website that fills its disk cannot fill the disk of the server.
//   - A server with no user quotas and no loop devices has no disk limits for its websites.
//     The status report tells the panel (quotaService).
//
// The files:
//
//	/srv/cloudfactory/sites/<user>       the directory of the website user: a directory, or
//	                                     the mount point of the disk image. Root owns it, and
//	                                     the SFTP service locks the user in it.
//	/srv/cloudfactory/sites/<user>/app   the files of the website. The website user owns it.
//	/srv/cloudfactory/disks/<user>.img   the disk image (ext4), only with no user quotas. The
//	                                     file is sparse: it uses only the disk space that the
//	                                     website uses. Only root can open the directory.
//
// A website keeps the type of disk that it started with. A website that has a directory keeps
// the directory, with no limit, when the server has no user quotas: the agent does not move
// the files of a website into a disk image. A website with a disk image keeps the image when
// the server gets user quotas later.
//
// A systemd mount unit mounts the disk image at each start of the server, before the website
// container starts. When the mount is not there, the directory "app" is not there, and Podman
// does not start the container: a website never starts with an empty directory in place of
// its files.
//
// The SFTP container gets each mount and unmount of the server (quadlet.SftpUnit). An open
// SFTP session has its root directory on the disk of its website, so the agent closes the
// sessions of a website before it unmounts the disk.

// siteDisksDir holds the disk images of the websites. It is next to the directory of the
// websites, on the same file system, as the home directories are. Only root can open it.
func (d *Deps) siteDisksDir() string {
	return filepath.Join(filepath.Dir(d.Host.SitesDir), "disks")
}

// siteDisk gives the disk image of a website user and its mount point.
func (d *Deps) siteDisk(user string) loopDisk {
	return loopDisk{
		Owner: "the website user " + user, Kind: "website",
		Description: "Disk of the Cloudfactory website user " + user,
		Image:       filepath.Join(d.siteDisksDir(), user+".img"), Dir: d.Host.SiteRoot(user), Group: user,
		// The worker stops the website before the file system becomes smaller
		// (shrinkSiteDisk). An open SFTP session has its root directory on the disk, and the
		// unmount fails with it, so the root helper closes the sessions of the website.
		Stop: func(ctx context.Context) (func(context.Context) error, error) {
			d.closeSftpSessions(ctx, user)
			return nil, nil
		},
	}
}

// SiteDiskLimits reports whether this server can limit the disk space of a new website: with
// a user quota, or with a disk image.
func (d *Deps) SiteDiskLimits() bool { return d.QuotaMount != "" || d.loopDevices() }

// ensureWebsiteUser makes the Unix user of a website, its disk and its directories, if the
// server does not have them. diskBytes is the disk limit of the website, and 0 means no limit.
//
// The disk is there before the directories: the root helper makes the directory "app" on the
// disk image, not below its mount point.
func (d *Deps) ensureWebsiteUser(ctx context.Context, user string, diskBytes int64) error {
	if d.Gate != nil {
		return d.Gate.Call(ctx, opUserEnsure, ensureIn{User: user, DiskBytes: diskBytes}, nil)
	}
	if err := d.Host.EnsureAccount(ctx, user); err != nil {
		return err
	}
	if err := d.ensureSiteDisk(ctx, user, diskBytes); err != nil {
		return err
	}
	return d.Host.EnsureUser(ctx, user)
}

// ensureSiteDisk makes sure that the disk image of a website is there, when the website has
// one or must get one. The Unix user of the website exists. Only the root helper runs it.
//
// A new website gets a disk image when it has a disk limit, the file system of the websites
// has no user quotas, and the server has loop devices.
func (d *Deps) ensureSiteDisk(ctx context.Context, user string, bytes int64) error {
	k := d.siteDisk(user)
	if _, err := os.Lstat(k.Image); os.IsNotExist(err) {
		if d.QuotaMount != "" || bytes <= 0 {
			return nil
		}
		_, dirErr := os.Lstat(k.Dir)
		if dirErr != nil && !os.IsNotExist(dirErr) {
			return dirErr
		}
		if !d.loopDevices() || dirErr == nil {
			slog.Warn("a website has no disk limit", "user", user, "user quotas", false, "loop devices", d.loopDevices(), "has a directory", dirErr == nil)
			return nil
		}
		// Only root can open the directory of the disk images.
		if err := os.MkdirAll(filepath.Dir(k.Image), 0o700); err != nil {
			return err
		}
		if err := d.makeDisk(ctx, k.Image, user, bytes); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	return d.mountImage(ctx, k)
}

// siteDiskState gives the disk of a website. Known is true when the website has a disk image:
// Size is then the size of the image, and Used is the disk space in use on its file system.
// For a website with no disk image, measure makes the function measure the files of the
// website: Used is then their size, and it is -1 when the function did not measure.
func (d *Deps) siteDiskState(ctx context.Context, user string, measure bool) diskOut {
	out := diskOut{Used: -1}
	if d.Gate != nil {
		if err := d.Gate.Call(ctx, opSiteDisk, siteIn{User: user, Measure: measure}, &out); err != nil {
			return diskOut{Used: -1}
		}
		return out
	}
	k := d.siteDisk(user)
	if fi, err := os.Stat(k.Image); err == nil {
		out.Known, out.Size = true, fi.Size()
		// The kernel counts the blocks of a file system: the agent does not read the files.
		out.Used, _ = imageUse(k.Dir)
	} else if measure {
		// The size of the files. It does not follow links.
		out.Used = dirSize(d.Host.AppDir(user))
	}
	return out
}

// resizeSiteDisk gives the disk image of a website the size of a new disk limit. Only the
// root helper does the work. A smaller limit fails, and changes nothing, while the website
// or an SFTP session uses the disk: the worker stops them first (changeDiskLimit).
func (d *Deps) resizeSiteDisk(ctx context.Context, user string, bytes int64) error {
	if d.Gate != nil {
		return d.Gate.Call(ctx, opSiteResize, quotaIn{User: user, Bytes: bytes}, nil)
	}
	k := d.siteDisk(user)
	if _, err := os.Lstat(k.Image); err != nil {
		return err
	}
	if err := d.mountImage(ctx, k); err != nil {
		return err
	}
	return d.resizeImage(ctx, k, bytes)
}

// changeDiskLimit applies a new disk limit to a website that the server has: the size of its
// disk image, and its user quota.
//
// A larger disk image needs no stop. For a smaller disk image, the function stops the website
// for the time of the change, the root helper closes its SFTP sessions, and the function
// starts the website again. The change
// fails when the files of the website need more space than the new limit, and the website
// then keeps its disk (errDiskKeepsSize).
func (d *Deps) changeDiskLimit(ctx context.Context, site *Site, bytes int64) error {
	user := site.UnixUser
	bytes = max(bytes, 0)
	// A disk image with no limit keeps its size: a file system always has a size.
	if st := d.siteDiskState(ctx, user, false); st.Known && bytes > 0 {
		want := imageSize(bytes)
		switch {
		case want >= st.Size:
			if err := d.resizeSiteDisk(ctx, user, bytes); err != nil {
				return err
			}
		case st.Used > want:
			// This check does not stop the website.
			return fmt.Errorf("the disk of the website user %s cannot have the new limit of %d MB: %w: the website uses %d MB", user, bytes>>20, errDiskKeepsSize, st.Used>>20)
		default:
			if err := d.shrinkSiteDisk(ctx, site, bytes); err != nil {
				return err
			}
		}
	}
	if d.QuotaMount == "" {
		return nil
	}
	return d.Host.SetDiskQuota(ctx, user, d.QuotaMount, bytes)
}

// shrinkSiteDisk makes the disk image of a website smaller. A file system cannot become
// smaller while it is in use, so the website does not operate during the change.
func (d *Deps) shrinkSiteDisk(ctx context.Context, site *Site, bytes int64) error {
	user := site.UnixUser
	service := quadlet.Website{UnixUser: user, Suffix: site.Suffix}.ServiceName()
	if !site.Stopped {
		if _, err := d.Host.Systemctl(ctx, user, "stop", service); err != nil {
			return err
		}
	}
	err := d.resizeSiteDisk(ctx, user, bytes)
	// Also after a failure, the website operates again with the disk that it has.
	if !site.Stopped {
		if _, startErr := d.Host.Systemctl(ctx, user, "start", service); startErr != nil {
			err = errors.Join(err, startErr)
		}
	}
	return err
}

// removeWebsiteUser stops the services of a website user, and removes the user, its disk
// image and its files. It is safe to run it again after a failure.
func (d *Deps) removeWebsiteUser(ctx context.Context, user string) error {
	if d.Gate != nil {
		return d.Gate.Call(ctx, opUserRemove, userIn{User: user}, nil)
	}
	// An open SFTP session has its root directory in the directory of the website. The
	// function needs the UID of the user, so it runs before the user goes.
	d.closeSftpSessions(ctx, user)
	if err := d.Host.RemoveAccount(ctx, user); err != nil {
		return err
	}
	if err := d.removeSiteDisk(ctx, user); err != nil {
		return err
	}
	return d.Host.RemoveUser(ctx, user)
}

// siteUnmountTries and siteUnmountDelay are the attempts to unmount the disk of a website
// that the agent removes, and the time between two attempts. Tests change them.
var (
	siteUnmountTries = 10
	siteUnmountDelay = time.Second
)

// removeSiteDisk unmounts and removes the disk image of a website. A website with no disk
// image has nothing to remove here. The processes of the user are gone, but the kernel can
// need a short time to release their files, so the function tries the unmount again.
func (d *Deps) removeSiteDisk(ctx context.Context, user string) error {
	k := d.siteDisk(user)
	for try := 1; ; try++ {
		err := d.unmountImage(ctx, k)
		if err == nil {
			break
		}
		if try >= siteUnmountTries {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(siteUnmountDelay):
		}
	}
	for _, file := range []string{k.Image, k.Image + ".new", growMark(k.Image)} {
		if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
