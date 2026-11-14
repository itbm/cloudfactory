#!/bin/sh
# Cloudfactory installation script (spec section 7.18).
#
# Install a new panel (FR-1802):
#   curl -fsSL https://github.com/itbm/cloudfactory/releases/latest/download/install.sh | sh
#   curl -fsSL https://github.com/itbm/cloudfactory/releases/latest/download/install.sh | sh -s -- --domain panel.example.com --email you@example.com
#
# A new panel gets a TLS certificate from its own certificate authority (CA). This works for all
# domains, also panel.localhost. The administrator can change to Let's Encrypt in the panel.
#
# Add a server to a panel:
#   curl -fsSL https://github.com/itbm/cloudfactory/releases/latest/download/install.sh | sh -s -- --join <TOKEN> --panel https://panel.example.com
#
# The script first does all checks. It does not change the system until all checks pass
# (FR-1804). If a step fails, the script stops and shows the step that failed.
#
# Check the script before you run it. Each release has install.sh.sha256 and install.sh.sig:
#   sha256sum -c install.sh.sha256
#   cosign verify-blob --key cosign.pub --signature install.sh.sig install.sh
#
# SPDX-License-Identifier: AGPL-3.0-only

set -eu

# The release pipeline replaces these values.
CF_VERSION="${CF_VERSION:-__CF_VERSION__}"
GVISOR_VERSION="${GVISOR_VERSION:-__GVISOR_VERSION__}"
# CF-16: the SHA-512 checksums of the gVisor archive for GVISOR_VERSION. The script does not
# download the checksum from the same server as the archive.
GVISOR_SHA512_X86_64='__GVISOR_SHA512_X86_64__'
GVISOR_SHA512_AARCH64='__GVISOR_SHA512_AARCH64__'
REPOSITORY="${CF_REPOSITORY:-itbm/cloudfactory}"
REGISTRY="${CF_REGISTRY:-__CF_REGISTRY__}"
RELEASE_URL="${CF_RELEASE_URL:-https://github.com/${REPOSITORY}/releases/download}"
GVISOR_URL="${CF_GVISOR_URL:-https://storage.googleapis.com/gvisor/releases/release}"
# Tests set CF_TEST_ROOT to read fake system files.
ROOT="${CF_TEST_ROOT:-}"
OS_RELEASE="$ROOT/etc/os-release"

# The public key that signs the agent binary and the images (FR-1101, FR-1807).
COSIGN_PUBLIC_KEY='__COSIGN_PUBLIC_KEY__'

MODE=panel
JOIN_TOKEN=""
PANEL_URL=""
PANEL_PIN=""
# The SHA-256 fingerprint of the root certificate of the panel CA, for a panel that has a
# certificate from its own CA (--panel-ca-sha256).
PANEL_CA_PIN=""
# The certificate method of the panel domain: "internal" (the panel CA) or "letsencrypt". Empty
# keeps the method of a panel that operates. A new panel uses "internal".
CERTIFICATE=""
DOMAIN=""
EMAIL=""
NAME=""
ROLES=""
ADDRESS=""
PANEL_IP=""
CHECK_ONLY=0
UNSIGNED_REGISTRIES=""
# The SFTP port of the websites. Port 22 stays for the SSH service of the host.
SFTP_PORT=2200
# The private address of the container ports on "lo" (quadlet.ServiceAddress in the agent).
SERVICE_ADDRESS=169.254.77.1
# The private network of the database servers on "lo". Each customer has its own database
# servers on a server, on one address of this network. The agent has the same values
# (quadlet.DatabaseNetwork and quadlet.DatabaseClientAddress).
DATABASE_NETWORK=169.254.80.0/20
DATABASE_CLIENT_ADDRESS=169.254.80.1
# The files of Cloudfactory in /etc. The agent writes its firewall files in "firewall".
CF_ETC=/etc/cloudfactory
# Audit finding S1: the agent makes each website user with a UID and a GID in this range
# (host.WebsiteIDMin and host.WebsiteIDMax in the agent). The firewall finds the connections
# of the website containers by this range.
WEBSITE_UID_MIN=20000
WEBSITE_UID_MAX=59999
# The websites cannot connect to port 25 and to private networks, unless the operator
# permits it.
WEBSITE_SMTP=0
WEBSITE_NETWORKS=""

usage() {
	cat <<'USAGE'
Usage:
  install.sh [--domain DOMAIN] [--email EMAIL]        Install a new panel on this server.
                                                      The default domain is the full host name.
  install.sh --join TOKEN --panel URL                 Add this server to a panel.

Options:
  --certificate METHOD The TLS certificate of the panel domain: "internal" (a certificate
                       from the certificate authority of the panel) or "letsencrypt". A new
                       panel uses "internal", which works also for localhost and
                       panel.localhost. You can change the method in the panel later.
  --panel-ca-sha256 SHA256
                       With --join: the SHA-256 fingerprint of the root certificate of the
                       panel CA. Give it when the panel uses the "internal" certificate. The
                       panel shows the full command in Servers > Add server.
  --name NAME          The name of this server in the panel. The default is the host name.
  --roles ROLES        The roles of this server: web, database, email, dns, backup.
                       The default is "web" for --join, and all roles for a new panel.
  --address HOST       The host name or IP address that the panel uses to reach this server.
  --panel-ip IP        Accept the agent port only from this IP address (the panel).
  --sftp-port PORT     The SFTP port of the websites. The default is 2200. Port 22 stays
                       for the SSH service of this server.
  --registry URL       Use a different image registry, for example a mirror (FR-1813).
  --allow-unsigned REGISTRY
                       Let Podman pull images with no signature from REGISTRY, for example
                       docker.io. Podman refuses all other unsigned images. You can give
                       this option more than one time.
  --allow-website-smtp
                       Let the websites connect to port 25 (SMTP) of other servers. The
                       default refuses these connections, so that a website cannot send
                       email directly. Ports 465 and 587 are always open.
  --allow-website-network NETWORK
                       Let the websites connect to this private address or network, for
                       example 10.0.5.10 or 10.0.5.0/24. The default refuses the private
                       and link-local networks and the services of this server. You can
                       give this option more than one time.
  --check              Do only the checks. Do not change the system.
  --help               Show this help.
USAGE
}

