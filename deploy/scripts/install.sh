#!/usr/bin/env sh
# Transactional installer/upgrader for AlmaLinux production nodes.
set -eu

PROJECT_DIR=$(unset CDPATH; cd -- "$(dirname -- "$0")/../.." && pwd)
DIST_DIR="$PROJECT_DIR/dist/linux-amd64"
CONFIG_SOURCE=""
PROVISION_SOURCE=""
ROOT=${VNA_TEST_ROOT:-}
case "$ROOT" in
    "") ;;
    /*) ROOT=${ROOT%/} ;;
    *) echo "VNA_TEST_ROOT must be an absolute path" >&2; exit 2 ;;
esac
[ "$ROOT" != "/" ] || { echo "VNA_TEST_ROOT must not be /" >&2; exit 2; }
LOCK_PATH="$ROOT/run/vps-node-auditor/config.lock"
ETC_DIR="$ROOT/etc/vps-node-auditor"
SYSTEMD_DIR="$ROOT/etc/systemd/system"
LIBEXEC_DIR="$ROOT/usr/local/libexec/vps-node-auditor"
LOCAL_BIN="$ROOT/usr/local/bin"
USR_BIN="$ROOT/usr/bin"
LOCAL_SBIN="$ROOT/usr/local/sbin"
STATE_DIR="$ROOT/var/lib/vps-node-auditor"
BACKUP_ROOT="$ROOT/var/backups/vps-node-auditor"

while [ "$#" -gt 0 ]; do
    case "$1" in
        --dist-dir) [ "$#" -ge 2 ] || { echo "--dist-dir requires a path" >&2; exit 2; }; DIST_DIR=$2; shift 2 ;;
        --config) [ "$#" -ge 2 ] || { echo "--config requires a path" >&2; exit 2; }; CONFIG_SOURCE=$2; shift 2 ;;
        --provision-config) [ "$#" -ge 2 ] || { echo "--provision-config requires a path" >&2; exit 2; }; PROVISION_SOURCE=$2; shift 2 ;;
        *) echo "unknown argument: $1" >&2; exit 2 ;;
    esac
done

[ "$(id -u)" -eq 0 ] || { echo "must run as root" >&2; exit 1; }
for binary in vna node-audit node-audit-collector node-audit-maintenance node-provision; do
    [ -f "$DIST_DIR/$binary" ] || { echo "missing build artifact: $DIST_DIR/$binary" >&2; exit 1; }
done
[ -z "$CONFIG_SOURCE" ] || [ -f "$CONFIG_SOURCE" ] || { echo "auditor config source does not exist" >&2; exit 1; }
[ -z "$PROVISION_SOURCE" ] || [ -f "$PROVISION_SOURCE" ] || { echo "provision config source does not exist" >&2; exit 1; }

install -d -m 0755 "$ROOT/run/vps-node-auditor"
exec 9>"$LOCK_PATH"
flock -n 9 || { echo "another operation holds the global lock" >&2; exit 1; }
export VNA_OPERATIONS_LOCK_HELD=1

collector_active=false
collector_enabled=false
collector_present=false
maintenance_timer_active=false
backup_timer_active=false
observe_timer_active=false
maintenance_timer_enabled=false
backup_timer_enabled=false
observe_timer_enabled=false
maintenance_timer_present=false
backup_timer_present=false
observe_timer_present=false
DATABASE_ROLLBACK=""
DATABASE_FILES_SAVED=false
vnstat_active=false
vnstat_enabled=false
vnstat_present=false
vnstat_package_present=false
sysstat_active=false
sysstat_enabled=false
sysstat_present=false
sysstat_package_present=false
systemctl is-active --quiet node-audit-collector.service 2>/dev/null && collector_active=true
systemctl is-enabled --quiet node-audit-collector.service 2>/dev/null && collector_enabled=true
systemctl is-active --quiet node-audit-maintenance.timer 2>/dev/null && maintenance_timer_active=true
systemctl is-active --quiet node-audit-backup.timer 2>/dev/null && backup_timer_active=true
systemctl is-active --quiet node-audit-observe.timer 2>/dev/null && observe_timer_active=true
systemctl is-enabled --quiet node-audit-maintenance.timer 2>/dev/null && maintenance_timer_enabled=true
systemctl is-enabled --quiet node-audit-backup.timer 2>/dev/null && backup_timer_enabled=true
systemctl is-enabled --quiet node-audit-observe.timer 2>/dev/null && observe_timer_enabled=true
load_state=$(systemctl show --property=LoadState --value vnstat.service 2>/dev/null || true)
if [ "$load_state" != "not-found" ] && [ -n "$load_state" ]; then
    vnstat_present=true
    systemctl is-active --quiet vnstat.service 2>/dev/null && vnstat_active=true
    systemctl is-enabled --quiet vnstat.service 2>/dev/null && vnstat_enabled=true
fi
rpm -q vnstat >/dev/null 2>&1 && vnstat_package_present=true
load_state=$(systemctl show --property=LoadState --value sysstat.service 2>/dev/null || true)
if [ "$load_state" != "not-found" ] && [ -n "$load_state" ]; then
    sysstat_present=true
    systemctl is-active --quiet sysstat.service 2>/dev/null && sysstat_active=true
    systemctl is-enabled --quiet sysstat.service 2>/dev/null && sysstat_enabled=true
fi
rpm -q sysstat >/dev/null 2>&1 && sysstat_package_present=true

install -d -o root -g root -m 0700 "$BACKUP_ROOT"
ROLLBACK_DIR=$(mktemp -d "$BACKUP_ROOT/install-XXXXXXXXXX")
chmod 0700 "$ROLLBACK_DIR"
MANIFEST="$ROLLBACK_DIR/managed-files.txt"
: >"$MANIFEST"
for path in \
    "$LIBEXEC_DIR" \
    "$LOCAL_BIN/vna" "$LOCAL_BIN/node-audit" "$LOCAL_BIN/node-provision" \
    "$USR_BIN/vna" "$USR_BIN/node-audit" "$USR_BIN/node-provision" \
    "$LOCAL_SBIN/node-audit-backup" "$LOCAL_SBIN/node-audit-restore" \
    "$ETC_DIR/config.json" "$ETC_DIR/provision.json" \
    "$SYSTEMD_DIR/node-audit-collector.service" \
    "$SYSTEMD_DIR/multi-user.target.wants/node-audit-collector.service" \
    "$SYSTEMD_DIR/node-audit-maintenance.service" "$SYSTEMD_DIR/node-audit-maintenance.timer" \
    "$SYSTEMD_DIR/timers.target.wants/node-audit-maintenance.timer" \
    "$SYSTEMD_DIR/node-audit-backup.service" "$SYSTEMD_DIR/node-audit-backup.timer" \
    "$SYSTEMD_DIR/timers.target.wants/node-audit-backup.timer" \
    "$SYSTEMD_DIR/node-audit-observe.service" "$SYSTEMD_DIR/node-audit-observe.timer" \
    "$SYSTEMD_DIR/timers.target.wants/node-audit-observe.timer"; do
    [ ! -e "$path" ] && [ ! -L "$path" ] || printf '%s\n' "$path" >>"$MANIFEST"
done
[ ! -s "$MANIFEST" ] || tar -cpf "$ROLLBACK_DIR/managed-files.tar" -P -T "$MANIFEST"
grep -Fxq "$SYSTEMD_DIR/node-audit-collector.service" "$MANIFEST" && collector_present=true
grep -Fxq "$SYSTEMD_DIR/node-audit-maintenance.timer" "$MANIFEST" && maintenance_timer_present=true
grep -Fxq "$SYSTEMD_DIR/node-audit-backup.timer" "$MANIFEST" && backup_timer_present=true
grep -Fxq "$SYSTEMD_DIR/node-audit-observe.timer" "$MANIFEST" && observe_timer_present=true

ROLLBACK_ARMED=true
rollback_install() {
    status=$?
    trap - EXIT HUP INT TERM
    set +e
    rollback_ok=true
    if [ "$ROLLBACK_ARMED" = true ]; then
        if ! systemctl stop node-audit-collector.service >/dev/null 2>&1 && [ "$collector_present" = true ]; then
            rollback_ok=false
        fi
        rm -rf "$LIBEXEC_DIR" || rollback_ok=false
        rm -f "$LOCAL_BIN/vna" "$LOCAL_BIN/node-audit" "$LOCAL_BIN/node-provision" \
            "$USR_BIN/vna" "$USR_BIN/node-audit" "$USR_BIN/node-provision" \
            "$LOCAL_SBIN/node-audit-backup" "$LOCAL_SBIN/node-audit-restore" \
            "$SYSTEMD_DIR/node-audit-collector.service" \
            "$SYSTEMD_DIR/multi-user.target.wants/node-audit-collector.service" \
            "$SYSTEMD_DIR/node-audit-maintenance.service" "$SYSTEMD_DIR/node-audit-maintenance.timer" \
            "$SYSTEMD_DIR/timers.target.wants/node-audit-maintenance.timer" \
            "$SYSTEMD_DIR/node-audit-backup.service" "$SYSTEMD_DIR/node-audit-backup.timer" \
            "$SYSTEMD_DIR/timers.target.wants/node-audit-backup.timer" \
            "$SYSTEMD_DIR/node-audit-observe.service" "$SYSTEMD_DIR/node-audit-observe.timer" \
            "$SYSTEMD_DIR/timers.target.wants/node-audit-observe.timer" || rollback_ok=false
        if ! grep -Fxq "$ETC_DIR/config.json" "$MANIFEST"; then
            rm -f "$ETC_DIR/config.json" || rollback_ok=false
        fi
        if ! grep -Fxq "$ETC_DIR/provision.json" "$MANIFEST"; then
            rm -f "$ETC_DIR/provision.json" || rollback_ok=false
        fi
        [ ! -f "$ROLLBACK_DIR/managed-files.tar" ] || tar -xpf "$ROLLBACK_DIR/managed-files.tar" -P || rollback_ok=false
        systemctl daemon-reload || rollback_ok=false
        if [ "$DATABASE_FILES_SAVED" = true ]; then
            database="$STATE_DIR/audit.db"
            rm -f "$database" "$database-wal" "$database-shm" "$database.install.tmp" || rollback_ok=false
            for suffix in "" -wal -shm; do
                [ ! -e "$ROLLBACK_DIR/database-files/audit.db$suffix" ] || \
                    cp -a "$ROLLBACK_DIR/database-files/audit.db$suffix" "$database$suffix" || rollback_ok=false
            done
            [ -e "$database" ] || rollback_ok=false
            VNA_OPERATIONS_LOCK_HELD=1 "$DIST_DIR/node-audit-maintenance" \
                --config "$ETC_DIR/config.json" integrity --database "$database" \
                >/dev/null 2>&1 || rollback_ok=false
        fi
        if [ "$collector_present" = true ]; then
            if [ "$collector_enabled" = true ]; then
                systemctl enable node-audit-collector.service >/dev/null 2>&1 || rollback_ok=false
            else
                systemctl disable node-audit-collector.service >/dev/null 2>&1 || rollback_ok=false
            fi
            if [ "$collector_active" = true ]; then
                systemctl start node-audit-collector.service >/dev/null 2>&1 || rollback_ok=false
                systemctl is-active --quiet node-audit-collector.service >/dev/null 2>&1 || rollback_ok=false
            else
                systemctl stop node-audit-collector.service >/dev/null 2>&1 || rollback_ok=false
                systemctl is-active --quiet node-audit-collector.service >/dev/null 2>&1 && rollback_ok=false
            fi
        fi
        for timer_state in \
            "node-audit-maintenance.timer:$maintenance_timer_present:$maintenance_timer_active:$maintenance_timer_enabled" \
            "node-audit-backup.timer:$backup_timer_present:$backup_timer_active:$backup_timer_enabled" \
            "node-audit-observe.timer:$observe_timer_present:$observe_timer_active:$observe_timer_enabled"; do
            timer=${timer_state%%:*}
            remainder=${timer_state#*:}
            present_state=${remainder%%:*}
            remainder=${remainder#*:}
            active_state=${remainder%%:*}
            enabled_state=${remainder#*:}
            if [ "$present_state" != true ]; then
                continue
            fi
            if [ "$enabled_state" = true ]; then
                systemctl enable "$timer" >/dev/null 2>&1 || rollback_ok=false
            else
                systemctl disable "$timer" >/dev/null 2>&1 || rollback_ok=false
            fi
            if [ "$active_state" = true ]; then
                systemctl start "$timer" >/dev/null 2>&1 || rollback_ok=false
                systemctl is-active --quiet "$timer" >/dev/null 2>&1 || rollback_ok=false
            else
                systemctl stop "$timer" >/dev/null 2>&1 || rollback_ok=false
                systemctl is-active --quiet "$timer" >/dev/null 2>&1 && rollback_ok=false
            fi
        done
        for auxiliary_state in \
            "vnstat:$vnstat_present:$vnstat_active:$vnstat_enabled" \
            "sysstat:$sysstat_present:$sysstat_active:$sysstat_enabled"; do
            auxiliary=${auxiliary_state%%:*}
            remainder=${auxiliary_state#*:}
            present_state=${remainder%%:*}
            remainder=${remainder#*:}
            active_state=${remainder%%:*}
            enabled_state=${remainder#*:}
            if [ "$present_state" = true ] && [ "$enabled_state" = true ]; then
                systemctl enable "$auxiliary.service" >/dev/null 2>&1 || rollback_ok=false
            else
                systemctl disable "$auxiliary.service" >/dev/null 2>&1 || rollback_ok=false
            fi
            if [ "$present_state" = true ] && [ "$active_state" = true ]; then
                systemctl start "$auxiliary.service" >/dev/null 2>&1 || rollback_ok=false
                systemctl is-active --quiet "$auxiliary.service" >/dev/null 2>&1 || rollback_ok=false
            else
                systemctl stop "$auxiliary.service" >/dev/null 2>&1 || rollback_ok=false
                systemctl is-active --quiet "$auxiliary.service" >/dev/null 2>&1 && rollback_ok=false
            fi
        done
        packages_to_remove=""
        [ "$vnstat_package_present" = true ] || packages_to_remove="$packages_to_remove vnstat"
        [ "$sysstat_package_present" = true ] || packages_to_remove="$packages_to_remove sysstat"
        if [ -n "$packages_to_remove" ]; then
            # These packages were introduced by this failed installation, so
            # remove only those additions after their services have stopped.
            # shellcheck disable=SC2086
            dnf -y remove $packages_to_remove >/dev/null 2>&1 || rollback_ok=false
        fi
    fi
    if [ "$rollback_ok" = true ]; then
        echo "installation failed; previous managed files and service state restored from $ROLLBACK_DIR" >&2
    else
        echo "installation failed and automatic rollback could not be proven; inspect $ROLLBACK_DIR immediately" >&2
    fi
    exit "$status"
}
trap rollback_install EXIT HUP INT TERM

dnf -y install vnstat sysstat
systemctl enable --now vnstat.service sysstat.service

if ! getent passwd node-audit >/dev/null 2>&1 && [ -z "$ROOT" ]; then
    useradd --system --home-dir /var/lib/vps-node-auditor --shell /sbin/nologin node-audit
fi
if getent passwd node-audit >/dev/null 2>&1; then
    install -d -o root -g node-audit -m 0750 "$ETC_DIR"
    install -d -o node-audit -g node-audit -m 0700 "$STATE_DIR"
    install -d -o node-audit -g node-audit -m 0700 "$BACKUP_ROOT/database"
else
    install -d -m 0750 "$ETC_DIR"
    install -d -m 0700 "$STATE_DIR" "$BACKUP_ROOT/database"
fi
install -d -o root -g root -m 0700 "$BACKUP_ROOT/provision"
install -d -o root -g root -m 0755 "$LIBEXEC_DIR" "$LOCAL_BIN" "$USR_BIN" "$LOCAL_SBIN" "$SYSTEMD_DIR"

# Freeze the old writer immediately before its binaries and schema can change.
if [ "$collector_active" = true ]; then
    systemctl stop node-audit-collector.service
fi
if [ -f "$STATE_DIR/audit.db" ]; then
    install -d -m 0700 "$ROLLBACK_DIR/database-files"
    for suffix in "" -wal -shm; do
        [ ! -e "$STATE_DIR/audit.db$suffix" ] || \
            cp -a "$STATE_DIR/audit.db$suffix" "$ROLLBACK_DIR/database-files/audit.db$suffix"
    done
    DATABASE_FILES_SAVED=true
fi
if [ -x "$LIBEXEC_DIR/node-audit-maintenance" ] && [ -f "$ETC_DIR/config.json" ]; then
    "$LIBEXEC_DIR/node-audit-maintenance" \
        --config "$ETC_DIR/config.json" backup --dir "$ROLLBACK_DIR/database"
    DATABASE_ROLLBACK=$(find "$ROLLBACK_DIR/database" -maxdepth 1 -type f -name 'audit-*.db' -print -quit)
    [ -n "$DATABASE_ROLLBACK" ] || { echo "database rollback artifact was not created" >&2; exit 1; }
fi

for binary in vna node-audit node-audit-collector node-audit-maintenance node-provision; do
    install -o root -g root -m 0755 "$DIST_DIR/$binary" "$LIBEXEC_DIR/$binary"
done
ln -sfn "$LIBEXEC_DIR/node-audit" "$LOCAL_BIN/node-audit"
ln -sfn "$LIBEXEC_DIR/node-provision" "$LOCAL_BIN/node-provision"
ln -sfn "$LIBEXEC_DIR/vna" "$LOCAL_BIN/vna"
ln -sfn "$LOCAL_BIN/node-audit" "$USR_BIN/node-audit"
ln -sfn "$LOCAL_BIN/node-provision" "$USR_BIN/node-provision"
ln -sfn "$LOCAL_BIN/vna" "$USR_BIN/vna"
install -o root -g root -m 0755 "$PROJECT_DIR/deploy/scripts/backup.sh" "$LOCAL_SBIN/node-audit-backup"
install -o root -g root -m 0755 "$PROJECT_DIR/deploy/scripts/restore.sh" "$LOCAL_SBIN/node-audit-restore"

[ -z "$CONFIG_SOURCE" ] || install -o root -g node-audit -m 0640 "$CONFIG_SOURCE" "$ETC_DIR/config.json"
[ -z "$PROVISION_SOURCE" ] || install -o root -g root -m 0600 "$PROVISION_SOURCE" "$ETC_DIR/provision.json"
if [ -f "$ETC_DIR/provision.json" ]; then
    python3 "$PROJECT_DIR/deploy/scripts/migrate-provision-config.py" "$ETC_DIR/provision.json"
fi

install -o root -g root -m 0644 "$PROJECT_DIR"/deploy/systemd/node-audit-*.service "$SYSTEMD_DIR/"
install -o root -g root -m 0644 "$PROJECT_DIR"/deploy/systemd/node-audit-*.timer "$SYSTEMD_DIR/"
systemctl daemon-reload

if [ -f "$ETC_DIR/config.json" ]; then
    systemctl enable node-audit-collector.service
    systemctl enable --now node-audit-maintenance.timer node-audit-backup.timer node-audit-observe.timer
    systemctl restart node-audit-collector.service
    systemctl is-active --quiet node-audit-collector.service
fi

ROLLBACK_ARMED=false
trap - EXIT HUP INT TERM
echo "installation completed; rollback point: $ROLLBACK_DIR"
