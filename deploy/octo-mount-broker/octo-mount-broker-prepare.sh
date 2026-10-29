#!/bin/sh
set -eu

base=/srv/octo-managed-mounts
state=/var/lib/octo-mount-broker
run=/srv/octo-mount-broker/run
install -d -o root -g root -m 0755 "$base" "$run"
install -d -o root -g root -m 0700 "$state"

# Refuse to adopt an unrelated mount at the control path. The identity is
# recorded before the dedicated bind mount is first created.
identity=$(stat -c '%d:%i' "$base")
if [ -e "$state/base-identity" ]; then
  test "$(cat "$state/base-identity")" = "$identity" || { echo 'managed mount base identity changed' >&2; exit 1; }
else
  if mountpoint -q "$base"; then echo 'unrecognized managed mount base' >&2; exit 1; fi
  umask 077
  printf '%s\n' "$identity" > "$state/base-identity"
fi
if ! mountpoint -q "$base"; then
  mount --bind "$base" "$base"
fi
mount --make-rshared "$base"