info() { printf '%s\n' "$*"; }
fail() {
	printf 'Error: %s\n' "$*" >&2
	exit 1
}

# value gives the value of an option, or stops when the option has no value.
value() {
	if [ $# -lt 2 ] || [ -z "$2" ]; then
		usage >&2
		fail "the option $1 needs a value."
	fi
	printf '%s' "$2"
}

while [ $# -gt 0 ]; do
	case "$1" in
	--join) MODE="join"; JOIN_TOKEN=$(value "$@"); shift ;;
	--panel) PANEL_URL=$(value "$@"); shift ;;
	--panel-ca-sha256) PANEL_CA_PIN=$(value "$@"); shift ;;
	--certificate) CERTIFICATE=$(value "$@"); shift ;;
	--domain) DOMAIN=$(value "$@"); shift ;;
	--email) EMAIL=$(value "$@"); shift ;;
	--name) NAME=$(value "$@"); shift ;;
	--roles) ROLES=$(value "$@"); shift ;;
	--address) ADDRESS=$(value "$@"); shift ;;
	--panel-ip) PANEL_IP=$(value "$@"); shift ;;
	--sftp-port) SFTP_PORT=$(value "$@"); shift ;;
	--registry) REGISTRY=$(value "$@"); shift ;;
	--allow-unsigned) UNSIGNED_REGISTRIES="$UNSIGNED_REGISTRIES $(value "$@")"; shift ;;
	--allow-website-smtp) WEBSITE_SMTP=1 ;;
	--allow-website-network) WEBSITE_NETWORKS="$WEBSITE_NETWORKS $(value "$@")"; shift ;;
	--check) CHECK_ONLY=1 ;;
	--help | -h) usage; exit 0 ;;
	*) usage >&2; fail "unknown option: $1" ;;
	esac
	shift
done

# ---------------------------------------------------------------------------
# Checks. These do not change the system.
# ---------------------------------------------------------------------------

ERRORS=""
problem() { ERRORS="${ERRORS}  - $*
"; }

os_value() {
	# Reads one value from os-release, with no quotes.
	sed -n "s/^$1=//p" "$OS_RELEASE" | tr -d '"' | head -n 1
}

check_os() {
	[ -r "$OS_RELEASE" ] || { problem "cannot read $OS_RELEASE"; return; }
	OS_ID=$(os_value ID)
	OS_VERSION=$(os_value VERSION_ID)
	case "$OS_ID-$OS_VERSION" in
	debian-13 | ubuntu-26.04) ;;
	*) problem "this operating system is $OS_ID $OS_VERSION. Cloudfactory operates only on Debian 13 and Ubuntu 26.04 LTS (FR-1205)." ;;
	esac
}

check_arch() {
	case "$(uname -m)" in
	x86_64 | amd64) ARCH=amd64; GVISOR_ARCH=x86_64; GVISOR_SHA512=$GVISOR_SHA512_X86_64 ;;
	aarch64 | arm64) ARCH=arm64; GVISOR_ARCH=aarch64; GVISOR_SHA512=$GVISOR_SHA512_AARCH64 ;;
	*) problem "the CPU architecture $(uname -m) is not supported. Use x86-64 or ARM64." ;;
	esac
}

check_root() {
	[ "$(id -u)" -eq 0 ] || problem "run this script as root, for example with sudo."
}

check_systemd() {
	[ -d "$ROOT/run/systemd/system" ] || problem "systemd is not the init system."
	[ -f "$ROOT/sys/fs/cgroup/cgroup.controllers" ] || problem "cgroup v2 is not mounted. Cloudfactory needs cgroup v2 for resource limits."
}

check_tools() {
	for tool in curl openssl sha512sum sha256sum base64; do
		command -v "$tool" >/dev/null 2>&1 || problem "the command $tool is not installed."
	done
}

