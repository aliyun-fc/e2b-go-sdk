# AGENTS.md — E2B Go SDK

## 通用约束

- 始终使用中文与用户沟通，包括计划、进度更新、代码审查反馈和最终总结。
- 代码标识符、协议字段、错误码、命令、环境变量和外部 API 名称保持原文，必要时用中文解释。
- 尊重用户已有改动，只修改与当前任务直接相关的文件；不要顺手重构无关代码。

## 任务定位

你的任务是参照 E2B Python SDK（`/home/work/github/E2B/packages/python-sdk`），构建生产可用、符合 Go 惯用模式的官方 E2B Go SDK。

Python SDK 是 API 面、数据面协议和业务逻辑的唯一权威来源。需要验证行为、参数、错误码或数据模型时，先查 Python SDK，再在 Go SDK 中实现等价语义。

## 目标

产出一个生产可用、符合 Go 惯用模式的 SDK，要求：

- 完整镜像 Python SDK 的公开 API：Sandbox 生命周期、文件系统、命令执行、PTY、Git、模板、卷、快照、网络。
- 使用原生 Go 模式：`context.Context`、显式 `error`、接口、函数选项模式。
- 与 E2B 控制面 REST 和数据面 Connect-RPC + envd HTTP 正确通信。
- 保持 README、示例和测试与公开 API 同步。

## 常用验证命令

```sh
go test ./...
go test -count=1 ./...
go test -run TestName -count=1 -v
go vet ./...
```

如果执行环境需要显式 Go 缓存目录：

```sh
GOCACHE=/tmp/e2b-go-sdk-gocache go test ./...
GOCACHE=/tmp/e2b-go-sdk-gocache go vet ./...
```

真实环境验证会创建云端资源，需要 `E2B_API_KEY`：

```sh
go run ./examples/sandbox/main.go
go run ./examples/template/main.go
```

集成测试默认关闭，避免误创建资源：

```sh
E2B_TEMPLATE_INTEGRATION=1 go test -run TestTemplateIntegrationBuildCRUD -timeout 45m -count=1 -v
E2B_TEMPLATE_COPY_INTEGRATION=1 go test -run TestTemplateIntegrationBuildCopy -timeout 45m -count=1 -v
```

## 架构边界

- 仓库是单一扁平 Go 包：根目录为 `package e2b`，除 `examples/` 外不引入子包。
- 控制面 REST 入口在 `client.go`，错误解析在 `errors.go`，Sandbox / Template / Volume 生命周期分别在 `sandbox.go`、`template.go`、`volume.go`。
- 数据面 Connect-RPC 和 envd HTTP 连接逻辑在 `connect.go`；不要绕过已有连接、签名和错误映射 helper。
- `Sandbox` 聚合 `Files`、`Commands`、`Pty`、`Git` 四类运行时模块，相关实现分别在 `filesystem.go`、`commands.go`、`pty.go`、`git.go`。
- 配置读取与函数选项位于 `config.go`；共享模型位于 `types.go`；envd 请求签名位于 `signature.go`。

## 实现约定

- 新增公开能力时优先沿用函数选项模式，避免引入与现有 API 风格不一致的构造方式。
- 保持 `ctx`、客户端请求超时和 envd 执行超时的职责边界，不要把三者混成一个总超时。
- 文件传输遵循 Python SDK 的 idle timeout 语义：握手超时和 body chunk idle timeout 分离，不要给整个 body 强加总 deadline。
- 镜像 Python SDK 行为时，JSON 字段名、错误码、默认值和边界条件必须保持一致。
- Git 相关能力基于 sandbox 内命令执行实现；涉及认证 clone 时，确保 token 不持久化在 `.git/config` 的远端 URL 中。
- 修改公开 API、示例或用户可见行为时，同步检查 README 和测试。
