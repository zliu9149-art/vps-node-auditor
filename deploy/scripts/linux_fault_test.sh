#!/usr/bin/env sh
# Execute install rollback fault injection inside an isolated temporary root.
set -eu

[ "$(id -u)" -eq 0 ] || { echo "linux fault tests require an isolated root identity" >&2; exit 1; }
PROJECT_DIR=$(unset CDPATH; cd -- "$(dirname -- "$0")/../.." && pwd)
TEST_DIR=$(mktemp -d /tmp/vna-linux-fault-XXXXXXXXXX)
trap 'rm -rf "$TEST_DIR"' EXIT HUP INT TERM
ROOT="$TEST_DIR/root"
DIST="$TEST_DIR/dist"
STUBS="$TEST_DIR/stubs"
STATE="$TEST_DIR/state"
mkdir -p "$ROOT/usr/local/libexec/vps-node-auditor" "$ROOT/etc/vps-node-auditor" \
    "$ROOT/etc/systemd/system" "$ROOT/var/lib/vps-node-auditor" "$DIST" "$STUBS" "$STATE"

printf '%s\n' old-vna >"$ROOT/usr/local/libexec/vps-node-auditor/vna"
for binary in node-audit node-audit-collector node-provision; do
    printf 'old-%s\n' "$binary" >"$ROOT/usr/local/libexec/vps-node-auditor/$binary"
done
printf '%s\n' old-config >"$ROOT/etc/vps-node-auditor/config.json"
printf '%s\n' old-unit >"$ROOT/etc/systemd/system/node-audit-collector.service"
printf '%s\n' old-database >"$ROOT/var/lib/vps-node-auditor/audit.db"
printf '%s\n' active >"$STATE/collector"

cat >"$ROOT/usr/local/libexec/vps-node-auditor/node-audit-maintenance" <<'EOF'
#!/usr/bin/env sh
set -eu
command=""
directory=""
database=""
while [ "$#" -gt 0 ]; do
    case "$1" in
        --config) shift 2 ;;
        --database) database=$2; shift 2 ;;
        --dir) directory=$2; shift 2 ;;
        backup|integrity) command=$1; shift ;;
        *) shift ;;
    esac
done
case "$command" in
    backup) mkdir -p "$directory"; cp "$VNA_TEST_ROOT/var/lib/vps-node-auditor/audit.db" "$directory/audit-test.db" ;;
    integrity) [ -f "$database" ] ;;
    *) exit 2 ;;
esac
EOF
chmod 0755 "$ROOT/usr/local/libexec/vps-node-auditor/node-audit-maintenance"

for binary in vna node-audit node-audit-collector node-provision; do
    printf '#!/usr/bin/env sh\nprintf "new-%s\\n"\n' "$binary" >"$DIST/$binary"
    chmod 0755 "$DIST/$binary"
done
cp "$ROOT/usr/local/libexec/vps-node-auditor/node-audit-maintenance" "$DIST/node-audit-maintenance"

cat >"$STUBS/dnf" <<'EOF'
#!/usr/bin/env sh
case "${2:-}" in
    install) : >"$VNA_FAULT_STATE/packages-installed" ;;
    remove) : >"$VNA_FAULT_STATE/packages-removed" ;;
esac
exit 0
EOF
cat >"$STUBS/rpm" <<'EOF'
#!/usr/bin/env sh
exit 1
EOF
cat >"$STUBS/systemctl" <<'EOF'
#!/usr/bin/env sh
set -eu
command=$1
shift
case "$command" in
    is-active)
        [ "${1:-}" != "--quiet" ] || shift
        if [ "${1:-}" = "node-audit-collector.service" ]; then
            [ "$(cat "$VNA_FAULT_STATE/collector")" = active ]
        else
            exit 3
        fi
        ;;
    is-enabled)
        [ "${1:-}" != "--quiet" ] || shift
        [ "${1:-}" = "node-audit-collector.service" ]
        ;;
    show) printf '%s\n' not-found ;;
    stop)
        [ "${1:-}" != "node-audit-collector.service" ] || printf '%s\n' inactive >"$VNA_FAULT_STATE/collector"
        ;;
    start)
        [ "${1:-}" != "node-audit-collector.service" ] || printf '%s\n' active >"$VNA_FAULT_STATE/collector"
        ;;
    restart)
        if [ "${1:-}" = "node-audit-collector.service" ] && [ ! -e "$VNA_FAULT_STATE/restart-failed" ]; then
            : >"$VNA_FAULT_STATE/restart-failed"
            exit 1
        fi
        ;;
    enable|disable|daemon-reload) ;;
    *) exit 2 ;;
esac
EOF
chmod 0755 "$STUBS/dnf" "$STUBS/rpm" "$STUBS/systemctl"

set +e
PATH="$STUBS:$PATH" VNA_TEST_ROOT="$ROOT" VNA_FAULT_STATE="$STATE" \
    "$PROJECT_DIR/deploy/scripts/install.sh" --dist-dir "$DIST" >"$TEST_DIR/stdout" 2>"$TEST_DIR/stderr"
status=$?
set -e
[ "$status" -ne 0 ] || { echo "faulted install unexpectedly succeeded" >&2; exit 1; }
grep -Fxq old-vna "$ROOT/usr/local/libexec/vps-node-auditor/vna"
grep -Fxq old-config "$ROOT/etc/vps-node-auditor/config.json"
grep -Fxq old-unit "$ROOT/etc/systemd/system/node-audit-collector.service"
grep -Fxq old-database "$ROOT/var/lib/vps-node-auditor/audit.db"
[ "$(cat "$STATE/collector")" = active ]
[ -f "$STATE/packages-installed" ]
[ -f "$STATE/packages-removed" ]
grep -Fq "previous managed files and service state restored" "$TEST_DIR/stderr"
echo "Linux install rollback fault injection: ok"