check_arguments() {
	case "$MODE" in
	join)
		[ -n "$JOIN_TOKEN" ] || problem "--join needs a token. Get one in the panel: Servers > Add server."
		case "$PANEL_URL" in
		https://*) ;;
		*) problem "--join needs --panel with an https:// address." ;;
		esac
		case "$PANEL_CA_PIN" in
		"") ;;
		*[!0-9a-fA-F]*) problem "--panel-ca-sha256 needs the SHA-256 fingerprint of the panel CA (64 hex characters)." ;;
		*) [ "${#PANEL_CA_PIN}" -eq 64 ] || problem "--panel-ca-sha256 needs the SHA-256 fingerprint of the panel CA (64 hex characters)." ;;
		esac
		[ -z "$CERTIFICATE" ] || problem "--certificate is only for a new panel, not with --join."
		;;
	panel)
		if [ -z "$DOMAIN" ]; then
			DOMAIN=$(hostname -f 2>/dev/null || true)
			case "$DOMAIN" in
			*.*) ;;
			*) DOMAIN="" ;;
			esac
		fi
		[ -n "$DOMAIN" ] || problem "the host name is not a full domain. Give the domain of the panel with --domain, for example --domain panel.example.com or --domain panel.localhost."
		[ -n "$EMAIL" ] || EMAIL="admin@$DOMAIN"
		case "$EMAIL" in
		*@*.*) ;;
		*) problem "give the email address of the first administrator with --email." ;;
		esac
		[ -z "$PANEL_CA_PIN" ] || problem "--panel-ca-sha256 is only for --join."
		case "$CERTIFICATE" in
		"" | internal) ;;
		letsencrypt)
			# The same names as caddy.PublicName in the agent.
			case "$DOMAIN" in
			*.*) ;;
			*) problem "Let's Encrypt gives certificates only for public domains. Use --certificate internal for $DOMAIN." ;;
			esac
			case "$DOMAIN" in
			*.localhost | *.local | *.internal | *.home.arpa | *.test | *.example | *.invalid)
				problem "Let's Encrypt gives certificates only for public domains. Use --certificate internal for $DOMAIN." ;;
			esac
			;;
		*) problem "--certificate must be internal or letsencrypt." ;;
		esac
		;;
	esac
	case "$CF_VERSION" in
	__*) problem "this script has no release version. Download it from a GitHub release." ;;
	esac
	case "$COSIGN_PUBLIC_KEY" in
	__*) problem "this script has no signing key. Download it from a GitHub release." ;;
	esac
	case "${GVISOR_SHA512:-}" in
	__* | "") ;;
	*[!0-9a-f]*) problem "the gVisor checksum is not a SHA-512 value." ;;
	*) [ "${#GVISOR_SHA512}" -eq 128 ] || problem "the gVisor checksum is not a SHA-512 value." ;;
	esac
	case "${GVISOR_SHA512:-}" in
	__*) problem "this script has no gVisor checksum. Download it from a GitHub release." ;;
	esac
	# The same check as validate.SftpPort in the agent.
	case "$SFTP_PORT" in
	*[!0-9]* | 0*) problem "--sftp-port $SFTP_PORT is not a port number." ;;
	22 | 80 | 443 | 3306 | 5432 | 6379 | 7443 | 8080 | 8443 | 19080 | 19081 | 19432 | 19443) problem "--sftp-port $SFTP_PORT is in use by a different Cloudfactory service." ;;
	*)
		if [ "$SFTP_PORT" -gt 65535 ]; then
			problem "--sftp-port $SFTP_PORT is not a port number."
		elif [ "$SFTP_PORT" -ge 20000 ] && [ "$SFTP_PORT" -le 29999 ]; then
			problem "--sftp-port $SFTP_PORT is in the range of the website ports (20000 to 29999)."
		fi
		;;
	esac
	for registry in $UNSIGNED_REGISTRIES; do
		case "$registry" in
		*[!A-Za-z0-9.:/_-]* | "$REGISTRY" | "$REGISTRY"/*) problem "--allow-unsigned $registry is not permitted." ;;
		esac
	done
	# An IPv4 or IPv6 address, with or without a prefix length. nftables does the full check.
	for network in $WEBSITE_NETWORKS; do
		case "$network" in
		*[!0-9a-fA-F.:/]* | /* | */ | */*/* | */0 | *.*:* | *:*.*) problem "--allow-website-network $network is not an IP address or a network." ;;
		*[0-9].[0-9]* | *:*) ;;
		*) problem "--allow-website-network $network is not an IP address or a network." ;;
		esac
	done
}

# firewall_rules prints the nftables rules of Cloudfactory.
#
# FR-1705: block all incoming traffic except the published ports, SSH, SFTP and the agent port.
# The agent finds the SFTP rule by its comment, and changes it when the SFTP port changes.
# Spec section 15, question 7: rootless containers cannot open ports below 1024, so the
# firewall redirects 80 and 443 to the high ports of the edge proxy. The client address does
# not change (FR-1707).
firewall_rules() {
	agent_rule="tcp dport 7443 accept"
	[ -n "$PANEL_IP" ] && agent_rule="ip saddr $PANEL_IP tcp dport 7443 accept"
	edge_uid=$(id -u cf-edge)
	panel_uid=$(id -u cf-panel)
	website_networks=""
	for network in $WEBSITE_NETWORKS; do
		family=ip
		case "$network" in
		*:*) family=ip6 ;;
		esac
		website_networks="$website_networks    $family daddr $network accept
"
	done
	smtp_rule="tcp dport 25 jump refuse"
	[ "$WEBSITE_SMTP" -eq 1 ] && smtp_rule="# The option --allow-website-smtp: the websites can connect to port 25."
	cat <<NFT
