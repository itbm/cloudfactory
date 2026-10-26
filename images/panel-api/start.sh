#!/bin/sh
# Prepares the database, then serves the API. The queue worker uses the same image with
# a different command.
set -eu
mkdir -p /data /data/pki/agents
[ -f "${DB_DATABASE:-/data/panel.sqlite}" ] || touch "${DB_DATABASE:-/data/panel.sqlite}"
php artisan migrate --force --no-interaction
php artisan config:cache
php artisan route:cache
exec frankenphp php-server --listen :8080 --root /app/public
