#!/bin/sh
# The contract test of the example MySQL pack (cf-database/1, docs/packs.md). It starts the
# image as the agent starts a database server, then runs each operation of /cf/manage and
# connects as a website user.
#
#   plugins/packs/mysql/test.sh IMAGE
#
# CONTAINER selects the container program: docker (the default) or podman.
# SPDX-License-Identifier: AGPL-3.0-only
set -eu

image=${1:?give the image, for example cf-mysql:test}
engine=${CONTAINER:-docker}
name=cf-mysql-test-$$
volume=$name-data
port=${PORT:-20000}
superuser=$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')
password=Website$(od -An -N12 -tx1 /dev/urandom | tr -d ' \n')

cleanup() {
	"$engine" rm -f "$name" >/dev/null 2>&1 || true
	"$engine" volume rm -f "$volume" >/dev/null 2>&1 || true
}
trap cleanup EXIT

fail() {
	echo "FAIL: $*" >&2
	"$engine" logs "$name" 2>&1 | tail -n 40 >&2 || true
	exit 1
}

# manage OPERATION [INPUT]: runs one operation, as the agent does.
manage() {
	input=${2:-}
	[ -n "$input" ] || input='{}'
	printf '%s\n' "$input" | "$engine" exec -i "$name" /cf/manage "$1"
}

# expect OPERATION INPUT ANSWER: the operation must give the answer.
expect() {
	got=$(manage "$1" "$2") || fail "$1 $2: exit status $?"
	[ "$got" = "$3" ] || fail "$1 $2: got $got, want $3"
}

# refuse OPERATION INPUT: the operation must fail.
refuse() {
	if manage "$1" "$2" >/dev/null 2>&1; then
		fail "$1 $2: no error"
	fi
}

# client USER PASSWORD DATABASE SQL: a website connects on the published port.
client() {
	"$engine" run --rm --network host --entrypoint mysql "$image" --no-defaults --protocol=tcp --host=127.0.0.1 --port="$port" \
		--user="$1" --password="$2" --batch --skip-column-names --database="$3" --execute="$4" 2>/dev/null
}

# The settings of quadlet.DatabaseUnit: the user 999, a read-only root file system, no
# capabilities, a tmpfs on /tmp and the data directory of the pack.
start() {
	"$engine" run -d --name "$name" --user 999:999 --read-only --tmpfs /tmp --cap-drop=all --security-opt no-new-privileges \
		-v "$volume:/var/lib/mysql" -p "127.0.0.1:$port:3306" -e "CF_SUPERUSER_PASSWORD=$superuser" "$image" >/dev/null
}

wait_ready() {
	for _ in $(seq 1 90); do
		if [ "$(manage ready 2>/dev/null)" = '{"ready":true}' ]; then
			return 0
		fi
		sleep 2
	done
	fail "the server is not ready after 180 seconds"
}

start
# The first start makes the data directory. Until the final server listens, the answer is false.
wait_ready
echo "ok: ready"

describe=$(manage describe) || fail "describe"
printf '%s\n' "$describe" | grep -q '"contract":"cf-database/1"' || fail "describe: $describe"
printf '%s\n' "$describe" | grep -q '"version":"8\.' || fail "describe: $describe"
echo "ok: describe $describe"

expect create-database '{"name":"shop_a1b2_app"}' '{}'
refuse create-database '{"name":"shop_a1b2_app"}'
expect create-database '{"name":"shop_a1b2_logs"}' '{}'
# A name that the underscore in a grant would match: the user must not get access to it.
expect create-database '{"name":"shopxa1b2xapp"}' '{}'
echo "ok: create-database"

expect set-user "{\"user\":\"shop_a1b2_app\",\"password\":\"$password\",\"databases\":[{\"name\":\"shop_a1b2_app\",\"access\":\"readwrite\"}]}" '{}'
client shop_a1b2_app "$password" shop_a1b2_app "CREATE TABLE notes (id INT PRIMARY KEY, body VARCHAR(40)); INSERT INTO notes VALUES (1, 'first');" ||
	fail "the website user cannot write"
[ "$(client shop_a1b2_app "$password" shop_a1b2_app 'SELECT body FROM notes WHERE id = 1')" = first ] || fail "the website user cannot read"
if client shop_a1b2_app "$password" shopxa1b2xapp 'SELECT 1' >/dev/null; then
	fail "the grant for shop_a1b2_app gives access to shopxa1b2xapp"
fi
if client root "$superuser" shop_a1b2_app 'SELECT 1' >/dev/null; then
	fail "root can connect on TCP"
fi
echo "ok: set-user readwrite"

