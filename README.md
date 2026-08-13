# VPS Node Auditor

[![CI](https://github.com/zliu9149-art/vps-node-auditor/actions/workflows/ci.yml/badge.svg)](https://github.com/zliu9149-art/vps-node-auditor/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

**在自己的 VPS 上，用一条 `vna` 命令管理连接、查看每个用户或设备的流量，并持续检查节点是否正常。**

VPS Node Auditor 是一个面向**单台 Linux VPS 所有者**的 SSH 管理工具。它读取 sing-box 的 V2Ray Stats，在服务器本地记录流量与运行状态，并为 VLESS Reality 连接提供创建、轮换和撤销能力。

它不是机场面板，也不会再开放一个管理网站。你只需 SSH 登录服务器：日常操作使用中文菜单，需要自动化时再调用明确的子命令。

```bash
sudo vna
```

## 为什么会有这个项目？

自己维护一个 VPS 节点时，常见问题并不是“怎样再装一个面板”，而是：

- 这个月究竟用了多少流量，哪位用户或哪台设备用得最多？
- sing-box、Stats API 和后台采集现在是否都正常？
- 怎样给家人或自己的新设备单独创建连接，并在遗失后只撤销这一份凭据？
- 配置修改失败时，能否保留旧配置和回滚点？
- 能否不开放管理端口、不建立账号系统，也完成这些工作？

VPS Node Auditor 为这类小型、自用场景提供一个克制的答案：**管理入口留在 SSH 内，审计数据留在 VPS 本地，每个设备使用独立凭据。**

## 它能做什么？

| 需求 | VPS Node Auditor 的处理方式 |
|---|---|
| 查看谁用了流量 | 按用户统计，也可细分到设备凭据；支持 24 小时、7 天、月度等时间范围 |
| 核对总流量 | 将 sing-box 用户流量与 vnStat 的整机网卡口径分开展示，并只比较共同覆盖的时间段 |
| 管理连接 | 创建、轮换、停用单个设备，或一次撤销某个用户的全部活动连接 |
| 检查节点 | 检查 sing-box、443 端口、Stats API、采集新鲜度、SQLite 映射、客户端文件和权限 |
| 留存历史 | 后台定时采集到本地 SQLite，按保留策略维护分钟、事件和汇总数据 |
| 备份恢复 | 定时备份，也可手动备份或从指定数据库备份恢复 |
| 降低误操作风险 | 变更前加锁和备份，校验候选配置，重启后验证关键状态；失败时恢复原状态 |

后台链路很简单：

```text
sing-box V2Ray Stats（仅本机回环地址）
                    │ 每 60 秒采集
                    ▼
        node-audit-collector（低权限用户）
                    │
                    ▼
              SQLite audit.db
                    │
                    ▼
          vna 查询、检查和凭据管理

vnStat / Linux 指标 ────────┘  用作整机流量与运行状态参考
```

## 适合谁？

适合：

- 已经在一台 VPS 上运行 sing-box + VLESS Reality；
- 由一位所有者通过 SSH 管理，连接提供给自己、家人或少量可信用户；
- 希望按人、按设备区分流量，但不想维护 Web 面板、数据库服务和登录系统；
- 看重本地存储、最小网络暴露、明确权限与可回滚操作。

不适合：

- 需要多节点集中控制、用户自助注册、套餐计费或订阅链接分发；
- 需要浏览器管理界面、公开 API、二维码门户或商业运营功能；
- 尚未安装和配置 sing-box，想用一个脚本从零搭建完整代理节点；
- 要把流量记录作为财务级计费凭证。

> [!IMPORTANT]
> 流量归属依据是每个设备独立的 UUID。它能说明“哪份凭据产生了流量”，但凭据被转发或共享后，无法证明实际使用者是谁。

## 日常使用

### 不熟悉命令行：打开中文菜单

```bash
sudo vna
```

在交互终端中会看到六类操作：创建配置、管理用户和设备、流量统计、节点检查、日志、备份与恢复。直接按编号选择即可。

### 熟悉命令行：直接执行

```bash
# 给 alice 的手机创建一份独立连接配置
sudo vna create --user alice --device phone

# 查看当前用户和设备；普通输出不会显示 UUID
sudo vna users

# 查看最近 24 小时的用户流量
sudo vna traffic --since 24h

# 细分到每台设备
sudo vna traffic --since 7d --by-device

# 查看 alice 最近 7 天的时间线
sudo vna user alice --since 7d

# 查看本计费周期与 vnStat 对账结果
sudo vna month

# 快速状态与完整一致性检查
sudo vna status
sudo vna check

# 只跟踪命令执行后产生的新日志
sudo vna logs --follow

# 立即创建数据库备份
sudo vna backup
```

轮换、停用、整用户撤销和恢复备份会改变现有状态。在交互终端中，`vna` 会要求确认；在脚本中必须显式加入 `--yes`：

```bash
sudo vna rotate --user alice --device phone --new-device new-phone --yes
sudo vna disable --user alice --device new-phone --yes
sudo vna remove-user --user alice --yes
```

所有命令可通过 `vna help` 查看。查询和一致性检查也支持 JSON 输出，便于接入脚本：

```bash
sudo vna check --json
sudo vna traffic --since 24h --json
```

## 部署前必须了解

当前公开版本不是通用“一键脚本”，而是为已经运行的节点增加审计与安全管理能力。生产安装前请确认：

- **系统：** 当前安装器面向使用 `systemd`、`dnf` 和 RPM 的 AlmaLinux/RHEL 系统；
- **架构：** CI 和现有部署流程构建 Linux AMD64 静态二进制；
- **代理核心：** 已有 sing-box VLESS Reality 入站，且 sing-box 构建包含 `with_v2ray_api`；
- **Stats：** V2Ray Stats 只监听回环地址，例如 `127.0.0.1:10085`，不要暴露到公网；
- **权限：** 需要 root 完成首次安装和凭据变更，采集器安装后以无登录权限的 `node-audit` 用户运行；
- **构建：** 从源码构建需要 Go 1.24 或更高版本；
- **私人配置：** 真实 IP、UUID、Reality 参数、客户端 YAML、数据库和日志都不应放进 Git 仓库。

新 VPS 必须先做只读兼容性检查。不要假设另一台服务器上的 sing-box 版本、构建标签、入站结构或服务路径可以直接复用。

## 从源码构建

```bash
git clone https://github.com/zliu9149-art/vps-node-auditor.git
cd vps-node-auditor

go test ./...
go vet ./...

mkdir -p dist/linux-amd64
for command in vna node-audit node-audit-collector node-audit-maintenance node-provision; do
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -buildvcs=false \
    -o "dist/linux-amd64/$command" "./cmd/$command"
done
```

这一步只生成程序，不会修改 sing-box 或系统服务。你可以先确认构建结果：

```bash
./dist/linux-amd64/vna version
```

## 生产安装概览

仓库提供事务式安装/升级脚本，但它需要与你的真实节点匹配的两份私有配置：

- 审计配置：Stats 地址、网卡、时区、流量周期、保留策略和凭据映射；
- 凭据配置：sing-box 配置路径、VLESS Reality 入站、服务器地址和客户端输出目录。

[`config.v2ray.example.json`](config.v2ray.example.json) 和 [`provision.example.json`](provision.example.json) 仅用于说明字段，**其中的示例值不能直接用于生产**。建议把真实配置保存在 root-only 的仓库外目录，再执行：

```bash
sudo deploy/scripts/install.sh \
  --dist-dir "$PWD/dist/linux-amd64" \
  --config /root/vps-node-auditor-private/config.json \
  --provision-config /root/vps-node-auditor-private/provision.json
```

安装器会安装 `vnstat` 和 `sysstat`、创建低权限采集账户、安装 systemd 服务与定时器，并在升级前保存回滚点。它不会替你从零创建 sing-box 节点；已有生产用户的迁移也应先在只读审计后规划，而不是直接套用示例文件。

安装完成后至少验证：

```bash
sudo vna version
sudo vna status
sudo vna check
sudo vna timers
```

完整的预检、真实客户端烟雾测试和回滚要求见[部署与验收](docs/deployment.md)。日常权限、备份和恢复操作见[运维手册](docs/operations.md)。

## 安全与隐私边界

- 不提供 Web 服务、用户注册、订阅端点或公网管理 API；
- Stats API 只允许回环地址，采集服务不新增公网监听端口；
- 采集器使用独立低权限系统账户，systemd unit 启用多项沙箱限制；
- SQLite、WAL/SHM、备份和客户端 YAML 按敏感凭据保护；
- 普通列表、检查结果、事件与服务日志不输出完整 UUID；
- 查询 CLI 以只读方式打开数据库，后台采集器是常规审计数据的唯一写入者；
- 删除用户会撤销活动连接，但保留历史记录，便于事后审计。

任何能够读取 root 私有文件或完整 SQLite 数据库的人仍可能取得凭据。本项目降低不必要的暴露面，但不能抵御 VPS 已被完全控制的情况。

## 数据口径说明

sing-box Stats 与 vnStat 回答的是不同问题：

- **sing-box Stats：** 按 UUID/设备凭据统计代理流量，用于用户归属；
- **vnStat：** 统计网卡总流量，包含代理之外的系统通信，用于独立参考；
- **云厂商账单：** 可能采用不同方向、时间周期和计量规则，应作为最终套餐口径。

因此两组数字不应被强行要求完全一致。VPS Node Auditor 会保留各自语义，并只在共同采集覆盖期内给出差异参考。

## 项目文档

| 文档 | 什么时候阅读 |
|---|---|
| [运维手册](docs/operations.md) | 已安装，想查询流量、检查状态、备份或恢复 |
| [部署与验收](docs/deployment.md) | 准备接入真实 VPS、升级或制定回滚方案 |
| [架构说明](docs/architecture.md) | 想理解采集链路、计数器语义和安全边界 |
| [开发规范](docs/development.md) | 准备修改代码、运行测试或构建二进制 |
| [项目要求](requirements/PROJECT_REQUIREMENTS.md) | 需要核对 v1.0.0 的正式产品边界和兼容承诺 |
| [发布说明](docs/releasing.md) | 准备制作版本、校验公开内容或发布资产 |

## 项目状态

- 当前稳定版本：`v1.0.1`
- 当前生产安装目标：AlmaLinux/RHEL 系 Linux AMD64 单节点
- 自动检查：格式、`go vet`、单元/集成测试、Linux AMD64 构建、Shell/Python 检查、漏洞扫描和秘密扫描
- 兼容策略：v1.x 保持公开 CLI 语义，并以事务方式向前迁移 SQLite；破坏性接口变更留到 v2.0.0

欢迎提交能够保持“小而安全、SSH-only、单节点”边界的 Issue 和 Pull Request。多节点平台、Web 面板和商业用户系统不在当前路线内。

## 许可证

本项目采用 [Apache License 2.0](LICENSE) 开源许可证。

