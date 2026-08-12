# VPS Node Auditor

VPS Node Auditor 是面向单台 Linux VPS 的轻量、低权限、SSH-only 流量审计和凭据管理工具。正式名称用于项目识别，仓库名为 `vps-node-auditor`，日常命令为 `vna`。

它提供 sing-box V2Ray Stats 用户流量、Linux 主机指标、vnStat 对账、SQLite 历史、备份恢复，以及原子化的用户/设备凭据管理。它不提供 Web 面板、用户注册、订阅地址或公网管理 API。

## 使用

```bash
sudo vna
```

TTY 中会显示中文菜单；管道或自动化中无参数调用只显示帮助。常用直接命令：

```bash
sudo vna create --user alice --device windows
sudo vna users
sudo vna traffic --since 24h
sudo vna status
sudo vna check
sudo vna logs --follow
sudo vna backup
```

危险操作在交互终端要求确认，非交互调用需要 `--yes`：

```bash
sudo vna rotate --user alice --device windows --new-device laptop --yes
sudo vna disable --user alice --device laptop --yes
sudo vna remove-user --user alice --yes
```

每次写操作自动备份、检查、应用、重启验证并在失败时回滚。列表和检查输出不显示 UUID。

## 开发

需要 Go 1.24 或更高版本：

```powershell
go test ./...
go vet ./...
```

正式运维见 [docs/operations.md](docs/operations.md)，部署门禁见 [docs/deployment.md](docs/deployment.md)，安全与兼容要求见 [requirements/PROJECT_REQUIREMENTS.md](requirements/PROJECT_REQUIREMENTS.md)。

真实服务器地址、UUID、Stats 名称、SNI、short ID、YAML、SQLite 和日志不得加入源码或公开候选。

## 许可证

本项目采用 [Apache License 2.0](LICENSE) 开源许可证。
