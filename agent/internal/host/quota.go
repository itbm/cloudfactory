package host

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// SetDatabaseQuota limits the disk space of the user of a database server on the file system
// at mount. The limits are the same as the limits of a website user (SetDiskQuota). 0 bytes
// removes the limit. Only root calls this function (the root helper of the agent).
func (h *Host) SetDatabaseQuota(ctx context.Context, unixUser, mount string, bytes int64) error {
	if err := CheckDatabaseUser(unixUser); err != nil {
		return err
	}
	return h.setQuota(ctx, unixUser, mount, bytes)
}

// setQuota sets the user quota of a user. The hard limit is bytes. The soft limit is 95% of it.
func (h *Host) setQuota(ctx context.Context, unixUser, mount string, bytes int64) error {
	hard := bytes / 1024
	soft := hard * 95 / 100
	_, err := h.Run.Run(ctx, "setquota", "--user", unixUser, strconv.FormatInt(soft, 10), strconv.FormatInt(hard, 10), "0", "0", mount)
	return err
}

// QuotaUse gives the disk space that a user uses on the file system at mount, and the hard
// limit of its user quota, in bytes. The limit is 0 for a user with no limit. The kernel
// gives the two values, so the function does not read the files of the user. Only root can
// read the quota of a different user.
func (h *Host) QuotaUse(ctx context.Context, mount string, uid int) (used, limit int64, err error) {
	if h.ReadQuota != nil {
		return h.ReadQuota(mount, uid)
	}
	out, err := h.Run.Run(ctx, "findmnt", "--noheadings", "--output", "SOURCE", "--mountpoint", mount)
	if err != nil {
		return 0, 0, err
	}
	device := strings.TrimSpace(out)
	if !strings.HasPrefix(device, "/dev/") {
		return 0, 0, fmt.Errorf("host: the file system at %s is not on a block device (%s)", mount, device)
	}
	return readQuota(device, uid)
}

// dqblk is "struct if_dqblk" of the kernel (linux/quota.h). The block limits are in units
// of 1024 bytes. curSpace is in bytes.
type dqblk struct {
	bHardLimit, bSoftLimit, curSpace  uint64
	iHardLimit, iSoftLimit, curInodes uint64
	bTime, iTime                      uint64
	valid                             uint32
	_                                 uint32
}

// getUserQuota is the command of quotactl(2) that reads the quota of a user:
// QCMD(Q_GETQUOTA, USRQUOTA).
const getUserQuota = 0x800007 << 8

// readQuota reads the user quota of a UID on a block device with quotactl(2).
func readQuota(device string, uid int) (used, limit int64, err error) {
	special, err := unix.BytePtrFromString(device)
	if err != nil {
		return 0, 0, err
	}
	var q dqblk
	if _, _, errno := unix.Syscall6(unix.SYS_QUOTACTL, getUserQuota, uintptr(unsafe.Pointer(special)), uintptr(uid), uintptr(unsafe.Pointer(&q)), 0, 0); errno != 0 {
		return 0, 0, fmt.Errorf("host: read the quota of the UID %d on %s: %w", uid, device, errno)
	}
	return int64(q.curSpace), int64(q.bHardLimit) * 1024, nil
}
