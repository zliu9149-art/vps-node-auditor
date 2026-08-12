#!/usr/bin/env sh
# Uninstall programs while retaining sensitive state unless --purge is explicit.
set -eu

PURGE=false
case "$#:${1:-}" in
    0:) ;;
    1:--purge) PURGE=true ;;
    *) echo "only --purge is supported" >&2; exit 2 ;;
esac
[ "$(id -u)" -eq 0 ] || { echo "must run as root" >&2; exit 1; }

systemctl disable --now node-audit-collector.service node-audit-maintenance.timer \
    node-audit-backup.timer node-audit-observe.timer 2>/dev/null || true
rm -f /etc/systemd/system/node-audit-collector.service \
    /etc/systemd/system/node-audit-maintenance.service /etc/systemd/system/node-audit-maintenance.timer \
    /etc/systemd/system/node-audit-backup.service /etc/systemd/system/node-audit-backup.timer \
    /etc/systemd/system/node-audit-observe.service /etc/systemd/system/node-audit-observe.timer \
    /usr/local/bin/vna /usr/bin/vna \
    /usr/local/bin/node-audit /usr/local/bin/node-provision /usr/bin/node-audit /usr/bin/node-provision \
    /usr/local/sbin/node-audit-backup /usr/local/sbin/node-audit-restore
rm -rf /usr/local/libexec/vps-node-auditor
systemctl daemon-reload

if [ "$PURGE" = true ]; then
    CONFIG_DIR=/etc/vps-node-auditor
    DATA_DIR=/var/lib/vps-node-auditor
    BACKUP_DIR=/var/backups/vps-node-auditor
    [ "$CONFIG_DIR" = "/etc/vps-node-auditor" ] || exit 1
    [ "$DATA_DIR" = "/var/lib/vps-node-auditor" ] || exit 1
    [ "$BACKUP_DIR" = "/var/backups/vps-node-auditor" ] || exit 1
    rm -rf "$CONFIG_DIR" "$DATA_DIR" "$BACKUP_DIR"
    userdel node-audit 2>/dev/null || true
    echo "programs, configuration, database, and backups were permanently deleted"
else
    echo "programs were uninstalled; configuration, database, and backups were retained"
fi