# No password keeps the password. The list replaces the databases of the user.
expect set-user '{"user":"shop_a1b2_app","databases":[{"name":"shop_a1b2_app","access":"readonly"},{"name":"shop_a1b2_logs","access":"readwrite"}]}' '{}'
[ "$(client shop_a1b2_app "$password" shop_a1b2_app 'SELECT COUNT(*) FROM notes')" = 1 ] || fail "the read-only user cannot read"
if client shop_a1b2_app "$password" shop_a1b2_app "INSERT INTO notes VALUES (2, 'second')" >/dev/null; then
	fail "the read-only user can write"
fi
client shop_a1b2_app "$password" shop_a1b2_logs 'CREATE TABLE entries (id INT)' || fail "the user cannot write to its second database"
refuse set-user '{"user":"shop_a1b2_new","databases":[]}'
echo "ok: set-user readonly"

sizes=$(manage sizes) || fail "sizes"
printf '%s\n' "$sizes" | grep -Eq '\{"name":"shop_a1b2_app","bytes":[1-9][0-9]*\}' || fail "sizes: $sizes"
printf '%s\n' "$sizes" | grep -q '"name":"shop_a1b2_logs"' || fail "sizes: $sizes"
printf '%s\n' "$sizes" | grep -q '"name":"mysql"' && fail "sizes has the database mysql: $sizes"
echo "ok: sizes $sizes"

dump=$(mktemp)
trap 'rm -f "$dump"; cleanup' EXIT
manage dump '{"name":"shop_a1b2_app"}' >"$dump" || fail "dump"
grep -q 'CREATE TABLE .notes.' "$dump" || fail "the dump has no table"
if grep -q 'CREATE DATABASE' "$dump"; then
	fail "the dump has a database name"
fi
echo "ok: dump"

# A restore into a different database: a staging copy.
expect create-database '{"name":"shop_c3d4_app"}' '{}'
{ echo '{"name":"shop_c3d4_app"}'; cat "$dump"; } | "$engine" exec -i "$name" /cf/manage restore >/dev/null || fail "restore"
expect set-user "{\"user\":\"shop_c3d4_app\",\"password\":\"$password\",\"databases\":[{\"name\":\"shop_c3d4_app\",\"access\":\"readwrite\"}]}" '{}'
[ "$(client shop_c3d4_app "$password" shop_c3d4_app 'SELECT body FROM notes WHERE id = 1')" = first ] || fail "the copy has no data"
# A restore replaces the data, and the users keep their access.
client shop_c3d4_app "$password" shop_c3d4_app "INSERT INTO notes VALUES (3, 'third')" || fail "the copy is not writable"
{ echo '{"name":"shop_c3d4_app"}'; cat "$dump"; } | "$engine" exec -i "$name" /cf/manage restore >/dev/null || fail "restore"
[ "$(client shop_c3d4_app "$password" shop_c3d4_app 'SELECT COUNT(*) FROM notes')" = 1 ] || fail "the restore did not replace the data"
echo "ok: restore"

# The access of the users goes with the database: a new database with the same name is empty
# for them.
expect drop-database '{"name":"shop_c3d4_app"}' '{}'
expect create-database '{"name":"shop_c3d4_app"}' '{}'
if client shop_c3d4_app "$password" shop_c3d4_app 'SELECT 1' >/dev/null; then
	fail "the user has access to a new database with the name of a removed database"
fi
expect drop-database '{"name":"shop_c3d4_app"}' '{}'
expect drop-database '{"name":"shop_c3d4_app"}' '{}'
manage sizes | grep -q '"name":"shop_c3d4_app"' && fail "drop-database did not remove the database"
echo "ok: drop-database"

expect drop-user '{"user":"shop_c3d4_app"}' '{}'
expect drop-user '{"user":"shop_c3d4_app"}' '{}'
if client shop_c3d4_app "$password" shop_a1b2_app 'SELECT 1' >/dev/null; then
	fail "a removed user can connect"
fi
echo "ok: drop-user"

refuse create-database '{"name":"shop; DROP DATABASE mysql"}'
refuse set-user '{"user":"root","password":"Abcdefghij0123456789","databases":[]}'
refuse backup '{}'
echo "ok: refusals"

# The data stays after a restart. The password of root comes from the data directory.
"$engine" restart "$name" >/dev/null
wait_ready
[ "$(client shop_a1b2_app "$password" shop_a1b2_app 'SELECT COUNT(*) FROM notes')" = 1 ] || fail "the data is not there after a restart"
echo "ok: restart"
echo "All operations of cf-database/1 passed."
