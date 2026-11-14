package actions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/itbm/cloudfactory/agent/internal/gate"
	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/panel"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
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

// diskBlock is the block size of the file system of a disk image. The size of an image is a
// multiple of it.
const diskBlock = 4096

// diskDir is the mount point of the disk of a database server.
func (d *Deps) diskDir(s dbServer) string { return filepath.Join(d.dbDir(s), "disk") }

// diskImage is the disk image file of a database server.
func (d *Deps) diskImage(s dbServer) string { return filepath.Join(d.dbDir(s), "disk.img") }

// dataDir is the data directory of a database server, on its disk. The mount point of a file
// system has the directory "lost+found", and PostgreSQL refuses a data directory with files,
// so the data directory is not the mount point.
func (d *Deps) dataDir(s dbServer) string { return filepath.Join(d.diskDir(s), "data") }

// loopControl is the device that makes loop devices. A server that has it can mount a disk image.
func (d *Deps) loopControl() string {
	if d.LoopControl != "" {
		return d.LoopControl
	}
	return "/dev/loop-control"
}

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

// loopDevices reports whether this server can mount a disk image.
func (d *Deps) loopDevices() bool {
	_, err := os.Stat(d.loopControl())
	return err == nil
}

// diskLimits reports whether this server can limit the disk space of a new database server:
// with a user quota, or with a disk image.
func (d *Deps) diskLimits() bool { return d.DBQuotaMount != "" || d.loopDevices() }

// diskService reports the disk limits of the database servers. A server can operate without them.
func (d *Deps) diskService() panel.Service {
	if d.diskLimits() {
		return panel.Service{Name: "database-disk-limits", State: "running", Optional: true}
	}
	return panel.Service{Name: "database-disk-limits", State: "stopped", Optional: true,
		Detail: "User quotas are off on the file system of the database servers, and this server has no loop devices (" + d.loopControl() + "), so it does not apply the disk limit of a database server. Refer to \"Disk quotas\" in the installation guide."}
}

// mountUnit gives the name of the systemd mount unit for a mount point. systemd makes the
// name from the path: "/" becomes "-", and each other character that is not a letter, a
// digit, ":", "_" or "." becomes "\x" and its code.
func mountUnit(mountPoint string) string {
	p := strings.Trim(path.Clean(mountPoint), "/")
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c == '/':
			b.WriteByte('-')
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == ':', c == '_', c == '.' && i > 0:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, `\x%02x`, c)
		}
	}
	return b.String() + ".mount"
}

// systemdDir holds the units of the system manager that the agent writes.
func (d *Deps) systemdDir() string { return filepath.Dir(d.addressUnit()) }

// isMountPoint reports whether dir is the top directory of a file system.
func isMountPoint(dir string) bool {
	var here, parent syscall.Stat_t
	if syscall.Stat(dir, &here) != nil || syscall.Stat(filepath.Dir(dir), &parent) != nil {
		return false
	}
	return here.Dev != parent.Dev
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

// makeDisk makes a disk image with an empty file system. The file gets its name when the
// file system is complete, so an image with that name always has a file system.
func (d *Deps) makeDisk(ctx context.Context, image, user string, bytes int64) error {
	gid, err := d.Host.GID(user)
	if err != nil {
		return err
	}
	tmp := image + ".new"
	if err := os.Remove(tmp); err != nil && !os.IsNotExist(err) {
		return err
	}
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	// The file has the size of the limit, and uses no disk space before the file system writes.
	err = errors.Join(f.Truncate(bytes/diskBlock*diskBlock), f.Close())
	if err != nil {
		return err
	}
	// "-m 0": no blocks only for root. The top directory belongs to root and to the group of
	// the user of the database server, as the directory of a disk with no image does.
	if _, err := d.Run.Run(ctx, "mkfs.ext4", "-q", "-F", "-m", "0", "-b", fmt.Sprint(diskBlock), "-E", fmt.Sprintf("root_owner=0:%d", gid), tmp); err != nil {
		return err
	}
	return os.Rename(tmp, image)
}

// mountDisk mounts the disk image of a database server, now and at each start of the server.
func (d *Deps) mountDisk(ctx context.Context, s dbServer) error {
	dir := d.diskDir(s)
	// Only root can open the mount point while the disk is not there.
	if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
		return err
	}
	unit := mountUnit(dir)
	content := quadlet.Header + strings.Join([]string{
		"[Unit]",
		"Description=Disk of the Cloudfactory database server " + s.ID,
		// The file system that has the disk image is there before this mount.
		"RequiresMountsFor=" + d.dbDir(s),
		"",
		"[Mount]",
		"What=" + d.diskImage(s),
		"Where=" + dir,
		"Type=ext4",
		// "discard" gives the blocks of removed data back to the disk of the server.
		"Options=loop,noatime,nodev,nosuid,discard",
		"",
		"[Install]",
		// The mount is ready before the user manager of the database server starts the container.
		"WantedBy=local-fs.target",
		"",
	}, "\n")
	changed, err := host.WriteFile(filepath.Join(d.systemdDir(), unit), content, 0o644)
	if err != nil {
		return err
	}
	if changed {
		if _, err := d.Run.Run(ctx, "systemctl", "daemon-reload"); err != nil {
			return err
		}
	}
	if changed || !isMountPoint(dir) {
		if _, err := d.Run.Run(ctx, "systemctl", "enable", "--now", unit); err != nil {
			return fmt.Errorf("mount the disk of %s: %w", s, err)
		}
		// The top directory of the new file system. Only root and the user of the database
		// server can open it.
		if _, err := d.Run.Run(ctx, "chmod", "0750", dir); err != nil {
			return err
		}
	}
	return nil
}

