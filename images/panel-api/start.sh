#!/bin/sh
# Prepares the database, then serves the API. The queue worker uses the same image with
# a different command.
set -eu
mkdir -p /data /data/pki/agents
# Development only: SQLite keeps the database in one file. A server uses PostgreSQL.
if [ "${DB_CONNECTION:-pgsql}" = sqlite ]; then
	[ -f "${DB_DATABASE:-/data/panel.sqlite}" ] || touch "${DB_DATABASE:-/data/panel.sqlite}"
fi
# The database container can start after this container, for example at boot. A migration
# is one transaction in PostgreSQL, so it is safe to try again.
tries=0
until php artisan migrate --force --no-interaction; do
	tries=$((tries + 1))
	if [ "$tries" -ge 30 ]; then
		echo "The database is not available, or a migration failed." >&2
		exit 1
	fi
	sleep 2
done
php artisan config:cache
php artisan route:cache
# With the certificate of the panel, the API also has an HTTPS port for the agent on the panel
# server. The install script makes the certificate before the first start.
config=/etc/frankenphp/Caddyfile
cert=${CF_AGENT_CLIENT_CERT:-/data/pki/panel.crt}
key=${CF_AGENT_CLIENT_KEY:-/data/pki/panel.key}
if [ -s "$cert" ] && [ -s "$key" ]; then
	export CF_AGENT_CLIENT_CERT="$cert" CF_AGENT_CLIENT_KEY="$key"
	config=/etc/frankenphp/Caddyfile.tls
else
	echo "The panel has no certificate at $cert. The API starts with no HTTPS port." >&2
fi
exec frankenphp run --config "$config" --adapter caddyfile
