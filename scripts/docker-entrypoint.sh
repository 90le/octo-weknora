#!/bin/bash
set -e

# ─── Fix ownership of bind-mounted directories ───
# When users bind-mount host directories (e.g. ./data/files),
# the mount inherits the host UID/GID which may differ from the
# container's appuser. This entrypoint runs as root, fixes ownership,
# then drops privileges to appuser via gosu — the same pattern used
# by official postgres/redis images.

# Directories that may be bind-mounted and need appuser access
MOUNT_DIRS=(
    /data/files
)

for dir in "${MOUNT_DIRS[@]}"; do
    if [ -d "$dir" ]; then
        chown -R appuser:appuser "$dir" 2>/dev/null || true
    fi
done

# TMPDIR may be a deployment-provided directory under the writable file
# storage. Ensure it exists for multipart uploads and local source staging,
# without changing permissions on the shared system /tmp directory.
if [ -n "${TMPDIR:-}" ] && [ "$TMPDIR" != "/tmp" ]; then
    case "$TMPDIR" in
        /*) gosu appuser mkdir -p -m 700 -- "$TMPDIR" || echo "weknora: configured temporary directory is unavailable" >&2 ;;
        *) echo "weknora: TMPDIR must be an absolute directory" >&2 ;;
    esac
fi

# ─── Docker socket access for the sandbox backend ───
# The Engine API socket is typically root:docker 0660. This process then
# drops to appuser via gosu, which calls initgroups and therefore drops
# compose group_add. Match the socket's GID in /etc/group before gosu.
# Never chmod the host socket: that would weaken daemon access on the host.
grant_docker_sock_to_appuser() {
    local sock="$1"
    local gid grp
    [ -S "$sock" ] || return 0
    if gosu appuser sh -c "test -r \"$sock\" && test -w \"$sock\"" 2>/dev/null; then
        return 0
    fi
    gid="$(stat -c '%g' "$sock" 2>/dev/null || true)"
    if [ -z "$gid" ]; then
        echo "weknora: cannot stat $sock; Docker sandbox may be unable to reach the daemon" >&2
        return 0
    fi
    if [ "$gid" = "0" ]; then
        echo "weknora: $sock is not writable by appuser and owned by GID 0; Docker sandbox needs a group-writable socket with a non-root GID" >&2
        return 0
    fi
    if ! getent group "$gid" >/dev/null 2>&1; then
        if ! groupadd -g "$gid" dockersock >/dev/null 2>&1; then
            echo "weknora: failed to create group for $sock GID $gid; Docker sandbox may be unable to reach the daemon" >&2
            return 0
        fi
    fi
    grp="$(getent group "$gid" | cut -d: -f1)"
    if [ -z "$grp" ]; then
        echo "weknora: no group name for GID $gid on $sock" >&2
        return 0
    fi
    if ! usermod -aG "$grp" appuser >/dev/null 2>&1; then
        echo "weknora: failed to add appuser to $grp for $sock; Docker sandbox may be unable to reach the daemon" >&2
        return 0
    fi
}

grant_docker_sock_to_appuser /var/run/docker.sock
case "${DOCKER_HOST:-}" in
    unix://*)
        grant_docker_sock_to_appuser "${DOCKER_HOST#unix://}"
        ;;
esac

# The host folder broker uses its own non-root GID and a private Unix socket.
# gosu calls initgroups, so grant that group inside this container before
# dropping privileges. This does not change host socket permissions.
grant_mount_broker_group_to_appuser() {
    local sock="$1" gid grp
    [ -S "$sock" ] || return 0
    gid="$(stat -c '%g' "$sock" 2>/dev/null || true)"
    if [ -z "$gid" ] || [ "$gid" = "0" ]; then
        echo "weknora: mount broker socket has no dedicated group" >&2
        return 0
    fi
    if ! getent group "$gid" >/dev/null 2>&1; then
        groupadd -g "$gid" octomount >/dev/null 2>&1 || return 0
    fi
    grp="$(getent group "$gid" | cut -d: -f1)"
    [ -n "$grp" ] && usermod -aG "$grp" appuser >/dev/null 2>&1 || true
}
if [ -n "${OCTO_MOUNT_BROKER_SOCKET:-}" ]; then
    grant_mount_broker_group_to_appuser "$OCTO_MOUNT_BROKER_SOCKET"
fi

# ─── Drop privileges and exec the main process ───
exec gosu appuser "$@"
