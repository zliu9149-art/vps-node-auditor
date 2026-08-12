# 参考项目索引

本目录只保存外部参考资料和本地源码浅克隆，不属于 `vps-node-auditor` 主项目实现。参考项目不能覆盖主项目需求，也不应未经独立评估直接部署到 VPS。

## 来源与借鉴边界

| 参考项 | 类型与本地状态 | 值得借鉴的部分 | 采用原则 |
|---|---|---|---|
| [sing-box V2Ray Stats API](https://github.com/SagerNet/sing-box/blob/testing/docs/configuration/experimental/v2ray-api.md) | 官方能力文档；未克隆 | 官方定义的按用户上下行计数器 | 优先评估，但先只读检查现有二进制是否包含对应构建能力 |
| [S-UI](https://github.com/alireza0/s-ui) | 外部项目；未克隆 | sing-box 多用户、流量统计、用户与订阅数据结构 | 只参考相关代码，不原样安装完整面板 |
| [sb-panel](https://github.com/aaronchin/sb-panel) | 本地浅克隆：[`sb-panel/`](sb-panel/) | 定时查询 V2Ray Stats、SQLite 历史记录、按用户查询和订阅格式 | 重点借鉴采集器和数据库思路，不部署其 Web 面板、SSH 管理或远程推送能力 |
| [vnStat](https://github.com/vergoh/vnstat) | 外部工具；未克隆 | 轻量的网卡小时、日、月总流量 | 建议作为服务器总量对账来源，不替代按用户统计 |
| [Xray-core](https://github.com/XTLS/Xray-core) | 外部项目；未克隆 | 官方内置的按用户上下行及在线状态统计 | 仅在现有 sing-box 无法满足统计要求时评估为备选核心 |
| [sysstat](https://github.com/sysstat/sysstat) 与 [iproute2](https://git.kernel.org/pub/scm/network/iproute2/iproute2.git) | Linux 系统工具；未克隆 | `sar`、`nstat`、`ss` 提供 CPU、网络、TCP 重传和连接指标 | 优先作为故障时间线数据源，不在主项目中重复实现内核统计 |

## 本地浅克隆记录

### sb-panel

- 本地路径：`references/sb-panel/`
- 上游：`https://github.com/aaronchin/sb-panel.git`
- 克隆类型：浅克隆
- 当前提交：`252aa019d1e1c34a277de900369fd1f66cfc34c8`
- Git 元数据：保留在 `references/sb-panel/.git/`

该目录保持上游参考源码身份。主项目默认通过根目录 `.gitignore` 忽略本地参考克隆，避免把第三方源码误当作主项目代码提交。

## 使用规则

1. 主项目需求优先于任何参考实现。
2. 引用实现前记录具体上游版本、文件和许可证影响。
3. 不直接从参考目录部署服务或运行一键安装脚本。
4. 不把参考项目的 Web 面板、远程 SSH 管理、公开订阅或高权限常驻行为带入主项目。
5. 后续若下载其他参考源码，应放在各自独立子目录并保留来源信息。
