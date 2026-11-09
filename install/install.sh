#!/bin/sh
# Cloudfactory installation script (spec section 7.18).
#
# Install a new panel (FR-1802):
#   curl -fsSL https://github.com/itbm/cloudfactory/releases/latest/download/install.sh | sh
#   curl -fsSL https://github.com/itbm/cloudfactory/releases/latest/download/install.sh | sh -s -- --domain panel.example.com --email you@example.com
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
DOMAIN=""
EMAIL=""
NAME=""
ROLES=""
ADDRESS=""
PANEL_IP=""
CHECK_ONLY=0
UNSIGNED_REGISTRIES=""

usage() {
	cat <<'USAGE'
Usage:
  install.sh [--domain DOMAIN] [--email EMAIL]        Install a new panel on this server.
                                                      The default domain is the full host name.
  install.sh --join TOKEN --panel URL                 Add this server to a panel.

Options:
  --name NAME          The name of this server in the panel. The default is the host name.
  --roles ROLES        The roles of this server: web, database, email, dns, backup.
                       The default is "web" for --join, and all roles for a new panel.
  --address HOST       The host name or IP address that the panel uses to reach this server.
  --panel-ip IP        Accept the agent port only from this IP address (the panel).
  --registry URL       Use a different image registry, for example a mirror (FR-1813).
  --allow-unsigned REGISTRY
                       Let Podman pull images with no signature from REGISTRY, for example
                       docker.io. Podman refuses all other unsigned images. You can give
                       this option more than one time.
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
	--domain) DOMAIN=$(value "$@"); shift ;;
	--email) EMAIL=$(value "$@"); shift ;;
	--name) NAME=$(value "$@"); shift ;;
	--roles) ROLES=$(value "$@"); shift ;;
	--address) ADDRESS=$(value "$@"); shift ;;
	--panel-ip) PANEL_IP=$(value "$@"); shift ;;
	--registry) REGISTRY=$(value "$@"); shift ;;
	--allow-unsigned) UNSIGNED_REGISTRIES="$UNSIGNED_REGISTRIES $(value "$@")"; shift ;;
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
		;;
	panel)
		if [ -z "$DOMAIN" ]; then
			DOMAIN=$(hostname -f 2>/dev/null || true)
			case "$DOMAIN" in
			*.*) ;;
			*) DOMAIN="" ;;
			esac
		fi
		[ -n "$DOMAIN" ] || problem "the host name is not a full domain. Give the domain of the panel with --domain, for example --domain panel.example.com."
		[ -n "$EMAIL" ] || EMAIL="admin@$DOMAIN"
		case "$EMAIL" in
		*@*.*) ;;
		*) problem "give the email address of the first administrator with --email." ;;
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
	for registry in $UNSIGNED_REGISTRIES; do
		case "$registry" in
		*[!A-Za-z0-9.:/_-]* | "$REGISTRY" | "$REGISTRY"/*) problem "--allow-unsigned $registry is not permitted." ;;
		esac
	done
}

check_ports() {
	[ "$MODE" = panel ] || return 0
	command -v ss >/dev/null 2>&1 || return 0
	for port in 80 443; do
		if ss -Hltn "sport = :$port" 2>/dev/null | grep -q .; then
			problem "port $port is in use. Stop the service that uses it."
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
check_ports
check_resources

if [ -n "$ERRORS" ]; then
	printf 'The checks found these problems. The script did not change the system.\n%s' "$ERRORS" >&2
	exit 1
fi
info "All checks passed."
if [ "$CHECK_ONLY" -eq 1 ]; then
	exit 0
fi

# ---------------------------------------------------------------------------
# Steps. Each step is idempotent, so you can run the script again after a failure.
# ---------------------------------------------------------------------------

STEP=0
STEPS=13
[ "$MODE" = panel ] && STEPS=15
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
CF_ETC=/etc/cloudfactory
CF_DATA=/var/lib/cloudfactory
GVISOR_ROOT=/usr/local/lib/cloudfactory/gvisor
# The private address of the container ports on "lo" (quadlet.ServiceAddress in the agent).
SERVICE_ADDRESS=169.254.77.1

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

step "Add the private service address"
# The website, database and panel containers publish their ports on this address on "lo", not
# on 127.0.0.1: rootless Podman cannot send a connection from 127.0.0.1 into gVisor. The
# address is link-local, so it is never on a real network. The agent has the same value
# (quadlet.ServiceAddress).
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

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable --now --quiet cloudfactory-address.service

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
cat >/etc/apparmor.d/cloudfactory-podman <<'PROFILE'
abi <abi/4.0>,
include <tunables/global>
profile cloudfactory-podman /usr/bin/podman flags=(unconfined) {
  userns,
  include if exists <local/cloudfactory-podman>
}
PROFILE
cat >/etc/apparmor.d/cloudfactory-runsc <<'PROFILE'
abi <abi/4.0>,
include <tunables/global>
profile cloudfactory-runsc /usr/local/lib/cloudfactory/gvisor/*/{runsc,gvisor-bin/*} flags=(unconfined) {
  userns,
  include if exists <local/cloudfactory-runsc>
}
PROFILE
if command -v apparmor_parser >/dev/null 2>&1 && [ -d /sys/kernel/security/apparmor ]; then
	apparmor_parser -r /etc/apparmor.d/cloudfactory-podman /etc/apparmor.d/cloudfactory-runsc
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
install -d -m 0755 /srv/cloudfactory
# Root owns each directory in sites, so that SFTP can lock a user in it (ChrootDirectory).
# The home directories are separate, so that SFTP cannot reach them.
install -d -m 0711 /srv/cloudfactory/sites /srv/cloudfactory/homes
install -d -m 0700 "$CF_DATA"
install -d -o cf-edge -g cf-edge -m 0700 "$CF_DATA/caddy"
# CF-13: the admin API of Caddy listens on a Unix socket in this directory, not on a TCP port.
install -d -o cf-edge -g cf-edge -m 0700 "$CF_DATA/caddy-admin"
# The panel keeps one restic repository password for all servers and sends it with each
# backup job. Do not make a key for each server: two keys cannot open one repository.

step "Set up SFTP for the websites"
# Spec section 10: the owner of a website signs in with SFTP as the Unix user of the website.
# sshd locks the user in the directory of the website and allows only SFTP with an SSH key.
# Passwords are refused. The agent writes the keys of each user. Other users of the server
# are not changed.
getent group cf-sites >/dev/null || groupadd --system cf-sites
# All website users share one read-only copy of each runtime image. The agent pulls it as root
# and checks its signature. Only root can write the store, and only the website users can read
# it (agent package images). The agent uses the same path.
install -d -o root -g cf-sites -m 0750 /var/lib/cloudfactory-images
install -d -m 0755 "$CF_ETC/sftp"
mkdir -p /etc/ssh/sshd_config.d
cat >/etc/ssh/sshd_config.d/50-cloudfactory-sftp.conf <<'SSHD'
# Managed by the Cloudfactory installer.
Match Group cf-sites
	ChrootDirectory /srv/cloudfactory/sites/%u
	ForceCommand internal-sftp -d /app -u 0027
	AuthorizedKeysFile /etc/cloudfactory/sftp/%u.keys
	AuthenticationMethods publickey
	PasswordAuthentication no
	KbdInteractiveAuthentication no
	AllowTcpForwarding no
	AllowAgentForwarding no
	AllowStreamLocalForwarding no
	PermitTunnel no
	PermitTTY no
	PermitUserRC no
	X11Forwarding no
SSHD
# Debian and Ubuntu read sshd_config.d at the top of sshd_config. Check the configuration
# before the reload, so that a mistake does not lock out the administrator.
grep -Eq '^[[:space:]]*Include[[:space:]]+/etc/ssh/sshd_config\.d/\*\.conf' /etc/ssh/sshd_config ||
	fail "/etc/ssh/sshd_config does not include /etc/ssh/sshd_config.d/*.conf. Add the line \"Include /etc/ssh/sshd_config.d/*.conf\" at the top, then run the script again."
sshd -t || fail "the SSH configuration is not valid. Read the error above."
systemctl reload ssh.service 2>/dev/null || systemctl reload sshd.service

step "Set the firewall"
# FR-1705: block all incoming traffic except the published ports and the agent port.
# Spec section 15, question 7: rootless containers cannot open ports below 1024, so the
# firewall redirects 80 and 443 to the high ports of the edge proxy. The client address does
# not change (FR-1707).
agent_rule="tcp dport 7443 accept"
edge_uid=$(id -u cf-edge)
[ -n "$PANEL_IP" ] && agent_rule="ip saddr $PANEL_IP tcp dport 7443 accept"
cat >/etc/nftables.d-cloudfactory.nft <<NFT
table inet cloudfactory
delete table inet cloudfactory
table inet cloudfactory {
  chain input {
    type filter hook input priority filter; policy drop;
    ct state established,related accept
    iif lo accept
    meta l4proto { icmp, ipv6-icmp } accept
    tcp dport 22 accept
    tcp dport { 80, 443, 8080, 8443 } accept
    udp dport { 443, 8443 } accept
    $agent_rule
  }
  chain output {
    type filter hook output priority filter; policy accept;
    # FR-1703: only root (the agent) and the edge proxy can connect to the website and panel
    # ports on the private service address. pasta opens each connection of a container as the
    # Unix user of the container, so a website cannot reach a different website. All websites
    # can reach the shared database ports. The rule checks only new connections, because the
    # answers of the containers go to the address too.
    ct state new ip daddr $SERVICE_ADDRESS tcp dport != { 3306, 5432 } meta skuid != { 0, $edge_uid } reject with tcp reset
  }
  chain prerouting {
    type nat hook prerouting priority dstnat;
    tcp dport 80 redirect to :8080
    tcp dport 443 redirect to :8443
    udp dport 443 redirect to :8443
  }
}
NFT
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
# NFR-02: the agent uses less than 100 MB of memory.
MemoryMax=150M

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
	/usr/local/bin/cf-agent panel install --domain "$DOMAIN" --registry "$REGISTRY"

	step "Make the first administrator"
	i=0
	until curl -fsS -o /dev/null "http://$SERVICE_ADDRESS:19080/up"; do
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
	# The agent on the panel server reports directly to the API on the private service address.
	PANEL_URL="http://$SERVICE_ADDRESS:19080"
	# pasta maps this address in the panel containers to 127.0.0.1 on the host.
	[ -n "$ADDRESS" ] || ADDRESS=169.254.1.3
	[ -n "$ROLES" ] || ROLES=web,database,email,dns,backup
fi

step "Join the panel and start the agent"
if [ ! -f "$CF_ETC/agent.json" ]; then
	set -- --panel "$PANEL_URL" --token "$JOIN_TOKEN" --roles "${ROLES:-web}" --registry "$REGISTRY"
	[ -n "$NAME" ] && set -- "$@" --name "$NAME"
	[ -n "$ADDRESS" ] && set -- "$@" --address "$ADDRESS"
	/usr/local/bin/cf-agent join "$@"
fi
systemctl enable --now --quiet cf-agent.service

CURRENT=""
info ""
if [ "$MODE" = panel ]; then
	info "The panel is installed. Open https://$DOMAIN"
	info "Point the DNS of $DOMAIN to this server, so that the panel can get a TLS certificate."
	if [ -n "${PASSWORD:-}" ]; then
		info "Sign in as $EMAIL with this password: $PASSWORD"
		info "After you sign in, open Settings, change this password and set up an authenticator app (TOTP)."
		info "This password is also in your terminal history and in any log of this script."
	fi
else
	info "This server joined the panel. It shows in the panel under Servers."
fi