RESTORE_ROOT="$TEST_DIR/restore-root"
RESTORE_STATE="$TEST_DIR/restore-state"
RESTORE_STUBS="$TEST_DIR/restore-stubs"
mkdir -p "$RESTORE_ROOT/usr/local/libexec/vps-node-auditor" \
    "$RESTORE_ROOT/etc/vps-node-auditor" "$RESTORE_ROOT/var/lib/vps-node-auditor" \
    "$RESTORE_STUBS" "$RESTORE_STATE"
printf '%s\n' config >"$RESTORE_ROOT/etc/vps-node-auditor/config.json"
printf '%s\n' old-database >"$RESTORE_ROOT/var/lib/vps-node-auditor/audit.db"
printf '%s\n' old-wal >"$RESTORE_ROOT/var/lib/vps-node-auditor/audit.db-wal"
printf '%s\n' old-shm >"$RESTORE_ROOT/var/lib/vps-node-auditor/audit.db-shm"
printf '%s\n' replacement >"$TEST_DIR/replacement.db"
printf '%s\n' active >"$RESTORE_STATE/collector"

cat >"$RESTORE_ROOT/usr/local/libexec/vps-node-auditor/node-audit-maintenance" <<'EOF'
#!/usr/bin/env sh
set -eu
command=""
directory=""
while [ "$#" -gt 0 ]; do
    case "$1" in
        --config|--database) shift 2 ;;
        --dir) directory=$2; shift 2 ;;
        backup|integrity) command=$1; shift ;;
        *) shift ;;
    esac
done
case "$command" in
    backup) mkdir -p "$directory"; cp "$VNA_TEST_ROOT/var/lib/vps-node-auditor/audit.db" "$directory/audit-test.db" ;;
    integrity)
        count=0
        [ ! -f "$VNA_FAULT_STATE/integrity-count" ] || count=$(cat "$VNA_FAULT_STATE/integrity-count")
        count=$((count + 1))
        printf '%s\n' "$count" >"$VNA_FAULT_STATE/integrity-count"
        [ "$count" -ne 2 ]
        ;;
    *) exit 2 ;;
esac
EOF
chmod 0755 "$RESTORE_ROOT/usr/local/libexec/vps-node-auditor/node-audit-maintenance"

cat >"$RESTORE_STUBS/systemctl" <<'EOF'
#!/usr/bin/env sh
set -eu
command=$1
shift
case "$command" in
    is-active)
        [ "${1:-}" != "--quiet" ] || shift
        [ "$(cat "$VNA_FAULT_STATE/collector")" = active ]
        ;;
    stop) printf '%s\n' inactive >"$VNA_FAULT_STATE/collector" ;;
    start) printf '%s\n' active >"$VNA_FAULT_STATE/collector" ;;
    *) exit 2 ;;
esac
EOF
chmod 0755 "$RESTORE_STUBS/systemctl"

set +e
PATH="$RESTORE_STUBS:$PATH" VNA_TEST_ROOT="$RESTORE_ROOT" VNA_FAULT_STATE="$RESTORE_STATE" \
    "$PROJECT_DIR/deploy/scripts/restore.sh" "$TEST_DIR/replacement.db" >"$TEST_DIR/restore-stdout" 2>"$TEST_DIR/restore-stderr"
status=$?
set -e
[ "$status" -ne 0 ] || { echo "faulted restore unexpectedly succeeded" >&2; exit 1; }
grep -Fxq old-database "$RESTORE_ROOT/var/lib/vps-node-auditor/audit.db"
grep -Fxq old-wal "$RESTORE_ROOT/var/lib/vps-node-auditor/audit.db-wal"
grep -Fxq old-shm "$RESTORE_ROOT/var/lib/vps-node-auditor/audit.db-shm"
[ "$(cat "$RESTORE_STATE/collector")" = active ]
grep -Fq "original database and collector state recovered" "$TEST_DIR/restore-stderr"
echo "Linux restore rollback fault injection: ok"

# Repeat the same failure with an originally inactive collector. Recovery must
# not start a service that the operator had deliberately left stopped.
printf '%s\n' old-database >"$RESTORE_ROOT/var/lib/vps-node-auditor/audit.db"
printf '%s\n' old-wal >"$RESTORE_ROOT/var/lib/vps-node-auditor/audit.db-wal"
printf '%s\n' old-shm >"$RESTORE_ROOT/var/lib/vps-node-auditor/audit.db-shm"
printf '%s\n' inactive >"$RESTORE_STATE/collector"
rm -f "$RESTORE_STATE/integrity-count"
set +e
PATH="$RESTORE_STUBS:$PATH" VNA_TEST_ROOT="$RESTORE_ROOT" VNA_FAULT_STATE="$RESTORE_STATE" \
    "$PROJECT_DIR/deploy/scripts/restore.sh" "$TEST_DIR/replacement.db" >"$TEST_DIR/inactive-stdout" 2>"$TEST_DIR/inactive-stderr"
status=$?
set -e
[ "$status" -ne 0 ] || { echo "inactive faulted restore unexpectedly succeeded" >&2; exit 1; }
grep -Fxq old-database "$RESTORE_ROOT/var/lib/vps-node-auditor/audit.db"
[ "$(cat "$RESTORE_STATE/collector")" = inactive ]
echo "Linux restore inactive-state rollback: ok"
