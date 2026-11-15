package actions

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/itbm/cloudfactory/agent/internal/panel"
)

// The disk limit of a database server.
//
// Each database server has its own Unix user (dbUser), and that user owns all files of the
// database server. Thus the disk limit of a database server is as the disk limit of a
// website:
//
//   - On a file system with user quotas, the limit is the user quota of the user of the
//     database server. The data are in a directory. A database server that is at its limit
//     cannot write, and the other database servers and the websites continue to operate.
//   - On a file system with no user quotas, each database server with a disk limit has its
//     own file system: a disk image file with the size of the limit, which the server mounts
//     as a loop device. A database server that fills its disk cannot fill the disk of the
//     server.
//   - A server with no user quotas and no loop devices (for example a container of an LXC
//     host with a file system that is not ext4) has no disk limits for its database servers.
//     The status report tells the panel.
//
// The files:
//
//	/var/lib/cf-db/servers/<id>/disk        the disk: a directory, or the mount point of the
//	                                        disk image. Root owns it.
//	/var/lib/cf-db/servers/<id>/disk/data   the data directory. The user of the database
//	                                        server owns it.
//	/var/lib/cf-db/servers/<id>/disk.img    the disk image (ext4), only with no user quotas.
//	                                        The file is sparse: it uses only the disk space
//	                                        that the database server uses.
//
// A database server keeps the type of disk that it started with. A database server with a
// disk image keeps the image when the server gets user quotas later.
//
// Root owns the directory of the disk, with the two types of disk. Thus the root helper makes
// the data directory in a directory that the user of the database server cannot change, and a
// person who controls a database server cannot put a symbolic link in its place.
//
// A systemd mount unit mounts a disk image at each start of the server, before the database
// container starts. When the mount is not there, the data directory is not there, and Podman
// does not start the container: a database server never starts with an empty data directory
// in place of its data.

// diskDir is the mount point of the disk of a database server.
func (d *Deps) diskDir(s dbServer) string { return filepath.Join(d.dbDir(s), "disk") }

// diskImage is the disk image file of a database server.
func (d *Deps) diskImage(s dbServer) string { return filepath.Join(d.dbDir(s), "disk.img") }

// dataDir is the data directory of a database server, on its disk. The mount point of a file
// system has the directory "lost+found", and PostgreSQL refuses a data directory with files,
// so the data directory is not the mount point.
func (d *Deps) dataDir(s dbServer) string { return filepath.Join(d.diskDir(s), "data") }

// FindDatabaseQuotas finds the user quotas of the file system of the database servers, and
// keeps the result (DBQuotaMount). The root helper calls it at its start. A server with no
// directory of the database servers yet has it later on the file system of the first
// directory above it that exists.
func (d *Deps) FindDatabaseQuotas(ctx context.Context) {
	dir := d.dbHome()
	for dir != "/" && dir != "." {
		if _, err := os.Stat(dir); err == nil {
			break
		}
		dir = filepath.Dir(dir)
	}
	d.DBQuotaMount = d.Host.QuotaMount(ctx, dir)
}

// diskLimits reports whether this server can limit the disk space of a new database server:
// with a user quota, or with a disk image.
func (d *Deps) diskLimits() bool { return d.DBQuotaMount != "" || d.loopDevices() }

// diskService reports the disk limits of the database servers. A server can operate without
// them. The caller holds the lock of the observations.
//
// As for the websites (quotaService), the part is stopped when the server does not apply the
// disk limit of one of its database servers. The status report counts these database servers
// before it calls this function (databaseServerUse).
func (d *Deps) diskService() panel.Service {
	const guide = " Refer to \"Disk quotas\" in the installation guide."
	if !d.diskLimits() {
		return panel.Service{Name: "database-disk-limits", State: "stopped", Optional: true,
			Detail: "User quotas are off on the file system of the database servers, and this server has no loop devices (" + d.loopControl() + "), so it does not apply the disk limit of a database server." + guide}
	}
	// With no user quotas, a new database server gets a disk image. A database server that
	// has a directory keeps it, and has no limit.
	if o := &d.obs; d.DBQuotaMount == "" && o.dbNoLimit > 0 {
		return panel.Service{Name: "database-disk-limits", State: "stopped", Optional: true,
			Detail: fmt.Sprintf("User quotas are off on the file system of the database servers. Database servers with a disk limit and no disk image: %d of %d. This server does not apply the disk limit of these database servers. Only a new database server gets a disk image.", o.dbNoLimit, o.dbLimited) + guide}
	}
	return panel.Service{Name: "database-disk-limits", State: "running", Optional: true}
}