table inet cloudfactory
delete table inet cloudfactory
table inet cloudfactory {
  # Each customer has its own database servers on a server, on one address of the network
  # $DATABASE_NETWORK. Each database server has its own port on that address. Each element is
  # the UID of a website, the address of its customer, and the port of a database server of
  # its customer. The agent writes the elements in the file
  # $CF_ETC/firewall/database-access.nft, which the last line of this file includes.
  set database_access {
    typeof meta skuid . ip daddr . tcp dport
  }
  chain input {
    type filter hook input priority filter; policy drop;
    ct state established,related accept
    iif lo accept
    meta l4proto { icmp, ipv6-icmp } accept
    tcp dport 22 accept
    tcp dport $SFTP_PORT accept # cloudfactory-sftp
    tcp dport { 80, 443, 8080, 8443 } accept
    udp dport { 443, 8443 } accept
    $agent_rule
  }
  chain output {
    type filter hook output priority filter; policy accept;
    # FR-1703: only root (the agent) and the edge proxy can connect to the website and panel
    # ports on the private service address. pasta opens each connection of a container as the
    # Unix user of the container, so a website cannot reach a different website. The rule
    # checks only new connections, because the answers of the containers go to the address too.
    #
    # Ports 3306 and 5432 are the shared database containers of an earlier release. A server
    # has them only until the databases of its websites move to database servers of each customer.
    #
    # Port 19432 is the database of the panel. Only root and the containers of the panel
    # (cf-panel) can connect to it. Each connection also needs the password.
    ct state new ip daddr $SERVICE_ADDRESS tcp dport 19432 meta skuid != { 0, $panel_uid } reject with tcp reset
    ct state new ip daddr $SERVICE_ADDRESS tcp dport != { 3306, 5432, 19432 } meta skuid != { 0, $edge_uid } reject with tcp reset
    # The database servers of the customers. Only root (the agent) and the websites can
    # connect to them. The chain "websites" lets a website connect only to the database
    # servers of its own customer.
    ct state new ip daddr $DATABASE_NETWORK meta skuid != 0 meta skuid != $WEBSITE_UID_MIN-$WEBSITE_UID_MAX jump refuse
    # Audit finding S1: the limits for the connections that a website container opens. The
    # agent makes each website user with a UID from $WEBSITE_UID_MIN to $WEBSITE_UID_MAX. The
    # rule checks only new connections, so it does not change the answers to the visitors.
    ct state new meta skuid $WEBSITE_UID_MIN-$WEBSITE_UID_MAX jump websites
  }
  chain websites {
    # The database servers of the customer of the website: MariaDB, PostgreSQL and Valkey.
    # A website cannot connect to a database server of a different customer.
    meta skuid . ip daddr . tcp dport @database_access accept
    # The shared database containers of an earlier release.
    ip daddr $SERVICE_ADDRESS tcp dport { 3306, 5432 } accept
    # DNS. The resolver of the host can have a private or a link-local address.
    meta l4proto { tcp, udp } th dport 53 accept
    # The networks of the option --allow-website-network.
${website_networks}    # The edge proxy on the addresses of this server, so that a website can call its own
    # public address. All other services of this server are closed for the websites.
    fib daddr type local tcp dport { 80, 443, 8080, 8443 } accept
    fib daddr type local udp dport { 443, 8443 } accept
    fib daddr type { local, broadcast, multicast } jump refuse
    # Link-local addresses (the metadata service of a cloud provider), private networks and
    # the service address of Azure.
    ip daddr { 0.0.0.0/8, 10.0.0.0/8, 100.64.0.0/10, 127.0.0.0/8, 168.63.129.16, 169.254.0.0/16, 172.16.0.0/12, 192.168.0.0/16, 224.0.0.0/3 } jump refuse
    ip6 daddr { ::1, fc00::/7, fe80::/10, ff00::/8 } jump refuse
    # SMTP: a website must not send email directly from the address of this server.
    $smtp_rule
  }
  chain refuse {
    meta l4proto tcp reject with tcp reset
    reject
  }
  chain prerouting {
    type nat hook prerouting priority dstnat;
    tcp dport 80 redirect to :8080
    tcp dport 443 redirect to :8443
    udp dport 443 redirect to :8443
  }
  chain output_nat {
    # A connection that a program on this server opens does not go through the prerouting
    # chain. Thus a browser on this server (for example https://panel.localhost), or a website
    # that calls the public address of this server, needs the same redirects here. The
    # output chain then sees port 8080 or 8443.
    type nat hook output priority -100;
    # pasta cannot send a connection from ::1 into gVisor (refer to postrouting_nat), and IPv6
    # has no private service address. Thus a connection from ::1 to port 80 or 443 is refused,
    # and the browser then uses 127.0.0.1.
    ip6 saddr ::1 return
    fib daddr type local tcp dport 80 redirect to :8080
    fib daddr type local tcp dport 443 redirect to :8443
    fib daddr type local udp dport 443 redirect to :8443
  }
  chain postrouting_nat {
    # pasta sends a connection from a loopback address to the loopback interface in the network
    # namespace of the edge container. gVisor does not listen there, so pasta closes the
    # connection. This occurs for https://panel.localhost, and for the full host name, which
    # /etc/hosts gives as 127.0.1.1. Thus a redirected connection from a loopback address gets
    # the private service address as its source address. pasta then sends it to gVisor.
    type nat hook postrouting priority 100;
    ip saddr 127.0.0.0/8 ct status dnat meta l4proto { tcp, udp } th dport { 8080, 8443 } snat ip to $SERVICE_ADDRESS
  }
}
# The elements of the set database_access. The agent writes them.
include "$CF_ETC/firewall/*.nft"
NFT
}

