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
// All database containers have the Unix user cf-db, so a user quota cannot limit one database
// server. Thus each database server with a disk limit has its own file system: a disk image
// file with the size of the limit, which the server mounts as a loop device. The data
// directory of the database server is in that file system. A database server that fills its
// disk cannot fill the disk of the server.
//
//	/var/lib/cf-db/servers/<id>/disk.img    the disk image (ext4). The file is sparse: it uses
//	                                        only the disk space that the database server uses.
//	/var/lib/cf-db/servers/<id>/disk        the mount point of the disk image
//	/var/lib/cf-db/servers/<id>/disk/data   the data directory of the database server
//
// A systemd mount unit mounts the disk image at each start of the server, before the database
// container starts. When the mount is not there, the data directory is not there, and Podman
// does not start the container: a database server never starts with an empty data directory
// in place of its data.
//
// A server with no loop devices (for example a container of an LXC host) has no disk limits
// for its database servers. "disk" is then a directory, and the status report tells the panel.

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

// diskLimits reports whether this server can limit the disk space of a database server.
func (d *Deps) diskLimits() bool {
	_, err := os.Stat(d.loopControl())
	return err == nil
}

// diskService reports the disk limits of the database servers. A server can operate without them.
func (d *Deps) diskService() panel.Service {
	if d.diskLimits() {
		return panel.Service{Name: "database-disk-limits", State: "running", Optional: true}
	}
	return panel.Service{Name: "database-disk-limits", State: "stopped", Optional: true,
		Detail: "This server has no loop devices (" + d.loopControl() + "), so it does not apply the disk limit of a database server."}
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

// ensureDisk makes sure that a database server has its disk, and that the disk has the size
// of the limit. bytes is the disk limit, and 0 means no limit. The caller holds the lock of
// the database server.
//
//   - A new database server with a limit gets a disk image.
//   - A larger limit makes the file system larger while the database server operates.
//   - A smaller limit stops the database server, and makes the file system smaller. It fails
//     when the data needs more space than the new limit, and changes nothing then
//     (errDiskKeepsSize).
//   - A database server that started with no disk image keeps its directory, with no limit.
func (d *Deps) ensureDisk(ctx context.Context, s dbServer, bytes int64) error {
	dir, image := d.diskDir(s), d.diskImage(s)
	if _, err := os.Lstat(image); os.IsNotExist(err) {
		_, dirErr := os.Lstat(dir)
		if bytes <= 0 || !d.diskLimits() || dirErr == nil {
			if bytes > 0 {
				slog.Warn("a database server has no disk limit", "server", s.ID, "loop devices", d.diskLimits(), "data from before the limit", dirErr == nil)
			}
			return d.makeDataDir(ctx, dir, DBUser)
		}
		if err := d.makeDisk(ctx, image, bytes); err != nil {
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

// makeDisk makes a disk image with an empty file system. The file gets its name when the
// file system is complete, so an image with that name always has a file system.
func (d *Deps) makeDisk(ctx context.Context, image string, bytes int64) error {
	uid, err := d.Host.LookupUID(DBUser)
	if err != nil {
		return err
	}
	gid, err := d.Host.GID(DBUser)
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
	// "-m 0": no blocks only for root. The top directory belongs to cf-db, as a data directory
	// with no disk image does.
	if _, err := d.Run.Run(ctx, "mkfs.ext4", "-q", "-F", "-m", "0", "-b", fmt.Sprint(diskBlock), "-E", fmt.Sprintf("root_owner=%d:%d", uid, gid), tmp); err != nil {
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
		// The mount is ready before the user manager of cf-db starts the database container.
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
		// The top directory of the new file system. Only cf-db can open the data.
		if _, err := d.Run.Run(ctx, "chmod", "0700", dir); err != nil {
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
			if _, err := d.Host.Systemctl(ctx, DBUser, "stop", service); err != nil {
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
			if _, startErr := d.Host.Systemctl(ctx, DBUser, "start", service); startErr != nil {
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
// a database server with no disk image, measure makes the function measure its directory.
// Only cf-db and root can open the directory.
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
		out.Mounted = true
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
