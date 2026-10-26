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
