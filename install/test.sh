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
	chmod +x "$root/bin/uname" "$root/bin/id" "$root/bin/hostname"
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
expect "panel needs a domain" 1 "Give the domain of the panel with --domain" "$(fake debian 13)" --email admin@example.com
expect "no options: the host name is the domain" 0 "All checks passed." "$(fake debian 13 x86_64 panel.example.com)"
expect "option needs a value" 1 "the option --join needs a value." "$(fake debian 13)" --join
expect "unknown option" 1 "unknown option: --force" "$(fake debian 13)" --force

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
if ! grep -Eq 'sshd_config|sshd -t|reload_ssh|systemctl (reload|restart) ssh' "$HERE/install.sh"; then PASS=$((PASS + 1)); else FAIL=$((FAIL + 1)); echo 'FAIL: the script changes the SSH service of the host'; fi
# shellcheck disable=SC2016 # The pattern finds the text $SFTP_PORT in the script.
if grep -q '^    tcp dport \$SFTP_PORT accept # cloudfactory-sftp$' "$HERE/install.sh" && grep -q 'sftpRuleMarker = "# cloudfactory-sftp"' "$HERE/../agent/internal/actions/sftp.go"; then PASS=$((PASS + 1)); else FAIL=$((FAIL + 1)); echo 'FAIL: the SFTP firewall rule'; fi
# shellcheck disable=SC2016 # The pattern finds the text $SFTP_PORT in the script.
if grep -q -- '--sftp-port "\$SFTP_PORT"' "$HERE/install.sh"; then PASS=$((PASS + 1)); else FAIL=$((FAIL + 1)); echo 'FAIL: the agent does not get the SFTP port'; fi

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