// ensureDisk makes sure that a database server has its disk, and that the disk has the
// limit. bytes is the disk limit, and 0 means no limit. The caller holds the lock of the
// database server, and the Unix user of the database server exists.
//
// A database server with a disk image:
//
//   - A larger limit makes the file system larger while the database server operates.
//   - A smaller limit stops the database server, and makes the file system smaller. It fails
//     when the data needs more space than the new limit, and changes nothing then
//     (errDiskKeepsSize).
//
// A database server with a directory:
//
//   - On a file system with user quotas, the limit is the user quota of the user of the
//     database server (setDiskQuota).
//   - With no user quotas, a new database server with a limit gets a disk image, when the
//     server has loop devices. A database server that started with no disk image keeps its
//     directory, with no limit.
func (d *Deps) ensureDisk(ctx context.Context, s dbServer, bytes int64) error {
	dir, image, user := d.diskDir(s), d.diskImage(s), dbUser(s)
	if _, err := os.Lstat(image); os.IsNotExist(err) {
		_, dirErr := os.Lstat(dir)
		if d.DBQuotaMount != "" {
			if err := d.makeDiskDir(ctx, dir, user); err != nil {
				return err
			}
			return d.setDiskQuota(ctx, s, bytes)
		}
		if bytes <= 0 || !d.loopDevices() || dirErr == nil {
			if bytes > 0 {
				slog.Warn("a database server has no disk limit", "server", s.ID, "user quotas", false, "loop devices", d.loopDevices(), "has a directory", dirErr == nil)
			}
			return d.makeDiskDir(ctx, dir, user)
		}
		if err := d.makeDisk(ctx, image, user, bytes); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if err := d.mountDisk(ctx, s); err != nil {
		return err
	}
	if bytes <= 0 {
		return nil
	}
	return d.resizeDisk(ctx, s, bytes)
}

// makeDiskDir makes the directory of the disk of a database server that has no disk image.
// Root owns the directory, and only root and the user of the database server can open it
// (mode 0750). It does not change a directory that exists.
func (d *Deps) makeDiskDir(ctx context.Context, dir, user string) error {
	if _, err := os.Lstat(dir); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	_, err := d.Run.Run(ctx, "install", "-d", "-o", "root", "-g", user, "-m", "0750", dir)
	return err
}

// setDiskQuota sets the user quota of the user of a database server. 0 bytes removes the
// limit. The function always sets the quota: a new user can get the UID of a user that the
// agent removed, and the file system can still have the limit of that user.
//
// A smaller limit fails when the database server uses more disk space than the new limit,
// and changes nothing then (errDiskKeepsSize): a database server that is above its limit
// cannot write, and stops. A database server that gets its first limit always gets it.
func (d *Deps) setDiskQuota(ctx context.Context, s dbServer, bytes int64) error {
	user := dbUser(s)
	if bytes > 0 {
		if used, limit, ok := d.quotaUse(ctx, s); ok && limit > 0 && bytes < limit && used > bytes {
			return fmt.Errorf("the disk of %s cannot have the new limit of %d MB: %w: the database server uses %d MB", s, bytes>>20, errDiskKeepsSize, used>>20)
		}
	}
	return d.Host.SetDatabaseQuota(ctx, user, d.DBQuotaMount, bytes)
}

// quotaUse gives the disk space that the user of a database server uses, and the limit of
// its user quota, in bytes. The limit is 0 with no limit. ok is false when the server has no
// user quotas for the database servers, or cannot read them.
func (d *Deps) quotaUse(ctx context.Context, s dbServer) (used, limit int64, ok bool) {
	if d.DBQuotaMount == "" {
		return 0, 0, false
	}
	uid, err := d.Host.LookupUID(dbUser(s))
	if err != nil {
		return 0, 0, false
	}
	used, limit, err = d.Host.QuotaUse(ctx, d.DBQuotaMount, uid)
	if err != nil {
		slog.Warn("read the user quota of a database server", "server", s.ID, "err", err)
		return 0, 0, false
	}
	return used, limit, true
}

// loopDisk gives the disk image of a database server and its mount point.
func (d *Deps) loopDisk(s dbServer) loopDisk {
	return loopDisk{
		Owner: s.String(), Kind: "database server",
		Description: "Disk of the Cloudfactory database server " + s.ID,
		Image:       d.diskImage(s), Dir: d.diskDir(s), Group: dbUser(s),
		// A file system cannot become smaller while the database server uses it.
		Stop: func(ctx context.Context) (func(context.Context) error, error) {
			if !d.serverExists(s) {
				return nil, nil
			}
			service := s.container() + ".service"
			if _, err := d.Host.Systemctl(ctx, dbUser(s), "stop", service); err != nil {
				return nil, err
			}
			return func(ctx context.Context) error {
				_, err := d.Host.Systemctl(ctx, dbUser(s), "start", service)
				return err
			}, nil
		},
	}
}

// mountDisk mounts the disk image of a database server, now and at each start of the server.
func (d *Deps) mountDisk(ctx context.Context, s dbServer) error {
	return d.mountImage(ctx, d.loopDisk(s))
}

// resizeDisk gives the disk of a database server the size of a new limit. A smaller limit
// stops the database server.
func (d *Deps) resizeDisk(ctx context.Context, s dbServer, bytes int64) error {
	return d.resizeImage(ctx, d.loopDisk(s), bytes)
}

// removeDisk unmounts the disk image of a database server. A database server with no disk
// image has nothing to remove here.
func (d *Deps) removeDisk(ctx context.Context, s dbServer) error {
	return d.unmountImage(ctx, d.loopDisk(s))
}

// diskState gives the disk space that a database server uses, and the size of its disk. For
// a database server with no user quota and no disk image, measure makes the function measure
// its directory. Only the user of the database server and root can open the directory.
func (d *Deps) diskState(ctx context.Context, s dbServer, measure bool) diskOut {
	out := diskOut{Used: -1}
	if d.Gate != nil {
		if err := d.Gate.Call(ctx, opDBDisk, serverIn{Server: s, Measure: measure}, &out); err != nil {
			return diskOut{Used: -1}
		}
		return out
	}
	dir := d.diskDir(s)
	if isMountPoint(dir) {
		out.Used, out.Size = d.diskUse(s)
		out.Known = true
	} else if used, limit, ok := d.quotaUse(ctx, s); ok {
		// The kernel counts the files of the user: the agent does not read the directory.
		out.Used, out.Size, out.Known = used, limit, true
	} else if measure {
		out.Used = dirSize(dir)
	}
	return out
}

// diskUse gives the disk space that a database server uses and the size of its disk, in
// bytes. The size is 0 for a database server with no disk image.
func (d *Deps) diskUse(s dbServer) (used, size int64) { return imageUse(d.diskDir(s)) }
