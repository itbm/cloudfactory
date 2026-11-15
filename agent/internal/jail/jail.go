// Package jail runs a program of root in a view of the file system that has only the
// directories that the program needs.
//
// A backup reads the files of a website as root, while the website operates. The owner of the
// website controls these files, and can replace a directory with a symbolic link at any time.
// A program that opens files by their path then follows the link. On the host, such a link
// can point to each file that root can read: the files of a different website, the keys of
// the agent, /etc/shadow (adversarial review, finding G2-01).
//
// The jail removes the targets. It is a new mount namespace whose root is an empty
// file system in memory. It has:
//
//   - the programs and libraries of the system (/usr), read-only;
//   - the CA certificates and the files that a program needs to find a host name, read-only;
//   - /dev/null, /dev/zero, /dev/random and /dev/urandom;
//   - each path of "--ro", read-only, at the same path as on the host;
//   - the directory of "--tmp" at /tmp, for temporary files and the cache of the program.
//
// A link in the files of a website then points to a file in this view or to nothing. The view
// has no file with a secret of the host: no /etc/shadow, no /etc/cloudfactory, no /root, no
// /proc and no files of a different website. The program keeps the network and the
// environment of the agent, so restic gets its credentials as before.
//
// The agent starts the jail as a command of its own program ("cf-agent jail"), because the
// mounts must be there before the program starts.
package jail

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// Command is the name of the command of the agent that runs a jail.
const Command = "jail"

// Options are the parts of a jail command.
type Options struct {
	// ReadOnly are the paths that the program can read: directories or files.
	ReadOnly []string
	// Tmp is a directory of the host for the temporary files of the program, or "".
	Tmp string
	// Argv is the program and its arguments.
	Argv []string
}

// Args gives the arguments of "cf-agent jail" for the options.
func (o Options) Args() []string {
	args := []string{Command}
	for _, p := range o.ReadOnly {
		args = append(args, "--ro", p)
	}
	if o.Tmp != "" {
		args = append(args, "--tmp", o.Tmp)
	}
	return append(append(args, "--"), o.Argv...)
}

// Parse reads the arguments after "jail". Each path must be absolute and clean, so that the
// policy of the root helper and the jail read the same path.
func Parse(args []string) (Options, error) {
	var o Options
	for len(args) > 0 {
		switch {
		case args[0] == "--":
			o.Argv = args[1:]
			if len(o.Argv) == 0 || o.Argv[0] == "" {
				return o, errors.New("jail: no program")
			}
			return o, nil
		case len(args) >= 2 && args[0] == "--ro":
			if err := checkPath(args[1]); err != nil {
				return o, err
			}
			o.ReadOnly = append(o.ReadOnly, args[1])
		case len(args) >= 2 && args[0] == "--tmp" && o.Tmp == "":
			if err := checkPath(args[1]); err != nil {
				return o, err
			}
			o.Tmp = args[1]
		default:
			return o, fmt.Errorf("jail: the argument %q is not valid", args[0])
		}
		args = args[2:]
	}
	return o, errors.New("jail: no program")
}

func checkPath(p string) error {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p || p == "/" || strings.ContainsRune(p, 0) {
		return fmt.Errorf("jail: %q is not a clean absolute path", p)
	}
	return nil
}

// checkPlace refuses a path of the caller that is a part of the jail.
func checkPlace(p string) error {
	// A path of the caller cannot replace a part of the jail. A file in /etc is possible, for
	// example the key file of the backups, but not a file that the jail makes there.
	for _, top := range []string{"/usr", "/dev", "/tmp", "/bin", "/sbin", "/lib", "/lib32", "/lib64", "/libx32", "/.host"} {
		if p == top || strings.HasPrefix(p, top+"/") {
			return fmt.Errorf("jail: %q is in a directory of the jail", p)
		}
	}
	if p == "/etc" {
		return fmt.Errorf("jail: %q is a directory of the jail", p)
	}
	for _, name := range append(append([]string{}, etcFiles...), etcDirs...) {
		if own := "/etc/" + name; p == own || strings.HasPrefix(p, own+"/") {
			return fmt.Errorf("jail: %q is a file of the jail", p)
		}
	}
	return nil
}

// base is the directory on which the jail mounts its root. Only root can open it. The mount
// is in the namespace of one jail, so the directory stays empty on the host.
const base = "/run/cloudfactory-jail"

// systemLinks are the top directories that are symbolic links into /usr on Debian and Ubuntu.
var systemLinks = []string{"bin", "sbin", "lib", "lib32", "lib64", "libx32"}

// etcFiles are the files of /etc that a program needs to find a host name and the name of a
// Unix user. Every Unix user of the host can read them.
var etcFiles = []string{"resolv.conf", "hosts", "nsswitch.conf", "host.conf", "gai.conf", "passwd", "group", "localtime"}

// etcDirs are the directories of /etc with the CA certificates.
var etcDirs = []string{"ssl", "ca-certificates"}

var devices = []string{"null", "zero", "random", "urandom"}

