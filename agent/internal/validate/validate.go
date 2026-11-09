// Package validate checks the values that the panel sends before the agent uses them
// in file paths, unit names or command arguments.
package validate

import (
	"errors"
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

// SftpPort checks the public SFTP port of a server. The SSH service of the host keeps port 22,
// and the agent and the edge proxy have their own ports.
func SftpPort(port int) error {
	switch port {
	case 22, 80, 443, 3306, 5432, 7443, 8080, 8443, 19080, 19081:
		return fmt.Errorf("the SFTP port %d is in use by a different service", port)
	}
	// The ports of the website containers (actions.NewState).
	if port >= 20000 && port <= 29999 {
		return fmt.Errorf("the SFTP port %d is in the range of the website ports (20000 to 29999)", port)
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("the SFTP port %d is not a port number", port)
	}
	return nil
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

var docRootPart = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)

// DocumentRoot checks a directory in /app, for example "public" or "web/htdocs". "." is /app.
func DocumentRoot(s string) error {
	if s == "" || s == "." {
		return nil
	}
	parts := strings.Split(s, "/")
	if len(parts) > 8 {
		return fmt.Errorf("the document root %q is not valid", s)
	}
	for _, p := range parts {
		if !docRootPart.MatchString(p) || p == "." || p == ".." {
			return fmt.Errorf("the document root %q is not valid", s)
		}
	}
	return nil
}

// Command checks the start command of a custom image: at most 32 arguments with no control
// characters.
func Command(args []string) error {
	if len(args) > 32 {
		return errors.New("the start command has more than 32 arguments")
	}
	for _, a := range args {
		if a == "" || len(a) > 1024 {
			return errors.New("each argument of the start command must have 1 to 1024 characters")
		}
		for _, r := range a {
			if r < 0x20 || r == 0x7f {
				return errors.New("the start command has a control character")
			}
		}
	}
	return nil
}

// Port checks a TCP port.
func Port(p int32) error {
	if p < 0 || p > 65535 {
		return fmt.Errorf("the port %d is not valid", p)
	}
	return nil
}

var sshKey = regexp.MustCompile(`^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp256|ecdsa-sha2-nistp384|ecdsa-sha2-nistp521|sk-ssh-ed25519@openssh\.com|sk-ecdsa-sha2-nistp256@openssh\.com) [A-Za-z0-9+/]+={0,3}( [ -~]{0,200})?$`)

// SSHKey checks one public key in the format of authorized_keys, with no options.
func SSHKey(s string) error {
	if len(s) > 8192 || !sshKey.MatchString(s) {
		return errors.New("the SSH key is not a valid public key")
	}
	return nil
}
