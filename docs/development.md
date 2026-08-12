# 开发规范

## Go 代码

- 所有代码必须通过 `gofmt`、`go vet ./...` 和 `go test ./...`。
- 每个导出类型、函数、方法和变量使用符合 Go Doc 约定的完整句子注释，并以标识符名称开头。
- 注释用于解释安全边界、数据语义和非显然的设计原因，不逐行复述代码。
- 计数器重置、数据缺口、事务边界和凭据脱敏属于必须注释的关键逻辑。
- 日志、测试失败消息和普通 CLI 输出不得包含真实完整 UUID。
- 不引入 Web 服务、网络监听、任意 shell 执行或远程 SSH 能力。

## 测试

- 纯计算逻辑优先使用表驱动单元测试。
- SQLite 行为使用临时目录中的真实数据库做集成测试。
- 至少覆盖首次基线、正常增量、确认重启、未确认计数器回退、重复快照和采集失败。
- 新数据源必须根据真实接口结构制作脱敏固定测试夹具，使自动化测试不依赖实时 VPS；夹具用于防止回归，但不替代实际 VPS 的只读兼容性审计。
- V2Ray Stats 协议测试必须覆盖非重置请求、protobuf 畸形响应、未知用户忽略、重复方向拒绝和错误消息脱敏。

## Git

- `main` 保存可说明、可验证的稳定基线。
- 开发工作在 `codex/` 前缀的主题分支进行。
- 提交信息使用 `<类型>: <说明>`，例如 `feat: add phase-one local audit core`。
- 每个提交只包含一个可审查目标；第三方浅克隆不进入主项目提交。
- 提交前检查 `git diff --check`，并运行测试、静态检查和目标平台构建。

## 本地验证命令

```powershell
Get-ChildItem cmd,internal -Recurse -Filter *.go |
  ForEach-Object { gofmt -w $_.FullName }
go test ./...
go vet ./...
$env:GOOS = "linux"
$env:GOARCH = "amd64"
$env:CGO_ENABLED = "0"
go build -trimpath -o bin/linux-amd64/node-audit ./cmd/node-audit
go build -trimpath -o bin/linux-amd64/node-audit-collector ./cmd/node-audit-collector
go build -trimpath -o bin/linux-amd64/vna ./cmd/vna
```
