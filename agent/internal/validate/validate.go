// Package validate checks the values that the panel sends before the agent uses them
// in file paths, unit names or command arguments.
package validate

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

var (
	unixUser  = regexp.MustCompile(`^[a-z][a-z0-9]{0,15}_[a-z0-9]{4}$`)
	domain    = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	id        = regexp.MustCompile(`^[0-9A-Za-z]{26}$`)
	dbName    = regexp.MustCompile(`^[a-z][a-z0-9_]{1,62}$`)
	image     = regexp.MustCompile(`^[a-z0-9.-]+(:[0-9]+)?(/[a-z0-9._-]+)+:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	digest    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	extension = regexp.MustCompile(`^[a-z0-9_]{1,40}$`)
	iniKey    = regexp.MustCompile(`^[a-z_.]{1,64}$`)
	version   = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`)
)

// UnixUser checks a website user name, for example "acme_x7k2".
func UnixUser(s string) error { return match(unixUser, s, "Unix user") }

// Domain checks a lower-case domain name.
func Domain(s string) error {
	if len(s) > 253 {
		return fmt.Errorf("the domain %q is too long", s)
	}
	return match(domain, s, "domain")
}

// ID checks a ULID from the panel.
func ID(s string) error { return match(id, s, "ID") }

// DatabaseName checks a database or database user name.
func DatabaseName(s string) error { return match(dbName, s, "database name") }

// Image checks an image reference with a tag.
func Image(s string) error { return match(image, s, "image") }

// Digest checks an image digest. An empty digest is permitted.
func Digest(s string) error {
	if s == "" {
		return nil
	}
	return match(digest, s, "digest")
}

// Extension checks the name of a PHP extension.
func Extension(s string) error { return match(extension, s, "PHP extension") }

// IniKey checks the name of a php.ini setting.
func IniKey(s string) error { return match(iniKey, s, "php.ini key") }

// IniValue checks a php.ini value. It must be on one line and must not contain quotes.
func IniValue(s string) error {
	if len(s) > 200 || strings.ContainsAny(s, "\"\n\r\x00") {
		return fmt.Errorf("the php.ini value %q is not permitted", s)
	}
	return nil
}

// Version checks an agent version, for example "0.1.0".
func Version(s string) error { return match(version, s, "version") }

// RestorePath checks a file path in a website. It must start with "/" and must not go up.
func RestorePath(s string) error {
	if !strings.HasPrefix(s, "/") || strings.ContainsRune(s, 0) || path.Clean(s) != s || strings.Contains(s, "/../") || strings.HasSuffix(s, "/..") {
		return fmt.Errorf("the path %q is not permitted", s)
	}
	return nil
}

func match(re *regexp.Regexp, s, what string) error {
	if !re.MatchString(s) {
		return fmt.Errorf("the %s %q is not valid", what, s)
	}
	return nil
}
