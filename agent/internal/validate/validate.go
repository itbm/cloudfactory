// Package validate checks the values that the panel sends before the agent uses them
// in file paths, unit names or command arguments.
package validate

import (
	"fmt"
	"net"
	"net/url"
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
	sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)
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

// reservedDatabaseNames are names of the shared database servers (CF-01).
var reservedDatabaseNames = map[string]bool{
	"mysql": true, "sys": true, "information_schema": true, "performance_schema": true,
	"postgres": true, "template0": true, "template1": true, "root": true, "mariadb.sys": true, "healthcheck": true,
}

// TenantDatabaseName checks the name of a database or a database user of a website. The name
// must start with the Unix user of the website and "_", so that a tenant cannot use a system
// name or a name of a different tenant (CF-01).
func TenantDatabaseName(unixUser, s string) error {
	if err := UnixUser(unixUser); err != nil {
		return err
	}
	if err := DatabaseName(s); err != nil {
		return err
	}
	if !strings.HasPrefix(s, unixUser+"_") || len(s) == len(unixUser)+1 {
		return fmt.Errorf("the database name %q does not start with %q", s, unixUser+"_")
	}
	if reservedDatabaseNames[s] || strings.HasPrefix(s, "pg_") {
		return fmt.Errorf("the database name %q is reserved", s)
	}
	return nil
}

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

// SHA256 checks a lower-case SHA-256 hash in hexadecimal.
func SHA256(s string) error { return match(sha256Hex, s, "SHA-256 hash") }

// Version checks an agent version, for example "0.1.0".
func Version(s string) error { return match(version, s, "version") }

// RestorePath checks a file path in a website. It must start with "/" and must not go up.
func RestorePath(s string) error {
	if !strings.HasPrefix(s, "/") || strings.ContainsRune(s, 0) || path.Clean(s) != s || strings.Contains(s, "/../") || strings.HasSuffix(s, "/..") {
		return fmt.Errorf("the path %q is not permitted", s)
	}
	return nil
}

// PanelURL accepts an https:// address. It accepts http:// only for a loopback address, where
// the panel and the agent are on the same server (R-15). Over plain HTTP, somebody between the
// server and the panel could change the join token and the panel certificate fingerprint.
func PanelURL(s string) error {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.User != nil {
		return fmt.Errorf("the panel address %q is not valid", s)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) {
			return nil
		}
	}
	return fmt.Errorf("the panel address %q must start with https://", s)
}

func match(re *regexp.Regexp, s, what string) error {
	if !re.MatchString(s) {
		return fmt.Errorf("the %s %q is not valid", what, s)
	}
	return nil
}