// Main makes the jail and replaces this process with the program. It returns only with an
// error. The caller is root.
func Main(args []string) error {
	o, err := Parse(args)
	if err != nil {
		return err
	}
	for _, p := range o.ReadOnly {
		if err := checkPlace(p); err != nil {
			return err
		}
	}
	// The namespace is of this thread until the program starts.
	runtime.LockOSThread()
	if err := enter(o); err != nil {
		return fmt.Errorf("jail: %w", err)
	}
	program, err := exec.LookPath(o.Argv[0])
	if err != nil {
		return fmt.Errorf("jail: %w", err)
	}
	if o.Tmp != "" {
		// The home directory of root is not in the jail. The program keeps its cache in the
		// temporary directory, which stays between runs.
		os.Setenv("HOME", "/tmp")
		os.Setenv("TMPDIR", "/tmp")
		os.Setenv("XDG_CACHE_HOME", "/tmp/cache")
	}
	return fmt.Errorf("jail: start %s: %w", program, syscall.Exec(program, o.Argv, os.Environ()))
}

const (
	readOnly = unix.MS_RDONLY | unix.MS_NOSUID | unix.MS_NODEV
	bind     = unix.MS_BIND | unix.MS_REC
)

func enter(o Options) error {
	if err := os.MkdirAll(base, 0o700); err != nil {
		return err
	}
	// The files that the jail copies: read them before the root changes. A link such as
	// /etc/resolv.conf -> /run/systemd/resolve/stub-resolv.conf has no target in the jail.
	copies := map[string][]byte{}
	for _, name := range etcFiles {
		if b, err := os.ReadFile(filepath.Join("/etc", name)); err == nil {
			copies[name] = b
		}
	}
	if err := unix.Unshare(unix.CLONE_NEWNS); err != nil {
		return fmt.Errorf("make a mount namespace: %w", err)
	}
	// No mount of the jail goes back to the host.
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("make the mounts private: %w", err)
	}
	if err := unix.Mount("cf-jail", base, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=0755,size=4m"); err != nil {
		return fmt.Errorf("mount the root: %w", err)
	}
	in := func(p string) string { return filepath.Join(base, p) }

	if err := bindPath("/usr", in("/usr"), true); err != nil {
		return err
	}
	for _, name := range systemLinks {
		info, err := os.Lstat("/" + name)
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink("/" + name)
			if err != nil {
				return err
			}
			if err := os.Symlink(target, in(name)); err != nil {
				return err
			}
		} else if info.IsDir() {
			if err := bindPath("/"+name, in(name), true); err != nil {
				return err
			}
		}
	}
	if err := os.Mkdir(in("/etc"), 0o755); err != nil {
		return err
	}
	for name, content := range copies {
		if err := os.WriteFile(in(filepath.Join("/etc", name)), content, 0o644); err != nil {
			return err
		}
	}
	for _, name := range etcDirs {
		if info, err := os.Stat(filepath.Join("/etc", name)); err == nil && info.IsDir() {
			if err := bindPath(filepath.Join("/etc", name), in(filepath.Join("/etc", name)), true); err != nil {
				return err
			}
		}
	}
	if err := os.Mkdir(in("/dev"), 0o755); err != nil {
		return err
	}
	for _, name := range devices {
		if err := bindPath("/dev/"+name, in("/dev/"+name), false); err != nil {
			return err
		}
	}
	if err := os.Mkdir(in("/tmp"), 0o700); err != nil {
		return err
	}
	if o.Tmp != "" {
		if err := os.MkdirAll(o.Tmp, 0o700); err != nil {
			return err
		}
		if err := bindPath(o.Tmp, in("/tmp"), false); err != nil {
			return err
		}
	}
	for _, p := range o.ReadOnly {
		if err := bindPath(p, in(p), true); err != nil {
			return err
		}
	}

	// The root of the process becomes the jail, and the file system of the host goes.
	old := in("/.host")
	if err := os.Mkdir(old, 0o700); err != nil {
		return err
	}
	if err := unix.PivotRoot(base, old); err != nil {
		return fmt.Errorf("change the root: %w", err)
	}
	if err := os.Chdir("/"); err != nil {
		return err
	}
	if err := unix.Unmount("/.host", unix.MNT_DETACH); err != nil {
		return fmt.Errorf("remove the file system of the host: %w", err)
	}
	if err := os.Remove("/.host"); err != nil {
		return err
	}
	// The program cannot add a file to the root of the jail.
	if err := unix.Mount("", "/", "", unix.MS_REMOUNT|unix.MS_BIND|readOnly, ""); err != nil {
		return fmt.Errorf("make the root read-only: %w", err)
	}
	return nil
}

// bindPath shows the path source of the host at target in the jail. No part of source can be
// a symbolic link: the owner of a website must not select what the jail shows. The mount is
// from the open file, so a change of the path after the check has no effect.
func bindPath(source, target string, ro bool) error {
	fd, err := unix.Openat2(unix.AT_FDCWD, source, &unix.OpenHow{
		Flags:   unix.O_PATH | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		return fmt.Errorf("open %s: %w", source, err)
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT == unix.S_IFDIR {
		if err := os.MkdirAll(target, 0o755); err != nil {
			return err
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		f.Close()
	}
	if err := unix.Mount(fmt.Sprintf("/proc/self/fd/%d", fd), target, "", bind, ""); err != nil {
		return fmt.Errorf("show %s in the jail: %w", source, err)
	}
	if ro {
		if err := unix.Mount("", target, "", unix.MS_REMOUNT|unix.MS_BIND|readOnly, ""); err != nil {
			return fmt.Errorf("make %s read-only: %w", source, err)
		}
	}
	return nil
}
