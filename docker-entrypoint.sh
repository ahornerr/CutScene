#!/bin/sh
# Prepare the durable storage volume, then run CutScene unprivileged.
#
# Releases before the privilege drop ran as root and left the storage volume
# owned by root with 0700 permissions. Starting unprivileged against such a
# volume fails immediately: the service cannot create its session database,
# and cannot chmod the storage root. The image-layer chown does not help
# because a mounted volume shadows it.
#
# So: while still root, adopt any root-owned storage root, then drop to the
# unprivileged user for the lifetime of the process.
set -eu

STORAGE_ROOT="${CUTSCENE_STORAGE_ROOT:-/data}"
CUTSCENE_UID="${CUTSCENE_UID:-10001}"
CUTSCENE_GID="${CUTSCENE_GID:-10001}"

# Migrate ownership only when the directory exists and is not already ours.
# Chowning an already-correct volume is unnecessary and touching a
# non-directory (misconfiguration) must not be destructive.
if [ -d "$STORAGE_ROOT" ]; then
    current_owner="$(stat -c '%u:%g' "$STORAGE_ROOT" 2>/dev/null || echo "unknown")"
    if [ "$current_owner" != "$CUTSCENE_UID:$CUTSCENE_GID" ]; then
        echo "cutscene: adopting storage volume $STORAGE_ROOT (was $current_owner)" >&2
        # -R so pre-existing clips and the clip database are adopted too.
        chown -R "$CUTSCENE_UID:$CUTSCENE_GID" "$STORAGE_ROOT"
    fi
else
    mkdir -p "$STORAGE_ROOT"
    chown "$CUTSCENE_UID:$CUTSCENE_GID" "$STORAGE_ROOT"
fi

if [ "$(id -u)" = "0" ]; then
    # Drop to the unprivileged user while KEEPING the supplementary groups the
    # container was started with (notably the host `render` gid added by
    # docker-compose.gpu.yaml via group_add).
    #
    # setpriv --init-groups must NOT be used here: it re-reads /etc/group for
    # the target uid and discards any group injected with --group-add, which
    # would leave VAAPI renders unable to open /dev/dri/renderD128.
    # Collect the current supplementary gids explicitly and pass them through.
    extra_groups="$(id -G | tr ' ' ',')"
    exec setpriv --reuid="$CUTSCENE_UID" --regid="$CUTSCENE_GID" --groups="$extra_groups" /cutscene "$@"
fi

# Already unprivileged (for example when an operator overrides the user);
# ownership was migrated above only if this process had the privilege to.
exec /cutscene "$@"