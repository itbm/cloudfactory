#!/bin/sh
# Starts the SFTP service. The host keys are in /etc/ssh/keys, which the agent mounts from
# the host, so that they stay the same when the container starts again.
set -eu
umask 077
for type in ed25519 ecdsa rsa; do
	key="/etc/ssh/keys/ssh_host_${type}_key"
	if [ ! -s "$key" ]; then
		rm -f "$key" "$key.pub"
		ssh-keygen -q -t "$type" -N '' -C "cloudfactory-sftp" -f "$key"
		chmod 0644 "$key.pub"
	fi
done
# The privilege separation directory. /run is a tmpfs.
install -d -m 0755 /run/sshd
umask 022
sshd -t
exec /usr/sbin/sshd -D -e
