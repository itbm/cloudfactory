package actions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/itbm/cloudfactory/agent/internal/gate"
	"github.com/itbm/cloudfactory/agent/internal/host"
	"github.com/itbm/cloudfactory/agent/internal/quadlet"
)

// A disk image is a file with an ext4 file system, which the server mounts as a loop device.
// On a file system with no user quotas, a database server (dbdisk.go) and a website
// (sitedisk.go) with a disk limit have one: the size of the image is the limit. Only the
// root helper makes, mounts and changes a disk image.

// diskBlock is the block size of the file system of a disk image. The size of an image is a
// multiple of it.
const diskBlock = 4096

// loopDisk is a disk image and its mount point.
type loopDisk struct {
	// Owner names the owner of the disk in an error, for example "the website acme_x7k2".
	Owner string
	// Kind is the type of the owner in an error: "database server" or "website".
	Kind string
	// Description is the description of the mount unit.
	Description string
	// Image is the disk image file. The mount unit waits for the file system of its directory.
	Image string
	// Dir is the mount point. Root owns it.
	Dir string
	// Group is the Unix user whose group owns the top directory of the file system.
	Group string
	// Stop stops the processes that use the disk, before the file system becomes smaller. It
	// gives a function that starts a service again after a failure, or nil when it stopped
	// no service. A nil Stop means that the caller stopped all that uses the disk.
	Stop func(ctx context.Context) (start func(ctx context.Context) error, err error)
}

// loopControl is the device that makes loop devices. A server that has it can mount a disk image.
func (d *Deps) loopControl() string {
	if d.LoopControl != "" {
		return d.LoopControl
	}
	return "/dev/loop-control"
}

// loopDevices reports whether this server can mount a disk image.
func (d *Deps) loopDevices() bool {
	_, err := os.Stat(d.loopControl())
	return err == nil
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
	// the user, as the directory of a disk with no image does.
	if _, err := d.Run.Run(ctx, "mkfs.ext4", "-q", "-F", "-m", "0", "-b", fmt.Sprint(diskBlock), "-E", fmt.Sprintf("root_owner=0:%d", gid), tmp); err != nil {
		return err
	}
	return os.Rename(tmp, image)
}