// errDiskKeepsSize is the cause of the error when the disk of a database server cannot get a
// smaller limit. The disk and its data are then as before.
var errDiskKeepsSize = errors.New("the disk keeps its size")

// The worker finds this cause with errors.Is, also when the root helper gives the error.
func init() { gate.RegisterError("disk-keeps-size", errDiskKeepsSize) }

// growMark is the name of a file next to a disk image. It is there while the file system
// does not have the full size of the image, so that a job that runs again completes the step.
func growMark(image string) string { return image + ".grow" }

// resizeDisk gives the disk of a database server the size of a new limit.
func (d *Deps) resizeDisk(ctx context.Context, s dbServer, bytes int64) error {
	dir, image := d.diskDir(s), d.diskImage(s)
	fi, err := os.Stat(image)
	if err != nil {
		return err
	}
	want := bytes / diskBlock * diskBlock
	_, markErr := os.Lstat(growMark(image))
	switch {
	case want > fi.Size() || want == fi.Size() && markErr == nil:
		if err := os.WriteFile(growMark(image), nil, 0o600); err != nil {
			return err
		}
		if err := os.Truncate(image, want); err != nil {
			return err
		}
		// The loop device reads the new size of the file, and the file system uses it. The
		// database server continues to operate.
		device, err := d.Run.Run(ctx, "findmnt", "--noheadings", "--output", "SOURCE", "--mountpoint", dir)
		if err != nil {
			return err
		}
		device = strings.TrimSpace(device)
		if !strings.HasPrefix(device, "/dev/") {
			return fmt.Errorf("the disk of %s is not on a loop device (%q)", s, device)
		}
		if _, err := d.Run.Run(ctx, "losetup", "--set-capacity", device); err != nil {
			return err
		}
		if _, err := d.Run.Run(ctx, "resize2fs", device); err != nil {
			return err
		}
		return os.Remove(growMark(image))
	case want < fi.Size():
		refuse := func(cause error) error {
			return fmt.Errorf("the disk of %s cannot have the new limit of %d MB: %w: %w", s, bytes>>20, errDiskKeepsSize, cause)
		}
		// The data must go into the new size. This check does not stop the database server.
		if used, _ := d.diskUse(s); used > want {
			return refuse(fmt.Errorf("the database server uses %d MB", used>>20))
		}
		// A file system cannot become smaller while it is in use.
		service, stopped := s.container()+".service", false
		if d.serverExists(s) {
			if _, err := d.Host.Systemctl(ctx, dbUser(s), "stop", service); err != nil {
				return err
			}
			stopped = true
		}
		unit := mountUnit(dir)
		if _, err := d.Run.Run(ctx, "systemctl", "stop", unit); err != nil {
			return err
		}
		// resize2fs needs a check of the file system first. It fails, and changes nothing,
		// when the data and the tables of the file system need more space than the new size.
		_, err := d.Run.Run(ctx, "e2fsck", "-f", "-p", image)
		if err == nil {
			_, err = d.Run.Run(ctx, "resize2fs", image, fmt.Sprintf("%dK", want/1024))
		}
		if err == nil {
			err = os.Truncate(image, want)
		} else {
			err = refuse(err)
		}
		if _, mountErr := d.Run.Run(ctx, "systemctl", "start", unit); mountErr != nil {
			return errors.Join(err, mountErr)
		}
		// After a failure, the database server operates again with the disk that it had.
		if err != nil && stopped {
			if _, startErr := d.Host.Systemctl(ctx, dbUser(s), "start", service); startErr != nil {
				err = errors.Join(err, startErr)
			}
		}
		return err
	}
	return nil
}

// removeDisk unmounts and removes the disk image of a database server. A database server
// with no disk image has nothing to remove here.
func (d *Deps) removeDisk(ctx context.Context, s dbServer) error {
	dir := d.diskDir(s)
	unit := mountUnit(dir)
	file := filepath.Join(d.systemdDir(), unit)
	if _, err := os.Stat(file); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if _, err := d.Run.Run(ctx, "systemctl", "disable", "--now", unit); err != nil {
		return fmt.Errorf("unmount the disk of %s: %w", s, err)
	}
	if err := os.Remove(file); err != nil {
		return err
	}
	_, err := d.Run.Run(ctx, "systemctl", "daemon-reload")
	return err
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
func (d *Deps) diskUse(s dbServer) (used, size int64) {
	dir := d.diskDir(s)
	if !isMountPoint(dir) {
		return dirSize(dir), 0
	}
	var fs syscall.Statfs_t
	if err := syscall.Statfs(dir, &fs); err != nil {
		return 0, 0
	}
	block := int64(fs.Bsize)
	return (int64(fs.Blocks) - int64(fs.Bfree)) * block, int64(fs.Blocks) * block
}
