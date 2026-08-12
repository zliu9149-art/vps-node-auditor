# 部署与验收

## 本地门禁

1. `gofmt`、`go vet ./...`、`go test ./...`。
2. 注入 `v1.0.0` 构建五个 Linux AMD64 静态二进制，`vna version` 必须准确。
3. ShellCheck、Python 语法、govulncheck、依赖许可证和秘密扫描通过。
4. 工作树干净，私人最终提交是唯一发布源码依据。

## 生产流程

1. SSH 只读预检：系统、sing-box 版本和构建标签、服务、MainPID、443、Stats、采集器、定时器、配置和数据库权限。
2. 创建独立回滚点；上传隔离发布包；服务器端复核 SHA-256。
3. 安装器升级二进制、unit 和脚本，迁移 provision 配置但保留真实用户、UUID和审计数据。
4. 验证服务、端口、Stats、定时器、采集推进、权限、菜单和 `vna check`。
5. 创建 `release-smoke`，进行真实客户端连接测试，再用 `remove-user` 撤销。
6. 确认临时 YAML 删除、历史保留且现有用户不受影响。

任一阶段第一次失败立即停止并回滚，不继续生成公开候选。

## 公开候选

成功部署的私人提交通过 `git archive` 导出到 `D:\\vps-node-auditor-public`。目标若已存在且非空则停止。候选不得包含 `.git`、缓存、构建产物、数据库、YAML、日志或真实配置。许可证确定前，候选保持为没有 `.git` 的普通目录。
