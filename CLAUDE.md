# CLAUDE.md

本文件为 Claude Code 在本仓库工作时使用的项目上下文。

## 工作约束

- 始终使用中文与用户沟通，包括计划、进度更新、代码审查反馈和最终总结。
- 代码标识符、协议字段、错误码、命令、环境变量和外部 API 名称保持原文，必要时用中文解释。
- Python SDK 是 API 面、数据面协议和业务逻辑的唯一权威来源；实现细节不确定时，查阅 `/home/work/github/E2B/packages/python-sdk`。
- 优先保持 Go SDK 的公开 API 与 Python SDK 行为一致，同时使用 Go 惯用模式表达：`context.Context`、显式 `error`、接口和函数选项。

## 项目概览

这是官方 E2B Go SDK（`github.com/aliyun-fc/e2b-go-sdk`）。仓库采用单一扁平 Go 包：根目录全部是 `package e2b`，除 `examples/` 外没有子包；文件按业务域拆分。

主要能力：

- Sandbox 生命周期：创建、连接、查询状态、延长超时、停止、列表、快照。
- Filesystem：读写、流式传输、目录列表、stat、rename、remove、watch。
- Commands / PTY：同步命令、后台命令、stdout/stderr 流式输出、stdin、kill、交互式终端。
- Git：clone/init/status/branch/add/commit/push/pull/config，基于 `Commands` 实现。
- Template / Volume：控制面资源管理和真实环境验证。

## 常用命令

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

