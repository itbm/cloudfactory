// Package validate checks the values that the panel sends before the agent uses them
// in file paths, unit names or command arguments.
package validate

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// rule is one rule of schema/input-rules.json. The generated file rules_gen.go has the rules
// and one function for each rule, for example Domain and UnixUser. The panel API has the same
// rules (api/app/Validation/InputRules.php), and the two run the examples in
// schema/input-vectors.json. Do not write a pattern here: change the rules file and run
// "go run ./cmd/cf-rules".
type rule struct {
	what      string
	pattern   *regexp.Regexp
	also, not []*regexp.Regexp
	maxLength int
	empty     bool
	// echo is false when the error must not repeat the value, for example a long key.
	echo bool
}

func (r *rule) check(s string) error {
	if s == "" && r.empty {
		return nil
	}
	ok := (r.maxLength == 0 || len(s) <= r.maxLength) && r.pattern.MatchString(s)
	for _, also := range r.also {
		ok = ok && also.MatchString(s)
	}
	for _, not := range r.not {
		ok = ok && !not.MatchString(s)
	}
	switch {
	case ok:
		return nil
	case r.echo:
		return fmt.Errorf("the %s %q is not valid", r.what, s)
	}
	return fmt.Errorf("the %s is not valid", r.what)
}

// PanelDomain checks the domain of the panel. It is a domain, or "localhost" for a panel that
// only a browser on the same server opens.
func PanelDomain(s string) error {
	if s == "localhost" {
		return nil
	}
	return Domain(s)
}

// SftpPort checks the public SFTP port of a server. The SSH service of the host keeps port 22,
// and the agent and the edge proxy have their own ports.
func SftpPort(port int) error {
	switch port {
	case 22, 80, 443, 3306, 5432, 6379, 7443, 8080, 8443, 19080, 19081, 19432, 19443:
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
	if slices.Contains(reservedDatabaseNames, s) {
		return fmt.Errorf("the database name %q is reserved", s)
	}
	for _, prefix := range reservedDatabasePrefixes {
		if strings.HasPrefix(s, prefix) {
			return fmt.Errorf("the database name %q is reserved", s)
		}
	}
	return nil
}

// Command checks the start command of a custom image.
func Command(args []string) error {
	if len(args) > MaxCommandArguments {
		return fmt.Errorf("the start command has more than %d arguments", MaxCommandArguments)
	}
	for _, a := range args {
		if err := CommandArgument(a); err != nil {
			return err
		}
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

// Port checks a TCP port.
func Port(p int32) error {
	if p < 0 || p > 65535 {
		return fmt.Errorf("the port %d is not valid", p)
	}
	return nil
}
