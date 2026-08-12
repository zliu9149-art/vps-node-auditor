#!/usr/bin/env sh
# Create one private, transactionally consistent SQLite backup.
# 中文：创建一份私有、事务一致的 SQLite 备份。
set -eu
[ "$(id -u)" -eq 0 ] || { echo "必须以 root 运行。" >&2; exit 1; }
CONFIG=${1:-/etc/vps-node-auditor/config.json}
exec /usr/local/libexec/vps-node-auditor/node-audit-maintenance \
    --config "$CONFIG" backup --dir /var/backups/vps-node-auditor/database

