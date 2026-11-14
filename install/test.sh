#!/bin/sh
# The tests split $panel into options on purpose.
# shellcheck disable=SC2086
# Tests for install.sh. They run only the checks, with fake system files, so they do not
# change the system. Run: sh install/test.sh
set -u

HERE=$(cd "$(dirname "$0")" && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
PASS=0
FAIL=0

# A release copy of the script, with a version and a key.
sed -e 's/__CF_VERSION__/0.1.0/' -e 's/__GVISOR_VERSION__/20261019.0/' -e 's#__CF_REGISTRY__#ghcr.io/itbm#' \
	-e "s/__GVISOR_SHA512_X86_64__/$(printf '%0128d' 1)/" -e "s/__GVISOR_SHA512_AARCH64__/$(printf '%0128d' 2)/" \
	-e "s/__COSIGN_PUBLIC_KEY__/-----BEGIN PUBLIC KEY-----/" "$HERE/install.sh" >"$WORK/install.sh"

# fake makes a fake system: an OS, a CPU architecture, and the root user.
fake() {
	root="$WORK/root-$1-$2"
	mkdir -p "$root/etc" "$root/run/systemd/system" "$root/sys/fs/cgroup" "$root/proc" "$root/bin"
	printf 'ID=%s\nVERSION_ID="%s"\n' "$1" "$2" >"$root/etc/os-release"
	: >"$root/sys/fs/cgroup/cgroup.controllers"
	printf 'MemTotal:        4000000 kB\n' >"$root/proc/meminfo"
	printf '#!/bin/sh\necho %s\n' "${3:-x86_64}" >"$root/bin/uname"
	printf '#!/bin/sh\necho 0\n' >"$root/bin/id"
	printf '#!/bin/sh\necho %s\n' "${4:-localhost}" >"$root/bin/hostname"
	# The check of the connection to the panel: only panel.example.com answers.
	printf '#!/bin/sh\ncase "$*" in *https://panel.example.com/) exit 0 ;; *) exit 7 ;; esac\n' >"$root/bin/curl"
	chmod +x "$root/bin/uname" "$root/bin/id" "$root/bin/hostname" "$root/bin/curl"
	printf '%s' "$root"
}

# expect runs the checks and compares the exit status and a part of the output.
expect() {
	name=$1 want_status=$2 want_text=$3 root=$4
	shift 4
	out=$(CF_TEST_ROOT="$root" PATH="$root/bin:$PATH" sh "$WORK/install.sh" --check "$@" 2>&1)
	status=$?
	if [ "$status" -eq "$want_status" ] && printf '%s' "$out" | grep -qF -- "$want_text"; then
		PASS=$((PASS + 1))
	else
		FAIL=$((FAIL + 1))
		printf 'FAIL: %s (status %s, want %s)\n%s\n\n' "$name" "$status" "$want_status" "$out"
	fi
}

panel="--domain panel.example.com --email admin@example.com"

expect "Debian 13 passes" 0 "All checks passed." "$(fake debian 13)" $panel
expect "Ubuntu 26.04 on ARM64 passes" 0 "All checks passed." "$(fake ubuntu 26.04 aarch64)" $panel
expect "join mode passes" 0 "All checks passed." "$(fake debian 13)" --join cfj_abc --panel https://panel.example.com
expect "Debian 12 stops" 1 "Cloudfactory operates only on Debian 13 and Ubuntu 26.04 LTS" "$(fake debian 12)" $panel
expect "Ubuntu 24.04 stops" 1 "this operating system is ubuntu 24.04" "$(fake ubuntu 24.04)" $panel
expect "Rocky Linux stops" 1 "this operating system is rocky 9" "$(fake rocky 9)" $panel
expect "32-bit ARM stops" 1 "the CPU architecture armv7l is not supported" "$(fake debian 13 armv7l)" $panel
expect "no change after a failed check" 1 "The script did not change the system." "$(fake debian 12)" $panel
expect "join needs https" 1 "--join needs --panel with an https:// address." "$(fake debian 13)" --join cfj_abc --panel http://panel.example.com
expect "join stops when the panel does not answer" 1 "this server cannot connect to the panel at https://panel.example.net. Make sure that the address is correct" "$(fake debian 13)" --join cfj_abc --panel https://panel.example.net
expect "join stops for a panel on localhost" 1 "This address is a name for the computer itself" "$(fake debian 13)" --join cfj_abc --panel https://panel.localhost
expect "join stops for a panel on localhost with a port" 1 "This address is a name for the computer itself" "$(fake debian 13)" --join cfj_abc --panel https://panel.localhost:8443
expect "join stops before a change of the system" 1 "The script did not change the system." "$(fake debian 13)" --join cfj_abc --panel https://panel.example.net
expect "panel needs a domain" 1 "Give the domain of the panel with --domain" "$(fake debian 13)" --email admin@example.com
expect "no options: the host name is the domain" 0 "All checks passed." "$(fake debian 13 x86_64 panel.example.com)"
expect "option needs a value" 1 "the option --join needs a value." "$(fake debian 13)" --join
expect "unknown option" 1 "unknown option: --force" "$(fake debian 13)" --force

# A new panel gets a certificate from its own CA, so a local domain works.
expect "panel.localhost passes" 0 "All checks passed." "$(fake debian 13)" --domain panel.localhost --email admin@example.com
expect "localhost passes" 0 "All checks passed." "$(fake debian 13)" --domain localhost --email admin@example.com
expect "localhost needs an email address" 1 "give the email address of the first administrator with --email." "$(fake debian 13)" --domain localhost
expect "Let's Encrypt for a public domain passes" 0 "All checks passed." "$(fake debian 13)" $panel --certificate letsencrypt
expect "the internal CA passes" 0 "All checks passed." "$(fake debian 13)" --domain panel.localhost --email admin@example.com --certificate internal
for local in panel.localhost localhost panel.internal panel.test; do
	expect "Let's Encrypt for $local stops" 1 "Use --certificate internal for $local." "$(fake debian 13)" --domain "$local" --email admin@example.com --certificate letsencrypt
done
expect "an unknown certificate method stops" 1 "--certificate must be internal or letsencrypt." "$(fake debian 13)" $panel --certificate zerossl
expect "--certificate is not for --join" 1 "--certificate is only for a new panel" "$(fake debian 13)" --join cfj_abc --panel https://panel.example.com --certificate internal
pin=$(printf '%064d' 7)
expect "join with the fingerprint of the panel CA passes" 0 "All checks passed." "$(fake debian 13)" --join cfj_abc --panel https://panel.example.com --panel-ca-sha256 "$pin"
expect "a short fingerprint of the panel CA stops" 1 "--panel-ca-sha256 needs the SHA-256 fingerprint" "$(fake debian 13)" --join cfj_abc --panel https://panel.example.com --panel-ca-sha256 abc123
expect "a fingerprint of the panel CA that is not hex stops" 1 "--panel-ca-sha256 needs the SHA-256 fingerprint" "$(fake debian 13)" --join cfj_abc --panel https://panel.example.com --panel-ca-sha256 "$(printf '%063dz' 7)"
expect "--panel-ca-sha256 is only for --join" 1 "--panel-ca-sha256 is only for --join." "$(fake debian 13)" $panel --panel-ca-sha256 "$pin"
# shellcheck disable=SC2016 # The patterns are text of the script, not variables of this test.
if grep -q -- '--panel-ca-sha256 "$PANEL_CA_PIN"' "$HERE/install.sh" && grep -q -- '--certificate "$CERTIFICATE"' "$HERE/install.sh" &&
	grep -q 'caPin := fs.String("panel-ca-sha256"' "$HERE/../agent/cmd/cf-agent/main.go"; then
	PASS=$((PASS + 1))
else
	FAIL=$((FAIL + 1))
	echo 'FAIL: the agent does not get the fingerprint of the panel CA or the certificate method'
fi

expect "an unsigned registry is permitted" 0 "All checks passed." "$(fake debian 13)" $panel --allow-unsigned docker.io --allow-unsigned quay.io
expect "the release registry must stay signed" 1 "--allow-unsigned ghcr.io/itbm is not permitted." "$(fake debian 13)" $panel --allow-unsigned ghcr.io/itbm
expect "a registry name is checked" 1 'is not permitted.' "$(fake debian 13)" $panel --allow-unsigned 'docker.io"'

# CF-16: the script has a pinned gVisor checksum and does not download it.
sed 's/^GVISOR_SHA512_X86_64=.*/GVISOR_SHA512_X86_64='"'"'__GVISOR_SHA512_X86_64__'"'"'/' "$WORK/install.sh" >"$WORK/nosum.sh"
out=$(CF_TEST_ROOT="$(fake debian 13)" PATH="$WORK/root-debian-13/bin:$PATH" sh "$WORK/nosum.sh" --check $panel 2>&1)
if printf '%s' "$out" | grep -qF "this script has no gVisor checksum"; then PASS=$((PASS + 1)); else FAIL=$((FAIL + 1)); printf 'FAIL: no gVisor checksum\n%s\n' "$out"; fi
if grep -q 'gvisor.tar.bz2.sha512"' "$HERE/install.sh" && ! grep -q '^download .*\.sha512' "$HERE/install.sh"; then PASS=$((PASS + 1)); else FAIL=$((FAIL + 1)); echo 'FAIL: the checksum is downloaded'; fi

# CF-11: the signature policy refuses unknown images and is valid JSON.
policy() {
	{
		# shellcheck disable=SC2016 # $1 expands in policy.sh.
		echo 'set -eu; CF_ETC=/etc/cloudfactory; REGISTRY=ghcr.io/itbm; UNSIGNED_REGISTRIES="$1"'
		sed -n '/^unsigned=""/,/^POLICY$/p' "$HERE/install.sh" | sed "s#/etc/containers/policy.json#$WORK/policy.json#"
	} >"$WORK/policy.sh"
	sh "$WORK/policy.sh" "$1" && python3 -c 'import json,sys; p=json.load(open(sys.argv[1])); print(p["default"][0]["type"], sorted(p["transports"]["docker"]))' "$WORK/policy.json"
}
for case in "|reject ['ghcr.io/itbm']" "docker.io|reject ['docker.io', 'ghcr.io/itbm']"; do
	got=$(policy "${case%%|*}" 2>&1)
	if [ "$got" = "${case#*|}" ]; then PASS=$((PASS + 1)); else FAIL=$((FAIL + 1)); printf 'FAIL: policy %s: %s\n' "${case%%|*}" "$got"; fi
done

# The shared image store: only root writes it, and only the website users can read it.
if grep -q '^install -d -o root -g cf-sites -m 0750 /var/lib/cloudfactory-images$' "$HERE/install.sh" && grep -q 'fuse-overlayfs' "$HERE/install.sh"; then PASS=$((PASS + 1)); else FAIL=$((FAIL + 1)); echo 'FAIL: the shared image store'; fi
if grep -q 'DefaultRoot = "/var/lib/cloudfactory-images"' "$HERE/../agent/internal/images/images.go"; then PASS=$((PASS + 1)); else FAIL=$((FAIL + 1)); echo 'FAIL: the agent uses a different image store'; fi

# Spec section 10: SFTP for the websites is the container cf-sftp on its own port. The script
# does not change the SSH service of the host, and the agent finds the firewall rule by its comment.
expect "a different SFTP port" 0 "All checks passed." "$(fake debian 13)" $panel --sftp-port 2222
for bad in 22 7443 8443 19432 19443 25000 65536 abc 0; do
	expect "SFTP port $bad stops" 1 "--sftp-port $bad" "$(fake debian 13)" $panel --sftp-port "$bad"
done
# Audit B2: "cf-agent join" accepts http only for a loopback address. The agent on the panel
# server gets the HTTPS port of the API and the fingerprint of the panel certificate.
# shellcheck disable=SC2016 # The patterns are text of the script, not variables of this test.
if grep -q 'PANEL_URL="https://$SERVICE_ADDRESS:19443"' "$HERE/install.sh" && ! grep -q 'PANEL_URL="http://' "$HERE/install.sh" &&
	grep -q -- '--panel-certificate-sha256 "$PANEL_PIN"' "$HERE/install.sh"; then
	PASS=$((PASS + 1))
else
	FAIL=$((FAIL + 1))
	echo 'FAIL: the agent on the panel server does not get an HTTPS address with the panel certificate'
fi
# Audit B4: the panel keeps its data in PostgreSQL on port 19432 of the service address. The
# firewall lets only root and the user of the panel connect, and the rule for the edge proxy
# does not open the port for the edge proxy.
# shellcheck disable=SC2016 # The patterns are text of the script, not variables of this test.
if grep -q 'ip daddr $SERVICE_ADDRESS tcp dport 19432 meta skuid != { 0, $panel_uid } reject with tcp reset' "$HERE/install.sh" &&
	grep -q 'tcp dport != { 3306, 5432, 6379, 19432 } meta skuid != { 0, $edge_uid } reject with tcp reset' "$HERE/install.sh" &&
	grep -q 'PanelDatabasePort = 19432' "$HERE/../agent/internal/quadlet/quadlet.go"; then
	PASS=$((PASS + 1))
else
	FAIL=$((FAIL + 1))
	echo 'FAIL: the firewall rule of the panel database'
fi
if ! grep -Eq 'sshd_config|sshd -t|reload_ssh|systemctl (reload|restart) ssh' "$HERE/install.sh"; then PASS=$((PASS + 1)); else FAIL=$((FAIL + 1)); echo 'FAIL: the script changes the SSH service of the host'; fi
# shellcheck disable=SC2016 # The pattern finds the text $SFTP_PORT in the script.
if grep -q '^    tcp dport \$SFTP_PORT accept # cloudfactory-sftp$' "$HERE/install.sh" && grep -q 'sftpRuleMarker = "# cloudfactory-sftp"' "$HERE/../agent/internal/actions/sftp.go"; then PASS=$((PASS + 1)); else FAIL=$((FAIL + 1)); echo 'FAIL: the SFTP firewall rule'; fi
# shellcheck disable=SC2016 # The pattern finds the text $SFTP_PORT in the script.
if grep -q -- '--sftp-port "\$SFTP_PORT"' "$HERE/install.sh"; then PASS=$((PASS + 1)); else FAIL=$((FAIL + 1)); echo 'FAIL: the agent does not get the SFTP port'; fi

# Audit finding S1: the firewall limits the connections that a website container opens. The
# tests read the rules that the script prints, with fake user IDs.
firewall() {
	root=$(fake debian 13)
	# shellcheck disable=SC2016 # $* expands in the fake command.
	printf '#!/bin/sh\ncase "$*" in *cf-edge*) echo 991 ;; *cf-panel*) echo 992 ;; *) echo 0 ;; esac\n' >"$root/bin/id"
	CF_TEST_ROOT="$root" CF_TEST_PRINT=firewall PATH="$root/bin:$PATH" sh "$WORK/install.sh" --check $panel "$@" 2>&1 | sed '1,/^All checks passed\.$/d'
}
# rule checks that a file of rules has, or does not have, a line.
rule() {
	name=$1 want=$2 text=$3 file=$4
	if grep -qxF -- "    $text" "$file"; then got=yes; else got=no; fi
	if [ "$got" = "$want" ]; then PASS=$((PASS + 1)); else FAIL=$((FAIL + 1)); printf 'FAIL: %s\n' "$name"; fi
}
# line gives the line number of a rule, or 0.
line() { grep -nxF -- "    $1" "$2" | head -n 1 | cut -d: -f1 | grep . || echo 0; }

firewall >"$WORK/default.nft"
rule "the website users go to the website chain" yes 'ct state new meta skuid 20000-59999 jump websites' "$WORK/default.nft"
rule "a website reaches the shared databases" yes 'ip daddr 169.254.77.1 tcp dport { 3306, 5432, 6379 } accept' "$WORK/default.nft"
rule "a website reaches the DNS resolver" yes 'meta l4proto { tcp, udp } th dport 53 accept' "$WORK/default.nft"
rule "a website does not reach the services of the server" yes 'fib daddr type { local, broadcast, multicast } jump refuse' "$WORK/default.nft"
rule "a website does not reach link-local and private networks" yes 'ip daddr { 0.0.0.0/8, 10.0.0.0/8, 100.64.0.0/10, 127.0.0.0/8, 168.63.129.16, 169.254.0.0/16, 172.16.0.0/12, 192.168.0.0/16, 224.0.0.0/3 } jump refuse' "$WORK/default.nft"
rule "a website does not reach private IPv6 networks" yes 'ip6 daddr { ::1, fc00::/7, fe80::/10, ff00::/8 } jump refuse' "$WORK/default.nft"
rule "a website does not send email directly" yes 'tcp dport 25 jump refuse' "$WORK/default.nft"
# A browser on this server opens https://panel.localhost on port 443. The connection does not
# go through the prerouting chain, so the output chain of the NAT table redirects it.
rule "local connections to port 80 go to the edge proxy" yes 'fib daddr type local tcp dport 80 redirect to :8080' "$WORK/default.nft"
rule "local connections to port 443 go to the edge proxy" yes 'fib daddr type local tcp dport 443 redirect to :8443' "$WORK/default.nft"
rule "local HTTP/3 connections go to the edge proxy" yes 'fib daddr type local udp dport 443 redirect to :8443' "$WORK/default.nft"
rule "the redirects of local connections are in the NAT table" yes 'type nat hook output priority -100;' "$WORK/default.nft"
# pasta closes a connection from a loopback address, because gVisor does not listen on the
# loopback interface of the container. The redirected connections from 127.0.0.0/8 get the
# service address as their source. A connection from ::1 is not redirected, so it is refused.
rule "local connections from a loopback address get the service address" yes 'ip saddr 127.0.0.0/8 ct status dnat meta l4proto { tcp, udp } th dport { 8080, 8443 } snat ip to 169.254.77.1' "$WORK/default.nft"
rule "the source address changes in the NAT table" yes 'type nat hook postrouting priority 100;' "$WORK/default.nft"
rule "local connections from ::1 are not redirected" yes 'ip6 saddr ::1 return' "$WORK/default.nft"
if [ "$(line 'ip6 saddr ::1 return' "$WORK/default.nft")" -lt "$(line 'fib daddr type local tcp dport 80 redirect to :8080' "$WORK/default.nft")" ]; then PASS=$((PASS + 1)); else FAIL=$((FAIL + 1)); echo 'FAIL: the rule for ::1 comes after the redirects'; fi
rule "the panel database rule has the user of the panel" yes 'ct state new ip daddr 169.254.77.1 tcp dport 19432 meta skuid != { 0, 992 } reject with tcp reset' "$WORK/default.nft"
# The rule for the ports of the other websites comes before the website chain, and the
# databases come before the rule that closes the addresses of the server.
other=$(line 'ct state new ip daddr 169.254.77.1 tcp dport != { 3306, 5432, 6379, 19432 } meta skuid != { 0, 991 } reject with tcp reset' "$WORK/default.nft")
jump=$(line 'ct state new meta skuid 20000-59999 jump websites' "$WORK/default.nft")
databases=$(line 'ip daddr 169.254.77.1 tcp dport { 3306, 5432, 6379 } accept' "$WORK/default.nft")
local_rule=$(line 'fib daddr type { local, broadcast, multicast } jump refuse' "$WORK/default.nft")
if [ "$other" -gt 0 ] && [ "$other" -lt "$jump" ] && [ "$databases" -gt 0 ] && [ "$databases" -lt "$local_rule" ]; then PASS=$((PASS + 1)); else FAIL=$((FAIL + 1)); echo 'FAIL: the order of the website firewall rules'; fi

firewall --allow-website-smtp --allow-website-network 10.0.5.0/24 --allow-website-network fd00:1::/64 --allow-website-network 192.168.7.9 >"$WORK/allowed.nft"
rule "--allow-website-smtp opens port 25" no 'tcp dport 25 jump refuse' "$WORK/allowed.nft"
rule "--allow-website-network opens an IPv4 network" yes 'ip daddr 10.0.5.0/24 accept' "$WORK/allowed.nft"
rule "--allow-website-network opens an IPv6 network" yes 'ip6 daddr fd00:1::/64 accept' "$WORK/allowed.nft"
rule "--allow-website-network opens one address" yes 'ip daddr 192.168.7.9 accept' "$WORK/allowed.nft"
rule "the other private networks stay closed" yes 'fib daddr type { local, broadcast, multicast } jump refuse' "$WORK/allowed.nft"
if [ "$(line 'ip daddr 10.0.5.0/24 accept' "$WORK/allowed.nft")" -lt "$(line 'fib daddr type { local, broadcast, multicast } jump refuse' "$WORK/allowed.nft")" ]; then PASS=$((PASS + 1)); else FAIL=$((FAIL + 1)); echo 'FAIL: a permitted network comes after the rule that closes it'; fi
for bad in example.com 10.0.0.0/8/1 0.0.0.0/0 '10.0.0.1;drop' 10.0.0.1/ fd00::1.2 1234; do
	expect "website network $bad stops" 1 "--allow-website-network $bad is not an IP address or a network." "$(fake debian 13)" $panel --allow-website-network "$bad"
done
# nftables checks the syntax of the rules, when the test runs as root on a system with nft.
if command -v nft >/dev/null 2>&1 && printf 'table inet cloudfactory_test\n' | nft -c -f - 2>/dev/null; then
	for file in default allowed; do
		if nft -c -f "$WORK/$file.nft"; then PASS=$((PASS + 1)); else FAIL=$((FAIL + 1)); printf 'FAIL: nft refuses the %s firewall rules\n' "$file"; fi
	done
else
	echo 'Note: nft cannot run here, so nftables did not check the syntax of the firewall rules.'
fi
# The agent makes the website users in the UID range of the firewall, and it does not start a
# website as a user that is not in the range.
if grep -q '^WEBSITE_UID_MIN=20000$' "$HERE/install.sh" && grep -q '^WEBSITE_UID_MAX=59999$' "$HERE/install.sh" &&
	grep -q 'WebsiteIDMin = 20000$' "$HERE/../agent/internal/host/host.go" && grep -q 'WebsiteIDMax = 59999$' "$HERE/../agent/internal/host/host.go" &&
	grep -q '"UID_MIN="+min, "-K", "UID_MAX="+max' "$HERE/../agent/internal/host/host.go"; then
	PASS=$((PASS + 1))
else
	FAIL=$((FAIL + 1))
	echo 'FAIL: the agent and the firewall use different UID ranges for the website users'
fi
# Audit finding R9: needrestart restarts the services after an update, but not the agent.
# shellcheck disable=SC2016 # The pattern is text of the script, not a variable of this test.
if grep -q '^cat >/etc/needrestart/conf.d/50-cloudfactory.conf' "$HERE/install.sh" && grep -qF '$nrconf{override_rc}{qr(^cf-agent\.service$)} = 0;' "$HERE/install.sh" &&
	! grep -q '"needrestart", "-r", "a", "-b"' "$HERE/../agent/internal/hostupdate/hostupdate.go"; then
	PASS=$((PASS + 1))
else
	FAIL=$((FAIL + 1))
	echo 'FAIL: the needrestart settings'
fi

# The step counter agrees with the steps: all steps for --join, and two more for a new panel.
# The panel steps are in an "if" block, so they have a tab before "step".
common=$(grep -c '^step "' "$HERE/install.sh")
panel_only=$(grep -c "^	step \"" "$HERE/install.sh")
if grep -qx "STEPS=$common" "$HERE/install.sh" && grep -qxF "[ \"\$MODE\" = panel ] && STEPS=$((common + panel_only))" "$HERE/install.sh"; then
	PASS=$((PASS + 1))
else
	FAIL=$((FAIL + 1))
	printf 'FAIL: STEPS does not agree with the %d steps and the %d panel steps\n' "$common" "$panel_only"
fi

root=$(fake debian 13)
rm "$root/sys/fs/cgroup/cgroup.controllers"
expect "cgroup v2 is necessary" 1 "cgroup v2 is not mounted" "$root" $panel

root=$(fake debian 13)
printf 'MemTotal:        900000 kB\n' >"$root/proc/meminfo"
expect "memory is checked" 1 "Cloudfactory needs" "$root" $panel

# The unchanged script (with no release values) must stop.
out=$(CF_TEST_ROOT="$(fake debian 13)" PATH="$WORK/root-debian-13/bin:$PATH" sh "$HERE/install.sh" --check $panel 2>&1)
if printf '%s' "$out" | grep -qF "this script has no release version"; then PASS=$((PASS + 1)); else FAIL=$((FAIL + 1)); printf 'FAIL: unreleased script\n%s\n' "$out"; fi

printf '%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