统一格式化与静态检查（通过 `Taskfile.yml`，需要 [go-task](https://taskfile.dev/)）：

```sh
task fmt     # gofmt 统一格式化
task lint    # gofmt 校验 + go vet + golangci-lint（v1.64.8）
task test    # go test ./...
task cover   # 库包覆盖率并校验下限（默认 90%，与 CI 一致）
task check   # 提交前全量检查：lint + cover
```

启用提交前钩子（自动 `task fmt` 并 `task lint`）：

```sh
git config core.hooksPath .githooks
```

CI 定义在 `.github/workflows/ci.yaml`（push/pull_request 到 `master` 触发），
`go-sdk-lint` 与 `go-sdk-ut` 两个 job 分别对应上面的 lint 与 test，
`go-sdk-e2e-singapore` 跑新加坡真实环境 E2E（`go run ./test/e2e`，需 `E2B_API_KEY_SINGAPORE` secret）；
`go-sdk-ut` 强制库包语句覆盖率 >= `MIN_COVERAGE`（默认 90%，examples 薄 CLI 不计入）。
lint 规则集中在 `.golangci.yml`，三处（Taskfile、CI、hook）共用同一版本。

真实环境验证会创建云端资源，需要设置 `E2B_API_KEY`：

```sh
go run ./examples/sandbox/main.go
go run ./examples/template/main.go
```

示例逻辑位于 `examples/internal/sample`，入口文件只是薄 CLI。示例默认使用北京区域，可通过 `E2B_API_URL`、`E2B_DOMAIN`、`E2B_SAMPLE_TEMPLATE` 覆盖。

集成测试默认关闭，避免误创建资源；需要真实验证时显式打开：

```sh
E2B_TEMPLATE_INTEGRATION=1 go test -run TestTemplateIntegrationBuildCRUD -timeout 45m -count=1 -v
E2B_TEMPLATE_COPY_INTEGRATION=1 go test -run TestTemplateIntegrationBuildCopy -timeout 45m -count=1 -v
```

## 架构

SDK 有两层协议，均基于 `net/http`：

1. 控制面 REST：`client.go`。`Client` 持有配置和 `*http.Client`，所有 REST 调用经过 `Client.do*` helper，只在目标为 API URL 时注入 `X-API-KEY` / `Authorization`，非 2xx 响应由 `parseAPIError` 转换。
2. 数据面 Connect-RPC + envd HTTP：`connect.go`。`Sandbox` 相关方法访问运行中 sandbox 内的 envd agent。`connectUnary` 和 `connectServerStream` 手写 Connect 协议 JSON envelope、`Connect-Protocol-Version: 1` 和 keepalive ping header；流式响应由 `connectStream` 解码 length-prefixed envelope。

入口流程：`NewClient(opts...)` 创建 `*Client`，`client.CreateSandbox(ctx, opts...)` 返回 `*Sandbox`。`Sandbox` 保存 envd 连接 token / URL，并在构造时挂载领域模块：

- `sandbox.Files`：`filesystem.go`、`watch.go`
- `sandbox.Commands`：`commands.go`
- `sandbox.Pty`：`pty.go`
- `sandbox.Git`：`git.go`

控制面能力主要在 `sandbox.go`、`template.go`、`volume.go`。公共配置和模型位于 `config.go`、`types.go`，错误类型位于 `errors.go`，envd 签名在 `signature.go`，envd 版本门控在 `version_compare.go`。

## 实现约定

- 新增公开 API 优先使用函数选项模式，例如 `WithAPIKey`、`WithTemplate`、`WithTimeout`、`WithCommandTimeout`。
- 保持三类超时语义独立：`WithCommandTimeout` 是 envd 侧执行超时，`WithCommandRequestTimeout` 是客户端请求等待超时，`ctx` 是调用方取消信号。
- 文件传输超时必须保持 Python 语义（见 `idle_timeout.go`）：
  - `WithFileRequestTimeout` 只限制握手；显式传 `0` 表示禁用（对齐 Python `request_timeout=0 -> None`），不传则回落到 client 的 `RequestTimeout`。为区分「未设置」与「显式 0」，`fileOptions`/`watchOptions` 用 `requestTimeout` + `requestTimeoutSet` 两个字段，经 `handshakeTimeout()` 转成 `*time.Duration`。
  - 「显式 0 禁用」在整条 filesystem API 上必须一致，包括 Connect-RPC 路径（`List`/`GetInfo`/`Remove`/`Rename`/`MakeDir`/`WatchDir`）：`connectUnary` 接收 `*time.Duration`，由 `Config.resolveTimeout`（nil→全局，非 nil→显式含 0）解析。非 filesystem 路径（commands/pty、WatchHandle 的 `Stop`/`GetNewEvents` 等可变参数接口）用 `optionalTimeout`（`0→nil`）保留旧的「0 表示未设置、回落全局」语义。
  - 读的 body 由每个 chunk 的 idle timeout 控制（`WithFileStreamIdleTimeout`，默认等于 request timeout，`0` 禁用）；该 idle **只在等待 wire 数据时计时**（`idleResponseBody` 每次 Read 前 arm、返回后 pause），慢消费者在两次 Read 之间的处理不计入，否则会误判超时。
  - `WithFileStreamIdleTimeout` 仅作用于读/流；写入的 body 与等响应头始终由 request timeout 约束（`idleRequestBody` 不 pause，以捕获 socket 写阻塞）。
  - 不要在 `Filesystem.fileRequest` 重新引入覆盖整个 body 的总 deadline。
- 配置优先级为：显式 option → 环境变量 → debug 默认值 → 生产默认值（`https://api.<domain>`）。
- 主要环境变量：`E2B_API_KEY`、`E2B_API_URL`、`E2B_DOMAIN`、`E2B_SANDBOX_URL`、`E2B_VALIDATE_API_KEY`、`E2B_DEBUG`、`E2B_ACCESS_TOKEN`。
- API Key 本地格式校验匹配 `\Ae2b_[0-9a-f]+\z`；自定义 key 可通过 `WithValidateAPIKey(false)` 关闭。
- 镜像 Python SDK 行为时，JSON 字段名、错误码和边界条件必须保持一致；重点检查 `json` tag、`parseAPIError` 和 `mapConnectHTTPError`。
- Git 认证 clone 成功后，应继续避免 token 留在 `.git/config` 的 `origin` URL 中。

## 注意事项

- `Volume` 和 `Snapshot` API 仍在跟随服务端能力完善，不放入快速开始示例。
- Template step / COPY 构建可能被未开启能力的控制面拒绝并返回 `steps are not supported`；相关集成测试应跳过并清理临时资源，而不是硬失败。
- README 是中文用户入口；修改公开示例或行为时，同步检查 README 是否需要更新。
