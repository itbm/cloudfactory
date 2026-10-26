#!/bin/sh
# Starts PHP-FPM and Apache. If one of them stops, the container stops, and systemd starts it again.
set -eu
mkdir -p /tmp/apache
php-fpm --nodaemonize &
fpm=$!
apache2 -DFOREGROUND &
web=$!
trap 'kill -TERM $fpm $web 2>/dev/null' TERM INT
while kill -0 $fpm 2>/dev/null && kill -0 $web 2>/dev/null; do
	sleep 2
done
kill -TERM $fpm $web 2>/dev/null || true
exit 1
