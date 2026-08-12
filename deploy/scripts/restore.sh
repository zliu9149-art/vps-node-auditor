#!/usr/bin/env sh
# Restore SQLite atomically and recover the original database and collector
# state if any step fails.
set -eu

ROOT=${VNA_TEST_ROOT:-}
case "$ROOT" in
    "") ;;
    /*) ROOT=${ROOT%/} ;;
    *) echo "VNA_TEST_ROOT must be an absolute path" >&2; exit 2 ;;
esac
[ "$ROOT" != "/" ] || { echo "VNA_TEST_ROOT must not be /" >&2; exit 2; }
BACKUP=${1:-}
DATABASE=${2:-$ROOT/var/lib/vps-node-auditor/audit.db}
CONFIG=${3:-$ROOT/etc/vps-node-auditor/config.json}
MAINTENANCE="$ROOT/usr/local/libexec/vps-node-auditor/node-audit-maintenance"
COLLECTOR=node-audit-collector.service
DATABASE_MUTATED=false
COLLECTOR_WAS_ACTIVE=false
ROLLBACK=""

[ "$(id -u)" -eq 0 ] || { echo "must run as root" >&2; exit 1; }
[ -f "$BACKUP" ] || { echo "backup file does not exist" >&2; exit 1; }
[ -f "$CONFIG" ] || { echo "auditor config does not exist" >&2; exit 1; }
[ -x "$MAINTENANCE" ] || { echo "maintenance binary does not exist" >&2; exit 1; }

install -d -m 0755 "$ROOT/run/vps-node-auditor"
exec 9>"$ROOT/run/vps-node-auditor/config.lock"
flock -n 9 || { echo "another operation holds the global lock" >&2; exit 1; }
export VNA_OPERATIONS_LOCK_HELD=1

# Validate the selected artifact itself before stopping the writer or changing
# any production file.
"$MAINTENANCE" --config "$CONFIG" integrity --database "$BACKUP"

systemctl is-active --quiet "$COLLECTOR" 2>/dev/null && COLLECTOR_WAS_ACTIVE=true
install -d -m 0700 "$ROOT/var/backups/vps-node-auditor"
ROLLBACK=$(mktemp -d "$ROOT/var/backups/vps-node-auditor/pre-restore-XXXXXXXXXX")
chmod 0700 "$ROLLBACK"

# A consistent logical backup is retained for operators, while exact files
# preserve the database, WAL/SHM, ownership, and modes for automatic rollback.
"$MAINTENANCE" --config "$CONFIG" backup --dir "$ROLLBACK"
restore_original() {
    original_status=$?
    trap - EXIT HUP INT TERM
    set +e
    rollback_ok=true
    if [ "$DATABASE_MUTATED" = true ]; then
        systemctl stop "$COLLECTOR" >/dev/null 2>&1 || rollback_ok=false
        rm -f "$DATABASE" "$DATABASE-wal" "$DATABASE-shm" "$DATABASE.restore.tmp" || rollback_ok=false
        for suffix in "" -wal -shm; do
            [ ! -e "$ROLLBACK/current.db$suffix" ] || \
                cp -a "$ROLLBACK/current.db$suffix" "$DATABASE$suffix" || rollback_ok=false
        done
        [ -e "$DATABASE" ] || rollback_ok=false
        "$MAINTENANCE" --config "$CONFIG" integrity --database "$DATABASE" >/dev/null 2>&1 || rollback_ok=false
    fi
    if [ "$COLLECTOR_WAS_ACTIVE" = true ]; then
        systemctl start "$COLLECTOR" >/dev/null 2>&1 || rollback_ok=false
        systemctl is-active --quiet "$COLLECTOR" >/dev/null 2>&1 || rollback_ok=false
    else
        systemctl stop "$COLLECTOR" >/dev/null 2>&1 || rollback_ok=false
        systemctl is-active --quiet "$COLLECTOR" >/dev/null 2>&1 && rollback_ok=false
    fi
    if [ "$rollback_ok" = true ]; then
        echo "restore failed; original database and collector state recovered from $ROLLBACK" >&2
    else
        echo "restore failed and automatic rollback could not be proven; inspect $ROLLBACK immediately" >&2
    fi
    exit "$original_status"
}

trap restore_original EXIT HUP INT TERM
systemctl stop "$COLLECTOR"
for suffix in "" -wal -shm; do
    [ ! -e "$DATABASE$suffix" ] || cp -a "$DATABASE$suffix" "$ROLLBACK/current.db$suffix"
done

DATABASE_MUTATED=true
rm -f "$DATABASE-wal" "$DATABASE-shm" "$DATABASE.restore.tmp"
install -m 0600 "$BACKUP" "$DATABASE.restore.tmp"
directory_owner=$(stat -c %u "$(dirname "$DATABASE")")
directory_group=$(stat -c %g "$(dirname "$DATABASE")")
chown "$directory_owner:$directory_group" "$DATABASE.restore.tmp"
mv "$DATABASE.restore.tmp" "$DATABASE"
chmod 0600 "$DATABASE"

"$MAINTENANCE" --config "$CONFIG" integrity --database "$DATABASE"
if [ "$COLLECTOR_WAS_ACTIVE" = true ]; then
    systemctl start "$COLLECTOR"
    systemctl is-active --quiet "$COLLECTOR"
else
    systemctl stop "$COLLECTOR" >/dev/null 2>&1 || true
    if systemctl is-active --quiet "$COLLECTOR" >/dev/null 2>&1; then
        echo "collector remained active after restore" >&2
        false
    fi
fi

DATABASE_MUTATED=false
trap - EXIT HUP INT TERM
echo "database restore completed; pre-restore state is in $ROLLBACK"