// mountImage mounts a disk image, now and at each start of the server.
func (d *Deps) mountImage(ctx context.Context, k loopDisk) error {
	// Only root can open the mount point while the disk is not there.
	if err := os.Mkdir(k.Dir, 0o700); err != nil && !os.IsExist(err) {
		return err
	}
	unit := mountUnit(k.Dir)
	content := quadlet.Header + strings.Join([]string{
		"[Unit]",
		"Description=" + k.Description,
		// The file system that has the disk image is there before this mount.
		"RequiresMountsFor=" + filepath.Dir(k.Image),
		"",
		"[Mount]",
		"What=" + k.Image,
		"Where=" + k.Dir,
		"Type=ext4",
		// "discard" gives the blocks of removed data back to the disk of the server.
		"Options=loop,noatime,nodev,nosuid,discard",
		"",
		"[Install]",
		// The mount is ready before the user manager of the owner starts the container.
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
	if changed || !isMountPoint(k.Dir) {
		if _, err := d.Run.Run(ctx, "systemctl", "enable", "--now", unit); err != nil {
			return fmt.Errorf("mount the disk of %s: %w", k.Owner, err)
		}
		// The top directory of the new file system. Only root and the user can open it.
		if _, err := d.Run.Run(ctx, "chmod", "0750", k.Dir); err != nil {
			return err
		}
	}
	return nil
}

// errDiskKeepsSize is the cause of the error when a disk cannot get a smaller limit. The disk
// and its data are then as before.
var errDiskKeepsSize = errors.New("the disk keeps its size")

// The worker finds this cause with errors.Is, also when the root helper gives the error.
func init() { gate.RegisterError("disk-keeps-size", errDiskKeepsSize) }

// growMark is the name of a file next to a disk image. It is there while the file system
// does not have the full size of the image, so that a job that runs again completes the step.
func growMark(image string) string { return image + ".grow" }

// imageSize gives the size that a disk image has for a disk limit.
func imageSize(bytes int64) int64 { return bytes / diskBlock * diskBlock }

// resizeImage gives a disk image the size of a new limit.
//
//   - A larger limit makes the file system larger while the owner operates.
//   - A smaller limit unmounts the file system, and makes it smaller. It fails when the data
//     needs more space than the new limit, and changes nothing then (errDiskKeepsSize). It
//     also fails, and changes nothing, when a process uses the file system.
func (d *Deps) resizeImage(ctx context.Context, k loopDisk, bytes int64) error {
	fi, err := os.Stat(k.Image)
	if err != nil {
		return err
	}
	want := imageSize(bytes)
	_, markErr := os.Lstat(growMark(k.Image))
	switch {
	case want > fi.Size() || want == fi.Size() && markErr == nil:
		if err := os.WriteFile(growMark(k.Image), nil, 0o600); err != nil {
			return err
		}
		if err := os.Truncate(k.Image, want); err != nil {
			return err
		}
		// The loop device reads the new size of the file, and the file system uses it. The
		// owner continues to operate.
		device, err := d.Run.Run(ctx, "findmnt", "--noheadings", "--output", "SOURCE", "--mountpoint", k.Dir)
		if err != nil {
			return err
		}
		device = strings.TrimSpace(device)
		if !strings.HasPrefix(device, "/dev/") {
			return fmt.Errorf("the disk of %s is not on a loop device (%q)", k.Owner, device)
		}
		if _, err := d.Run.Run(ctx, "losetup", "--set-capacity", device); err != nil {
			return err
		}
		if _, err := d.Run.Run(ctx, "resize2fs", device); err != nil {
			return err
		}
		return os.Remove(growMark(k.Image))
	case want < fi.Size():
		refuse := func(cause error) error {
			return fmt.Errorf("the disk of %s cannot have the new limit of %d MB: %w: %w", k.Owner, bytes>>20, errDiskKeepsSize, cause)
		}
		// The data must go into the new size. This check stops nothing.
		if used, _ := imageUse(k.Dir); used > want {
			return refuse(fmt.Errorf("the %s uses %d MB", k.Kind, used>>20))
		}
		// A file system cannot become smaller while it is in use.
		var start func(context.Context) error
		if k.Stop != nil {
			if start, err = k.Stop(ctx); err != nil {
				return err
			}
		}
		// After a failure, the owner operates again with the disk that it had.
		again := func(err error) error {
			if err != nil && start != nil {
				err = errors.Join(err, start(ctx))
			}
			return err
		}
		unit := mountUnit(k.Dir)
		if _, err := d.Run.Run(ctx, "systemctl", "stop", unit); err != nil {
			return again(fmt.Errorf("unmount the disk of %s: %w", k.Owner, err))
		}
		// The mount can be in the mount namespace of a different process, which systemd does
		// not see. The kernel then uses the file system: a change of the image would break it.
		err = d.imageFree(ctx, k)
		if err == nil {
			// resize2fs needs a check of the file system first. It fails, and changes nothing,
			// when the data and the tables of the file system need more space than the new size.
			if _, err = d.Run.Run(ctx, "e2fsck", "-f", "-p", k.Image); err == nil {
				_, err = d.Run.Run(ctx, "resize2fs", k.Image, fmt.Sprintf("%dK", want/1024))
			}
			if err == nil {
				err = os.Truncate(k.Image, want)
			} else {
				err = refuse(err)
			}
		}
		if _, mountErr := d.Run.Run(ctx, "systemctl", "start", unit); mountErr != nil {
			return errors.Join(err, mountErr)
		}
		return again(err)
	}
	return nil
}

// loopFreeTries and loopFreeDelay are the checks that a disk image has no loop device after
// its unmount, and the time between two checks. Tests change them.
var (
	loopFreeTries = 10
	loopFreeDelay = 200 * time.Millisecond
)

// imageFree checks that no loop device uses a disk image. The kernel releases the loop device
// a short time after the unmount.
func (d *Deps) imageFree(ctx context.Context, k loopDisk) error {
	for try := 1; ; try++ {
		out, err := d.Run.Run(ctx, "losetup", "--associated", k.Image)
		if err != nil {
			return err
		}
		if out = strings.TrimSpace(out); out == "" {
			return nil
		}
		if try >= loopFreeTries {
			return fmt.Errorf("a process uses the disk of %s (%s)", k.Owner, out)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(loopFreeDelay):
		}
	}
}

// unmountImage unmounts a disk image and removes its mount unit. A disk with no mount unit
// has nothing to remove here. The image file stays.
func (d *Deps) unmountImage(ctx context.Context, k loopDisk) error {
	unit := mountUnit(k.Dir)
	file := filepath.Join(d.systemdDir(), unit)
	if _, err := os.Stat(file); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if _, err := d.Run.Run(ctx, "systemctl", "disable", "--now", unit); err != nil {
		return fmt.Errorf("unmount the disk of %s: %w", k.Owner, err)
	}
	if err := os.Remove(file); err != nil {
		return err
	}
	_, err := d.Run.Run(ctx, "systemctl", "daemon-reload")
	return err
}

// imageUse gives the disk space that is in use on the file system at dir, and the size of
// the file system, in bytes. For a directory that is not a mount point, it measures the
// directory, and the size is 0.
func imageUse(dir string) (used, size int64) {
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