# With --join, this server must connect to the panel. The script examines the connection before
# it changes the system. Before, a wrong address stopped the script only at the last step, after
# it installed all packages. The certificate of the panel can be from the panel CA, so this check
# does not examine the certificate: the agent does that with the fingerprint when it joins.
check_panel() {
	[ "$MODE" = join ] || return 0
	case "$PANEL_URL" in
	https://*) ;;
	*) return 0 ;;
	esac
	command -v curl >/dev/null 2>&1 || return 0
	if curl -ksS -o /dev/null --connect-timeout 10 --max-time 20 "$PANEL_URL/" 2>/dev/null; then
		return 0
	fi
	panel_host=${PANEL_URL#https://}
	panel_host=${panel_host%%/*}
	case "$panel_host" in
	localhost | localhost:* | *.localhost | *.localhost:* | 127.* | "[::1]" | "[::1]:"*)
		problem "this server cannot connect to the panel at $PANEL_URL. This address is a name for the computer itself, so only the panel server can use it. Install the panel with a domain or an address that this server can reach."
		;;
	*)
		problem "this server cannot connect to the panel at $PANEL_URL. Make sure that the address is correct, that its DNS record points to the panel server, and that port 443 of the panel server is open for this server."
		;;
	esac
}

check_ports() {
	command -v ss >/dev/null 2>&1 || return 0
	ports=$SFTP_PORT
	[ "$MODE" = panel ] && ports="80 443 $SFTP_PORT"
	for port in $ports; do
		# When you run the script again, the SFTP container already uses its port.
		if [ "$port" = "$SFTP_PORT" ] && systemctl is-active --quiet cf-sftp.service 2>/dev/null; then
			continue
		fi
		if ss -Hltn "sport = :$port" 2>/dev/null | grep -q .; then
			if [ "$port" = "$SFTP_PORT" ]; then
				problem "port $port is in use. Stop the service that uses it, or give a different port with --sftp-port."
			else
				problem "port $port is in use. The edge proxy needs ports 80 and 443. Stop the service that uses the port, for example a web server."
			fi
		fi
	done
}

check_resources() {
	mem_kb=$(sed -n 's/^MemTotal: *\([0-9]*\) kB/\1/p' "$ROOT/proc/meminfo" 2>/dev/null || echo 0)
	need=1000000
	[ "$MODE" = panel ] && need=1900000
	[ "${mem_kb:-0}" -ge "$need" ] || problem "this server has $((${mem_kb:-0} / 1024)) MB of memory. Cloudfactory needs $((need / 1024)) MB or more."
	free_kb=$(df -Pk / | awk 'NR == 2 { print $4 }')
	[ "${free_kb:-0}" -ge 10000000 ] || problem "the root file system has less than 10 GB free."
}

info "Cloudfactory $CF_VERSION: checks"
check_root
check_os
check_arch
check_systemd
check_tools
check_arguments
check_panel
check_ports
check_resources

if [ -n "$ERRORS" ]; then
	printf 'The checks found these problems. The script did not change the system.\n%s' "$ERRORS" >&2
	exit 1
fi
info "All checks passed."
if [ "$CHECK_ONLY" -eq 1 ]; then
	# The tests of this script read the firewall rules. This does not change the system.
	if [ -n "$ROOT" ] && [ "${CF_TEST_PRINT:-}" = firewall ]; then
		firewall_rules
	fi
	exit 0
fi

# ---------------------------------------------------------------------------
# Steps. Each step is idempotent, so you can run the script again after a failure.
# ---------------------------------------------------------------------------

# Run the steps from the root directory. The script runs commands as the service users, for
# example "runuser -u cf-panel -- podman". These commands start in the working directory of
# the script. A service user cannot open /root or a home directory, and rootless Podman then
# stops with "cannot chdir to /root: Permission denied".
cd /
# The SSH session of root sets these variables. They point to the files of root, for example
# the D-Bus session of root in /run/user/0. The commands as the service users must not use them.
unset DBUS_SESSION_BUS_ADDRESS XDG_RUNTIME_DIR XDG_SESSION_ID XDG_SESSION_TYPE XDG_SESSION_CLASS \
	XDG_CONFIG_HOME XDG_DATA_HOME XDG_CACHE_HOME XDG_STATE_HOME

STEP=0
STEPS=14
[ "$MODE" = panel ] && STEPS=16
CURRENT=""
TMP=$(mktemp -d)
on_exit() {
	status=$?
	rm -rf "$TMP"
	if [ "$status" -ne 0 ] && [ -n "$CURRENT" ]; then
		printf '\nThe step "%s" failed. Correct the problem and run the script again.\n' "$CURRENT" >&2
	fi
}
trap on_exit EXIT

step() {
	STEP=$((STEP + 1))
	CURRENT="$1"
	info "[$STEP/$STEPS] $1"
}

download() {
	curl -fsSL --retry 3 --proto '=https' --tlsv1.2 -o "$2" "$1"
}

export DEBIAN_FRONTEND=noninteractive
CF_DATA=/var/lib/cloudfactory
GVISOR_ROOT=/usr/local/lib/cloudfactory/gvisor

step "Install Podman and the system packages"
apt-get update -q
# FR-1805: Podman from the official repositories of the operating system.
apt-get install -y -q --no-install-recommends \
	podman passt uidmap netavark aardvark-dns conmon crun fuse-overlayfs \
	nftables unattended-upgrades needrestart restic apparmor apparmor-utils \
	systemd-container dbus-user-session ca-certificates curl openssl bzip2 \
	openssh-server quota iproute2

# The website users mount the shared images with fuse-overlayfs (agent package images).
modprobe fuse 2>/dev/null || true
echo fuse >/etc/modules-load.d/cloudfactory-fuse.conf

step "Turn off the automatic upgrades of APT"
# FR-1111: the agent installs the host updates, by the update policy of the server and in its
# maintenance window. The APT timer must not install them at a different time.
cat >/etc/apt/apt.conf.d/52cloudfactory <<'APT'
// Managed by the Cloudfactory installer. The Cloudfactory agent installs the updates.
APT::Periodic::Unattended-Upgrade "0";
APT
# FR-1114: after an update, the agent runs needrestart, which restarts the services that use
# changed files. needrestart must not restart the agent, because a restart stops the update
# job. The agent restarts itself after its own update.
mkdir -p /etc/needrestart/conf.d
cat >/etc/needrestart/conf.d/50-cloudfactory.conf <<'CONF'
# Managed by the Cloudfactory installer.
$nrconf{override_rc}{qr(^cf-agent\.service$)} = 0;
CONF

step "Add the private service address"
# The website, database and panel containers publish their ports on this address on "lo", not
# on 127.0.0.1: rootless Podman cannot send a connection from 127.0.0.1 into gVisor. The
# address is link-local, so it is never on a real network. The agent has the same value
# (quadlet.ServiceAddress).
#
# The second address makes all addresses of the database network local addresses of this
# server. Each customer has its own database servers on one address of the network. The
# database servers see $DATABASE_CLIENT_ADDRESS as the address of each website container.
cat >/etc/systemd/system/cloudfactory-address.service <<UNIT
[Unit]
Description=Cloudfactory private service address
DefaultDependencies=no
After=systemd-networkd.service networking.service
Before=network.target user@.service cf-agent.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/sbin/ip address replace $SERVICE_ADDRESS/32 dev lo
ExecStart=/usr/sbin/ip address replace $DATABASE_CLIENT_ADDRESS/${DATABASE_NETWORK#*/} dev lo

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable --quiet cloudfactory-address.service
# A restart adds the addresses again. It is necessary when an earlier release of this script
# started the service with only the first address. The service has no stop command, so the
# addresses stay on "lo" during the restart.
systemctl restart cloudfactory-address.service

step "Let rootless containers use CPU, memory, I/O and process limits"
# FR-202: by default, systemd gives the user services only the memory and pids controllers.
# Podman needs the cpu and io controllers to apply the limits of each website.
mkdir -p /etc/systemd/system/user@.service.d
cat >/etc/systemd/system/user@.service.d/cloudfactory-delegate.conf <<'UNIT'
[Unit]
# The containers publish their ports on the private service address.
Wants=cloudfactory-address.service
After=cloudfactory-address.service

[Service]
Delegate=cpu cpuset io memory pids
UNIT
# The systemd package of Ubuntu has the file "timeout.conf" with "TimeoutStopSec=5" for the
# user services. When the server stops, systemd then stops the containers of each user with
# SIGKILL after 5 seconds, before a database server writes its data to the disk. systemd reads
# the drop-in files in the sequence of their names and the last value applies, so the name of
# this file must come after "timeout.conf". The agent writes the same file on a server of an
# earlier release (quadlet.UserStopFile).
cat >/etc/systemd/system/user@.service.d/zz-cloudfactory-stop.conf <<'UNIT'
[Service]
TimeoutStopSec=120
UNIT
systemctl daemon-reload

step "Hold the container packages"
# FR-1207: only the agent updates these packages (FR-1113).
apt-mark hold podman conmon netavark aardvark-dns passt crun >/dev/null

step "Install gVisor $GVISOR_VERSION"
# FR-1206: the official release archive, with a check of the SHA-512 checksum. runsc starts
# the Sentry from gvisor-bin, so the script keeps the whole archive in one directory.
dir="$GVISOR_ROOT/$GVISOR_VERSION"
download "$GVISOR_URL/$GVISOR_VERSION/$GVISOR_ARCH/gvisor.tar.bz2" "$TMP/gvisor.tar.bz2"
printf '%s  gvisor.tar.bz2\n' "$GVISOR_SHA512" >"$TMP/gvisor.tar.bz2.sha512"
(cd "$TMP" && sha512sum -c gvisor.tar.bz2.sha512 >/dev/null) || fail "the SHA-512 checksum of gvisor.tar.bz2 is not correct."
mkdir -p "$GVISOR_ROOT"
rm -rf "$dir.new"
mkdir -p "$dir.new"
tar -xjf "$TMP/gvisor.tar.bz2" -C "$dir.new" --no-same-owner --no-same-permissions
if [ ! -f "$dir.new/runsc" ] || [ ! -f "$dir.new/containerd-shim-runsc-v1" ]; then
	fail "gvisor.tar.bz2 has no runsc."
fi
chmod 0755 "$dir.new/runsc" "$dir.new/containerd-shim-runsc-v1"
rm -rf "$dir"
mv "$dir.new" "$dir"
# Keep the previous version for rollback.
if [ -L /usr/local/bin/runsc ] && [ "$(readlink /usr/local/bin/runsc)" != "$dir/runsc" ]; then
	ln -sfn "$(readlink /usr/local/bin/runsc)" /usr/local/bin/runsc.previous
fi
ln -sfn "$dir/runsc" /usr/local/bin/runsc

step "Install the AppArmor profiles"
# Spec section 6.4 and R-10: AppArmor stays on. These profiles let rootless Podman and gVisor
# use user namespaces on Ubuntu.
# AppArmor 4.0 and later has its own profile "podman" for /usr/bin/podman, with the same
# permissions. Two profiles for one program conflict, and AppArmor then runs Podman with no
# profile. Ubuntu then does not let rootless Podman use user namespaces, and Podman stops with
# "failed to reexec: Permission denied". Thus add a profile for Podman only if the system does
# not have one, and remove the profile of an earlier installation.
podman_profile=""
for file in /etc/apparmor.d/*; do
	[ -f "$file" ] && [ "$file" != /etc/apparmor.d/cloudfactory-podman ] || continue
	if grep -Eq '^[[:space:]]*profile[[:space:]]+[^[:space:]]+[[:space:]]+/usr/bin/podman([[:space:]]|$)' "$file"; then
		podman_profile=$file
	fi
done
apparmor_on=0
command -v apparmor_parser >/dev/null 2>&1 && [ -d /sys/kernel/security/apparmor ] && apparmor_on=1
if [ -n "$podman_profile" ]; then
	info "Podman uses the AppArmor profile $podman_profile."
	if [ -f /etc/apparmor.d/cloudfactory-podman ]; then
		[ "$apparmor_on" -eq 0 ] || apparmor_parser -R /etc/apparmor.d/cloudfactory-podman 2>/dev/null || true
		rm -f /etc/apparmor.d/cloudfactory-podman
	fi
else
	cat >/etc/apparmor.d/cloudfactory-podman <<'PROFILE'
abi <abi/4.0>,
include <tunables/global>
profile cloudfactory-podman /usr/bin/podman flags=(unconfined) {
  userns,
  include if exists <local/cloudfactory-podman>
}
PROFILE
fi
cat >/etc/apparmor.d/cloudfactory-runsc <<'PROFILE'
abi <abi/4.0>,
include <tunables/global>
profile cloudfactory-runsc /usr/local/lib/cloudfactory/gvisor/*/{runsc,gvisor-bin/*} flags=(unconfined) {
  userns,
  include if exists <local/cloudfactory-runsc>
}
PROFILE
if [ "$apparmor_on" -eq 1 ]; then
	apparmor_parser -r /etc/apparmor.d/cloudfactory-runsc
	[ -n "$podman_profile" ] || apparmor_parser -r /etc/apparmor.d/cloudfactory-podman
fi

step "Set the image signature policy"
mkdir -p "$CF_ETC" /etc/containers/registries.d
umask 022
printf '%s\n' "$COSIGN_PUBLIC_KEY" >"$CF_ETC/cosign.pub"
# FR-1101, CF-11: Podman pulls a Cloudfactory image only when its signature is valid. Podman
# refuses all other images, except from a registry that the administrator gave with
# --allow-unsigned. Images that are already in the local storage stay usable.
unsigned=""
for registry in $UNSIGNED_REGISTRIES; do
	unsigned="$unsigned
      \"$registry\": [{ \"type\": \"insecureAcceptAnything\" }],"
done
cat >/etc/containers/policy.json <<POLICY
{
  "default": [{ "type": "reject" }],
  "transports": {
    "docker": {$unsigned
      "$REGISTRY": [{ "type": "sigstoreSigned", "keyPath": "$CF_ETC/cosign.pub", "signedIdentity": { "type": "matchRepository" } }]
    },
    "containers-storage": { "": [{ "type": "insecureAcceptAnything" }] }
  }
}
POLICY
cat >/etc/containers/registries.d/cloudfactory.yaml <<REGISTRIES
docker:
  $REGISTRY:
    use-sigstore-attachments: true
REGISTRIES

step "Make the service users"
# Each service has its own Unix user with rootless containers (spec section 6.4).
for user in cf-edge cf-db cf-panel; do
	if ! id "$user" >/dev/null 2>&1; then
		useradd --system --create-home --home-dir "/var/lib/$user" --shell /usr/sbin/nologin --add-subids-for-system "$user"
	fi
	loginctl enable-linger "$user"
done
# The worker of the agent. The agent service starts as root, and starts its worker as this
# user. The worker has the agent port, the connection to the panel and the jobs. It is not
# root: the root part of the agent does only the operations of a fixed list for it. The user
# has no home directory, no shell and no containers. The agent makes the user on a server
# from an earlier release.
if ! id cf-agent >/dev/null 2>&1; then
	useradd --system --user-group --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin cf-agent
fi
install -d -m 0755 /srv/cloudfactory
# Root owns each directory in sites, so that SFTP can lock a user in it (ChrootDirectory).
# The home directories are separate, so that SFTP cannot reach them.
install -d -m 0711 /srv/cloudfactory/sites /srv/cloudfactory/homes
# Mode 0711: the service users cf-edge and cf-panel must go through this directory to their own
# data, and rootless Podman stops with "statfs ...: permission denied" if they cannot. Other
# users cannot list the directory. Each directory in it has mode 0700 and its own owner, and
# the agent makes its own directories (jobs, pki, dumps, restore) with mode 0700.
install -d -m 0711 "$CF_DATA"
install -d -o cf-edge -g cf-edge -m 0700 "$CF_DATA/caddy"
# CF-13: the admin API of Caddy listens on a Unix socket in this directory, not on a TCP port.
install -d -o cf-edge -g cf-edge -m 0700 "$CF_DATA/caddy-admin"
# Audit finding S2: the panel keeps one restic repository and one repository password for each
# server, and sends the password with each backup job. This script makes no key file.

step "Prepare the website users and SFTP"
# Spec section 10: the owner of a website signs in with SFTP as the Unix user of the website,
# with an SSH key only. The SFTP service is the container cf-sftp, on port $SFTP_PORT, not the
# SSH service of the host. The agent starts the container and writes the users and the keys.
# The script does not change the SSH service of the host. Port 22 is for the administrators.
getent group cf-sites >/dev/null || groupadd --system cf-sites
# All website users share one read-only copy of each runtime image. The agent pulls it as root
# and checks its signature. Only root can write the store, and only the website users can read
# it (agent package images). The agent uses the same path.
install -d -o root -g cf-sites -m 0750 /var/lib/cloudfactory-images
# The SSH keys and the user list of the SFTP container. Only root can write them.
install -d -m 0755 "$CF_ETC/sftp" "$CF_ETC/sftp-users"

step "Set the firewall"
# The agent writes the database access of each website in this directory (firewall_rules).
install -d -m 0755 "$CF_ETC/firewall"
firewall_rules >/etc/nftables.d-cloudfactory.nft
grep -q 'nftables.d-cloudfactory.nft' /etc/nftables.conf 2>/dev/null || printf 'include "/etc/nftables.d-cloudfactory.nft"\n' >>/etc/nftables.conf
nft -f /etc/nftables.d-cloudfactory.nft
systemctl enable --quiet nftables

step "Download the agent and check its signature"
# FR-1807: the agent binary from GitHub Releases, with a check of its signature.
asset="cf-agent_linux_$ARCH"
download "$RELEASE_URL/v$CF_VERSION/$asset" "$TMP/cf-agent"
download "$RELEASE_URL/v$CF_VERSION/$asset.sig" "$TMP/cf-agent.sig"
base64 -d "$TMP/cf-agent.sig" >"$TMP/cf-agent.sig.der"
openssl dgst -sha256 -verify "$CF_ETC/cosign.pub" -signature "$TMP/cf-agent.sig.der" "$TMP/cf-agent" >/dev/null ||
	fail "the signature of the agent binary is not valid."
# R-13: the signed manifest names the version and the hash of the binary, so an older signed
# binary cannot pass as this version.
download "$RELEASE_URL/v$CF_VERSION/$asset.manifest" "$TMP/cf-agent.manifest"
download "$RELEASE_URL/v$CF_VERSION/$asset.manifest.sig" "$TMP/cf-agent.manifest.sig"
base64 -d "$TMP/cf-agent.manifest.sig" >"$TMP/cf-agent.manifest.sig.der"
openssl dgst -sha256 -verify "$CF_ETC/cosign.pub" -signature "$TMP/cf-agent.manifest.sig.der" "$TMP/cf-agent.manifest" >/dev/null ||
	fail "the signature of the agent manifest is not valid."
agent_sha256=$(sha256sum "$TMP/cf-agent" | cut -d' ' -f1)
for line in "version=$CF_VERSION" "asset=$asset" "sha256=$agent_sha256"; do
	grep -qx "$line" "$TMP/cf-agent.manifest" ||
		fail "the agent binary does not agree with the signed manifest of version $CF_VERSION."
done
# When you run the script of a new release again, the agent service must start again with the
# new binary.
AGENT_CHANGED=0
cmp -s "$TMP/cf-agent" /usr/local/bin/cf-agent 2>/dev/null || AGENT_CHANGED=1
install -m 0755 "$TMP/cf-agent" /usr/local/bin/cf-agent

step "Install the agent service"
cat >/etc/systemd/system/cf-agent.service <<'UNIT'
[Unit]
Description=Cloudfactory agent
Documentation=https://github.com/itbm/cloudfactory
After=network-online.target nftables.service
Wants=network-online.target
# If the new agent does not start after an update, go back to the previous binary (FR-1812).
OnFailure=cf-agent-rollback.service
StartLimitIntervalSec=300
StartLimitBurst=5

[Service]
ExecStart=/usr/local/bin/cf-agent serve
Restart=always
RestartSec=5
# NFR-02: the agent itself uses less than 100 MB of memory. The programs that the agent starts,
# for example podman pull, restic and the database dumps, are in the cgroup of this service too.
# They need more memory: restic used 164 MiB for the backup of 5 GB in 79,000 files, and podman
# pull used 82 MiB for an image of 756 MB. This limit stops a memory leak, not normal work.
MemoryMax=1G

[Install]
WantedBy=multi-user.target
UNIT
cat >/etc/systemd/system/cf-agent-rollback.service <<'UNIT'
[Unit]
Description=Put back the previous Cloudfactory agent

[Service]
Type=oneshot
ExecStart=/usr/local/bin/cf-agent rollback
ExecStartPost=/bin/systemctl reset-failed cf-agent.service
ExecStartPost=/bin/systemctl start cf-agent.service
UNIT
systemctl daemon-reload

if [ "$MODE" = panel ]; then
	step "Start the panel in gVisor"
	install -d -o cf-panel -g cf-panel -m 0700 "$CF_DATA/panel" "$CF_DATA/panel/pki" "$CF_DATA/panel/pki/agents"
	if [ ! -f "$CF_DATA/panel/pki/panel.crt" ]; then
		# The client certificate that the panel sends to each agent (FR-1706).
		openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 3650 \
			-subj "/CN=$DOMAIN/O=Cloudfactory panel" \
			-keyout "$CF_DATA/panel/pki/panel.key" -out "$CF_DATA/panel/pki/panel.crt" 2>/dev/null
		chown cf-panel:cf-panel "$CF_DATA/panel/pki/panel.key" "$CF_DATA/panel/pki/panel.crt"
		chmod 0600 "$CF_DATA/panel/pki/panel.key"
	fi
	if [ ! -f "$CF_DATA/panel/panel.env" ]; then
		umask 077
		cat >"$CF_DATA/panel/panel.env" <<ENV
APP_KEY=base64:$(openssl rand -base64 32)
APP_URL=https://$DOMAIN
CF_AGENT_CLIENT_CERT=/data/pki/panel.crt
CF_AGENT_CLIENT_KEY=/data/pki/panel.key
CF_AGENT_CERTIFICATES=/data/pki/agents
CF_AGENT_LATEST=$CF_VERSION
ENV
		chown cf-panel:cf-panel "$CF_DATA/panel/panel.env"
		umask 022
	fi
	# FR-1808: the agent pulls the panel images and starts them in gVisor.
	# A new panel gets a certificate from its own CA. A panel that operates keeps its method,
	# unless you give --certificate.
	set -- --domain "$DOMAIN" --registry "$REGISTRY"
	[ -n "$CERTIFICATE" ] && set -- "$@" --certificate "$CERTIFICATE"
	/usr/local/bin/cf-agent panel install "$@"

	step "Make the first administrator"
	i=0
	# The API needs some time to start. An attempt that fails is not a fault, so curl does not
	# print it.
	until curl -fsS -o /dev/null "http://$SERVICE_ADDRESS:19080/up" 2>/dev/null; do
		i=$((i + 1))
		[ $i -lt 60 ] || fail "the panel API did not start."
		sleep 2
	done
	uid=$(id -u cf-panel)
	result=$(runuser -u cf-panel -- env XDG_RUNTIME_DIR="/run/user/$uid" \
		podman exec cf-panel-api php artisan cloudfactory:install "$EMAIL" --image-tag="$CF_VERSION")
	PASSWORD=$(printf '%s' "$result" | sed -n 's/.*"password":"\([^"]*\)".*/\1/p')
	JOIN_TOKEN=$(printf '%s' "$result" | sed -n 's/.*"joinToken":"\([^"]*\)".*/\1/p')
	[ -n "$JOIN_TOKEN" ] || fail "the panel did not give a join token."
	# The agent on the panel server reports to the HTTPS port of the API on the private service
	# address. That port has the certificate of the panel, which has no public name. Thus the
	# agent gets the fingerprint of the certificate here and accepts only that certificate.
	PANEL_URL="https://$SERVICE_ADDRESS:19443"
	PANEL_PIN=$(openssl x509 -in "$CF_DATA/panel/pki/panel.crt" -outform DER | sha256sum | cut -d' ' -f1)
	[ "${#PANEL_PIN}" -eq 64 ] || fail "could not read the fingerprint of $CF_DATA/panel/pki/panel.crt."
	# pasta maps this address in the panel containers to 127.0.0.1 on the host.
	[ -n "$ADDRESS" ] || ADDRESS=169.254.1.3
	[ -n "$ROLES" ] || ROLES=web,database,email,dns,backup
fi

step "Join the panel and start the agent"
if [ ! -f "$CF_ETC/agent.json" ]; then
	set -- --panel "$PANEL_URL" --token "$JOIN_TOKEN" --roles "${ROLES:-web}" --registry "$REGISTRY" --sftp-port "$SFTP_PORT"
	[ -n "$NAME" ] && set -- "$@" --name "$NAME"
	[ -n "$ADDRESS" ] && set -- "$@" --address "$ADDRESS"
	[ -n "$PANEL_PIN" ] && set -- "$@" --panel-certificate-sha256 "$PANEL_PIN"
	[ -n "$PANEL_CA_PIN" ] && set -- "$@" --panel-ca-sha256 "$PANEL_CA_PIN"
	/usr/local/bin/cf-agent join "$@"
fi
if [ "$AGENT_CHANGED" -eq 1 ] && systemctl is-active --quiet cf-agent.service; then
	# An update: the service still has the old binary.
	systemctl restart cf-agent.service
fi
systemctl enable --now --quiet cf-agent.service

CURRENT=""
info ""
if [ "$MODE" = panel ]; then
	info "The panel is installed. Open https://$DOMAIN"
	# The certificate method of the panel, and the root certificate of the panel CA.
	/usr/local/bin/cf-agent panel certificate || true
	if [ -n "${PASSWORD:-}" ]; then
		info "Sign in as $EMAIL with this password: $PASSWORD"
		info "This password is temporary. The panel asks you for a new password when you sign in."
		info "Then the panel asks you to set up an authenticator app (TOTP). Have the app ready."
		info "This password is also in your terminal history and in any log of this script."
	fi
else
	info "This server joined the panel. It shows in the panel under Servers."
fi
info "The website owners use SFTP on port $SFTP_PORT. If this server has a firewall at your provider, open this port there."
