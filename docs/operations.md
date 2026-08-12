# 运维手册

VPS Node Auditor 只在服务器本机通过 SSH 管理，不提供网页或公网管理接口。

## 日常入口

```bash
sudo vna
```

交互菜单包含创建配置、用户与设备管理、流量统计、状态与一致性检查、日志、备份与恢复。脚本也可直接调用：

```bash
sudo vna create --user alice --device windows
sudo vna users
sudo vna rotate --user alice --device windows --new-device laptop --yes
sudo vna disable --user alice --device laptop --yes
sudo vna remove-user --user alice --yes
sudo vna traffic --since 24h
sudo vna user alice --since 7d
sudo vna month
sudo vna status
sudo vna check
sudo vna check --json
sudo vna logs
sudo vna logs --follow
sudo vna timers
sudo vna backup
sudo vna restore /var/backups/vps-node-auditor/database/audit-TIMESTAMP.db --yes
```

`create` 立即执行。`rotate`、`disable`、`remove-user` 和 `restore` 在交互终端要求确认；非交互执行必须增加 `--yes`。底层 `node-provision` 供自动化使用，不提供交互确认，因此日常操作优先使用 `vna`。

每次凭据变更会自动完成：全局锁、唯一私有备份、候选配置检查、原子替换、受控重启、MainPID/443/Stats/服务身份验证、SQLite 事务；失败时恢复旧配置和服务。客户端 YAML 位于 `/root/vps-node-auditor/clients/`，目录权限 `0700`，文件权限 `0600`。

## 固定权限

| 路径 | 所有者与权限 |
|---|---|
| `/etc/vps-node-auditor/config.json` | `root:node-audit 0640` |
| `/etc/vps-node-auditor/provision.json` | `root:root 0600` |
| sing-box 主配置 | `root:node-audit 0640` |
| `/var/lib/vps-node-auditor/` | `node-audit:node-audit 0700` |
| SQLite、WAL、SHM | 与数据库目录同属主，`0600` |

真实 IP、UUID、Stats 名称、SNI、short ID、YAML、数据库和日志均属于私人部署数据，不得提交到公开仓库。

## 安装、备份和恢复

构建 Linux AMD64 静态二进制后，以 root 执行安装器。已有配置只有在显式提供相应参数时才替换：

```bash
sudo deploy/scripts/install.sh --dist-dir dist/linux-amd64
```

恢复会先验证所选备份本身，再记录采集器原状态和数据库精确副本。任何后续步骤失败都会恢复数据库、WAL/SHM、属主权限及采集器原状态。

卸载默认保留配置、数据库和备份：

```bash
sudo deploy/scripts/uninstall.sh
```

`--purge` 会永久删除全部项目状态，只能在明确不再需要数据时使用。
