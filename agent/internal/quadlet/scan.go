package quadlet

import "strings"

// The malware scanner (docs/architecture.md#malware-scan). The Unix user cf-scan updates the
// ClamAV signatures with this unit. The scan itself runs in a container of the website user
// (actions.ScanWebsite), with no unit.

// Scan is the signature update of the malware scanner.
type Scan struct {
	Image  string
	Gvisor Runtime
	// SignatureDir holds the signatures. The website users can read it, and only cf-scan can
	// write it.
	SignatureDir string
}

// ScanSignatures is the directory of the signatures in the container of the scanner.
const ScanSignatures = "/var/lib/clamav"

// ScanUnit gives the unit that updates the signatures one time and stops: freshclam in the
// image of the scanner. It has no [Install] section, so it starts only when the agent starts
// it before a scan.
func ScanUnit(s Scan) string {
	lines := []string{
		"[Unit]",
		"Description=Cloudfactory malware signatures update",
		"",
		"[Container]",
		"ContainerName=cf-scan-update",
		"Image=" + s.Image,
		"Pull=never",
		"GlobalArgs=" + s.Gvisor.GlobalArgs(),
		// freshclam gets the signatures from database.clamav.net.
		"Network=" + PastaNetwork,
		// The label is shared ("z"), because the containers of the websites read the
		// signatures too.
		"Volume=" + s.SignatureDir + ":" + ScanSignatures + ":z",
		"Exec=freshclam",
		"NoNewPrivileges=true",
		"DropCapability=all",
		// freshclam runs as cf-scan, so the files of the signatures have that owner.
		"UserNS=keep-id",
		"ReadOnly=true",
		"Tmpfs=/tmp",
		"",
		"[Service]",
		"Type=oneshot",
		// The first update gets approximately 110 MB.
		"TimeoutStartSec=1800",
		"",
	}
	return Header + strings.Join(lines, "\n")
}
