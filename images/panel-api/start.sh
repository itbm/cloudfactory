#!/bin/sh
# Prepares the database, then serves the API. The queue worker uses the same image with
# a different command.
set -eu
mkdir -p /data /data/pki/agents
[ -f "${DB_DATABASE:-/data/panel.sqlite}" ] || touch "${DB_DATABASE:-/data/panel.sqlite}"
php artisan migrate --force --no-interaction
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
