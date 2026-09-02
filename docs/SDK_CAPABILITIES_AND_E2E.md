# E2B Go SDK 能力清单与 E2E 覆盖

本文档记录当前 Go SDK 的公开能力、对应代码入口和真实环境 E2E 覆盖情况。E2E 测试默认跳过，必须显式设置开关，避免普通 `go test ./...` 误创建云端 sandbox、template 或 volume。

## 运行前提

真实 E2E 需要配置控制面地址和 API key：

```sh
export E2B_API_KEY=***
export E2B_API_URL=https://api.ap-southeast-1.e2b.fc.aliyuncs.com
export E2B_DOMAIN=ap-southeast-1.e2b.fc.aliyuncs.com
```

常用可选变量：

| 变量 | 说明 |
| --- | --- |
| `E2B_E2E_TEMPLATE` | 创建 sandbox 使用的基础模板，默认 `base` |
| `E2B_E2E_KEEP_SANDBOX` | 设为 `1/true/yes/on` 时保留测试创建的 sandbox；模板安全 E2E 会同时保留关联 template，避免删除仍被 sandbox 使用的模板 |
| `E2B_E2E_KEEP_TEMPLATE` | 设为 `1/true/yes/on` 时保留测试创建的 template |
| `E2B_E2E_KEEP_VOLUME` | 设为 `1/true/yes/on` 时保留测试创建的 volume |
| `E2B_TEMPLATE_E2E_IMAGE` | 模板 E2E 的基础镜像，默认公共镜像 `fc-e2b-registry.ap-southeast-1.cr.aliyuncs.com/runtime/base:v0.0.47` |
| `E2B_TEMPLATE_E2E_NAME_PREFIX` | 模板 E2E 名称前缀；测试始终追加随机唯一后缀，不能指定完整旧模板名 |
| `*_TIMEOUT_SECONDS` | 对应 E2E 总超时，单位秒 |
| `*_REQUEST_TIMEOUT_SECONDS` | 对应 E2E 单请求超时，单位秒 |

## E2E 测试矩阵

| 测试 | 文件 | 开关 | 覆盖范围 |
| --- | --- | --- | --- |
| `TestSandboxLifecycle` | `test/e2e/sandbox_lifecycle_test.go` | `E2B_SANDBOX_LIFECYCLE_E2E=1` | 创建、连接、查询状态、延长超时、停止、按 metadata list |
| `TestSandboxRuntimeModules` | `test/e2e/sandbox_runtime_test.go` | `E2B_SANDBOX_RUNTIME_E2E=1` | Commands、Filesystem、Filesystem watch、PTY、Git |
| `TestSandboxAdvancedFeatures` | `test/e2e/sandbox_advanced_test.go` | `E2B_SANDBOX_ADVANCED_E2E=1` | network update、metrics、signed file URL、error mapping、pause/reconnect、snapshot |
| `TestVolumeLifecycleContentAndMount` | `test/e2e/volume_lifecycle_test.go` | `E2B_VOLUME_E2E=1` | Volume 创建、查询、连接、文件 API、metadata、挂载到 sandbox、销毁 |
| `TestTemplateFromImageBuildQueryDeleteAndSpawn` | `test/e2e/template_from_image_test.go` | `E2B_TEMPLATE_E2E=1` | Template from image 构建、查询、创建 sandbox；把原 `TemplateID` 误作 name 发起失败 rebuild（若其以数字开头、不符合控制面 name 语法，则确认被安全拒绝并用唯一 alias 覆盖已有模板失败路径）后，原模板和 sandbox 保持可用；确认停止和归属后显式删除 |
| `TestTemplateIntegrationBuildCRUD` | `template_integration_test.go` | `E2B_TEMPLATE_INTEGRATION=1` | 根包历史 template from image 集成测试 |
| `TestTemplateIntegrationBuildCopy` | `template_integration_test.go` | `E2B_TEMPLATE_COPY_INTEGRATION=1` | Template COPY 构建并用模板创建 sandbox |

快速运行单组：

```sh
E2B_SANDBOX_LIFECYCLE_E2E=1 go test ./test/e2e -run TestSandboxLifecycle -count=1 -v
E2B_SANDBOX_RUNTIME_E2E=1 go test ./test/e2e -run TestSandboxRuntimeModules -count=1 -v
E2B_SANDBOX_ADVANCED_E2E=1 go test ./test/e2e -run TestSandboxAdvancedFeatures -count=1 -v
E2B_VOLUME_E2E=1 go test ./test/e2e -run TestVolumeLifecycleContentAndMount -count=1 -v
E2B_TEMPLATE_E2E=1 go test ./test/e2e -run '^TestTemplateFromImageBuildQueryDeleteAndSpawn$' -count=1 -v -timeout 55m
```

CI 使用同一条定向命令和公共镜像。GitHub job 位于 `.github/workflows/template-e2e.yaml`，允许可信仓库的 `master` push、以 `master` 为目标的同仓库 pull request，以及 `master` 上的手动触发，并从 `e2e-singapore` environment 读取 `E2B_API_KEY_SINGAPORE`；fork pull request 不执行真实云端 job。

完整真实 E2E：

```sh
E2B_SANDBOX_LIFECYCLE_E2E=1 \
E2B_SANDBOX_RUNTIME_E2E=1 \
E2B_SANDBOX_ADVANCED_E2E=1 \
E2B_VOLUME_E2E=1 \
E2B_TEMPLATE_E2E=1 \
go test ./test/e2e -count=1 -v -timeout 90m
```

部分服务端能力在不同环境中可能尚未开通或不稳定：

- `pause/reconnect` 和 `snapshot` 属于 advanced 可选能力；如果控制面返回 404/500/501/503 或 not supported，测试会标记为 skip。
- `Volume` 如果控制面 `/volumes` 返回 `404 page not found`，说明当前环境未开通 volume API，测试会标记为 skip。
- SDK 会发送 `WithTemplateTags` 配置的请求 tag；当前在新加坡区域的 from-image 控制面观察到自定义 tag 被归一到 `default`。E2E 会记录该差异并验证 tag 列表可查询，控制面修复前不强制要求请求 tag 原样返回。

## 能力覆盖清单

### Client / Config

| 能力 | 公开 API | 单元测试 | E2E |
| --- | --- | --- | --- |
| 创建 client | `NewClient` | 已覆盖 | 所有 E2E 间接覆盖 |
| 环境变量配置 | `NewConfig` | 已覆盖 | 所有 E2E 间接覆盖 |
| API key 校验 | `ValidateAPIKey` | 已覆盖 | E2E 使用真实 key |
| 自定义控制面/域名/headers/timeout/http client | `WithAPIURL`、`WithDomain`、`WithHeader`、`WithRequestTimeout`、`WithHTTPClient` 等 | 已覆盖 | 所有 E2E 间接覆盖 API URL / domain / timeout |

### Sandbox 生命周期

| 能力 | 公开 API | 单元测试 | E2E |
| --- | --- | --- | --- |
| 创建 sandbox | `CreateSandbox`、`Client.CreateSandbox` | 已覆盖 | `TestSandboxLifecycle` |
| 模板、env、metadata、secure、internet、network、lifecycle、volume mount 参数 | `WithTemplate`、`WithEnv`、`WithMetadata`、`WithSecure`、`WithInternetAccess`、`WithNetwork`、`WithLifecycle`、`WithVolumeMount` | 已覆盖 | lifecycle/runtime/volume/template E2E 间接覆盖；`WithLifecycle` 暂无真实 E2E |
| 连接 sandbox | `Client.ConnectSandbox`、`Sandbox.Connect` | 已覆盖 | `TestSandboxLifecycle`、`TestSandboxAdvancedFeatures` |
| 停止 sandbox | `Client.KillSandbox`、`Sandbox.Kill` | 已覆盖 | 所有 sandbox 类 E2E 清理路径覆盖 |
| 查询状态和详情 | `Sandbox.IsRunning`、`Sandbox.GetInfo`、`Client.GetSandboxInfo` | 已覆盖 | `TestSandboxLifecycle` |
| 延长超时 | `Sandbox.SetTimeout`、`Client.SetSandboxTimeout` | 已覆盖 | `TestSandboxLifecycle` |
| 列出 sandbox | `Client.ListSandboxes` | 已覆盖 | `TestSandboxLifecycle` metadata query |
| pause / reconnect | `Sandbox.Pause`、`Sandbox.Connect` | 已覆盖 | `TestSandboxAdvancedFeatures` |
| metrics | `Sandbox.GetMetrics`、`Client.GetSandboxMetrics` | 已覆盖 | `TestSandboxAdvancedFeatures` |
| snapshot | `Sandbox.CreateSnapshot`、`Sandbox.ListSnapshots`、`Client.DeleteSnapshot` | 已覆盖 | `TestSandboxAdvancedFeatures` |
| host / MCP URL helpers | `GetHost`、`GetMCPURL` | 已覆盖 | `GetHost` 在 `test/e2e/main.go` 打印；暂无专门断言 |
| FC 受信集成读取 envd token | `SandboxAccessToken` | 已覆盖 | FC 专用扩展，非上游公开 API；敏感凭证不在 E2E 输出或断言 |
| signed file URL | `DownloadURL`、`UploadURL` | 已覆盖 | `TestSandboxAdvancedFeatures` |
| MCP sandbox | `WithMCP`、`MCPToken`、`GetMCPURL` | 已覆盖创建参数和 token helper | 暂无真实 MCP gateway E2E |

### Commands

| 能力 | 公开 API | 单元测试 | E2E |
| --- | --- | --- | --- |
| 同步执行 | `Commands.Run` | 已覆盖 | `TestSandboxRuntimeModules/commands` |
| 后台执行 | `Commands.Start` | 已覆盖 | `TestSandboxRuntimeModules/commands` |
| 连接后台进程 | `Commands.Connect` | 已覆盖 | `TestSandboxRuntimeModules/commands` |
| 列出进程 | `Commands.List` | 已覆盖 | `TestSandboxRuntimeModules/commands` |
| stdout/stderr 流式读取 | `WithWaitStdout`、`WithWaitStderr`、`WithStdoutHandler`、`WithStderrHandler` | 已覆盖 | `TestSandboxRuntimeModules/commands` |
| stdin / close stdin | `SendStdin`、`CloseStdin`、`CommandHandle.SendStdin`、`CommandHandle.CloseStdin` | 已覆盖 | `TestSandboxRuntimeModules/commands` |
| kill | `Commands.Kill`、`CommandHandle.Kill` | 已覆盖 | `TestSandboxRuntimeModules/commands` |
| 非 0 exit error | `CommandExitError` | 已覆盖 | `TestSandboxRuntimeModules/commands` |

### Filesystem

| 能力 | 公开 API | 单元测试 | E2E |
| --- | --- | --- | --- |
| 文本/字节/多文件写入 | `WriteText`、`WriteBytes`、`WriteFiles` | 已覆盖 | `TestSandboxRuntimeModules/filesystem` |
| 文本/字节读取 | `Read`、`ReadBytes` | 已覆盖 | `TestSandboxRuntimeModules/filesystem` |
| 流式读取和 idle timeout | `ReadStream`、`WithFileStreamIdleTimeout` | 已覆盖 | `TestSandboxRuntimeModules/filesystem` |
| 目录创建和列表 | `MakeDir`、`List` | 已覆盖 | `TestSandboxRuntimeModules/filesystem` |
| stat / exists | `GetInfo`、`Exists` | 已覆盖 | `TestSandboxRuntimeModules/filesystem` |
| rename / remove | `Rename`、`Remove` | 已覆盖 | `TestSandboxRuntimeModules/filesystem` |
| gzip / metadata / user / request timeout | `WithGzip`、`WithFileMetadata`、`WithFileUser`、`WithFileRequestTimeout` | 已覆盖 | gzip/metadata 根据 envd 版本在 runtime E2E 中覆盖 |
| watch | `WatchDir`、`WatchHandle.GetNewEvents`、`WatchHandle.Stop` | 已覆盖 | `TestSandboxRuntimeModules/filesystem_watch` |

### PTY

| 能力 | 公开 API | 单元测试 | E2E |
| --- | --- | --- | --- |
| 创建交互 shell | `Pty.Create` | 已覆盖 | `TestSandboxRuntimeModules/pty` |
| 连接 PTY | `Pty.Connect` | 已覆盖 | `TestSandboxRuntimeModules/pty` |
| resize | `Pty.Resize` | 已覆盖 | `TestSandboxRuntimeModules/pty` |
| 发送输入 | `Pty.SendStdin` | 已覆盖 | `TestSandboxRuntimeModules/pty` |
| 流式读取终端输出 | `WithWaitPty` | 已覆盖 | `TestSandboxRuntimeModules/pty` |
| kill | `Pty.Kill`、`CommandHandle.Kill` | 已覆盖 | 单元测试覆盖；真实 E2E 清理失败时会调用 |

### Git

| 能力 | 公开 API | 单元测试 | E2E |
| --- | --- | --- | --- |
| clone | `Git.Clone` | 已覆盖 | `TestSandboxRuntimeModules/git` |
| init / bare init | `Git.Init`、`WithGitInitBare`、`WithGitInitialBranch` | 已覆盖 | `TestSandboxRuntimeModules/git` |
| remote add/get | `Git.RemoteAdd`、`Git.RemoteGet` | 已覆盖 | `TestSandboxRuntimeModules/git` |
| status / branch | `Git.Status`、`Git.Branches`、`Git.CreateBranch`、`Git.CheckoutBranch`、`Git.DeleteBranch` | 已覆盖 | `TestSandboxRuntimeModules/git` |
| add / commit | `Git.Add`、`Git.Commit` | 已覆盖 | `TestSandboxRuntimeModules/git` |
| push / pull | `Git.Push`、`Git.Pull` | 已覆盖 | `TestSandboxRuntimeModules/git` 使用 sandbox 内 bare repo |
| config | `Git.SetConfig`、`Git.GetConfig`、`Git.ConfigureUser` | 已覆盖 | `TestSandboxRuntimeModules/git` |
| credentialed clone/push/pull 安全恢复 remote | `WithGitCloneAuth`、`Git.Push`/`Pull` credential 参数 | 已覆盖 | 暂无真实私有仓库 E2E |
| dangerous auth | `DangerouslyAuthenticate` | 已覆盖 | 暂无真实外部仓库 E2E |

### Template

| 能力 | 公开 API | 单元测试 | E2E |
| --- | --- | --- | --- |
| builder from image | `NewTemplate().FromImage`、`FromDockerImage` | 已覆盖 | `TestTemplateFromImageBuildQueryDeleteAndSpawn` |
| builder from base template | `FromBaseTemplate` | 已覆盖 | `test/e2e/main.go` 的 full/template build 路径覆盖；暂无独立 Go test |
| build steps | `RunCmd`、`SetEnv`、`Workdir`、`User`、`Copy` | 已覆盖 | `TestTemplateIntegrationBuildCopy` 覆盖 COPY；`test/e2e/main.go` full 路径覆盖 RUN/COPY/ENV |
| build template | `BuildTemplate`、`BuildTemplateInBackground`、`WithTemplateAPIHeaders` | 已覆盖，包括 Header 作用域、COPY 上传隔离和失败不隐式删除 | `TestTemplateFromImageBuildQueryDeleteAndSpawn` 覆盖把原 `TemplateID` 误作 name 的真实请求，并在数字开头的 ID 被 name 语法前置拒绝时用唯一 alias 保证覆盖已有模板失败 rebuild |
| build status | `GetBuildStatus`、`GetBuildStatusWithOptions` | 已覆盖，包括后台构建 Header 延续 | `TestTemplateFromImageBuildQueryDeleteAndSpawn` |
| list / get / exists | `ListTemplates`、`GetTemplate`、`TemplateExists` | 已覆盖 | `TestTemplateFromImageBuildQueryDeleteAndSpawn` |
| delete | `DeleteTemplate` | 已覆盖 | `TestTemplateFromImageBuildQueryDeleteAndSpawn` |
| tags | `WithTemplateTags`、`GetTemplateTags`、`AssignTemplateTags`、`RemoveTemplateTags` | 已覆盖请求字段和 tags API | `GetTemplateTags` 已覆盖；新加坡 from-image 自定义 tag 当前观察到返回 `default`，待控制面跟进；assign/remove 暂无真实 E2E |
| 用模板创建 sandbox | `CreateSandbox(WithTemplate(...))` | 已覆盖 | `TestTemplateFromImageBuildQueryDeleteAndSpawn` 验证失败 rebuild 前后 sandbox 都可执行命令 |

### Volume

| 能力 | 公开 API | 单元测试 | E2E |
| --- | --- | --- | --- |
| 创建 / 连接 / 查询 / 列出 / 销毁 | `CreateVolume`、`ConnectVolume`、`GetVolumeInfo`、`ListVolumes`、`DestroyVolume` | 已覆盖 | `TestVolumeLifecycleContentAndMount` |
| volume 文件读取 | `ReadFile`、`ReadFileBytes`、`ReadFileStream` | 已覆盖 | `TestVolumeLifecycleContentAndMount` |
| volume 文件写入 | `WriteFile`、`WriteFileText`、`WriteFileBytes` | 已覆盖 | `TestVolumeLifecycleContentAndMount` |
| list / mkdir / stat / exists / metadata / remove | `List`、`MakeDir`、`GetInfo`、`Exists`、`UpdateMetadata`、`Remove` | 已覆盖 | `TestVolumeLifecycleContentAndMount` |
| uid/gid/mode/force/depth/timeout 选项 | `WithVolumeUID`、`WithVolumeGID`、`WithVolumeMode`、`WithVolumeForce`、`WithVolumeDepth`、`WithVolumeRequestTimeout` | 已覆盖 | mode/depth 在 Volume E2E 中覆盖；其余主要靠单元测试 |
| 挂载到 sandbox | `WithVolumeMount` | 已覆盖 | `TestVolumeLifecycleContentAndMount` |

### 错误映射与协议细节

| 能力 | 公开 API / 类型 | 单元测试 | E2E |
| --- | --- | --- | --- |
| 控制面错误映射 | `APIError`、`AuthenticationError`、`NotFoundError`、`RateLimitError` 等 | 已覆盖 | `TestSandboxAdvancedFeatures/error_mapping` 部分覆盖 |
| 数据面 Connect-RPC 错误映射 | `mapConnectHTTPError`、`mapConnectCode` | 已覆盖 | runtime E2E 间接覆盖 |
| 文件错误映射 | `FileNotFoundError`、`NotEnoughSpaceError` | 已覆盖 | `TestSandboxAdvancedFeatures/error_mapping` |
| 请求签名 | `getSignature`、signed file URL | 已覆盖 | `TestSandboxAdvancedFeatures/signed_file_urls` |
| UTF-8 streaming / envelope decode | command stream helpers | 已覆盖 | runtime E2E 间接覆盖 |

## 当前 E2E 缺口

以下能力已有单元测试或脚本式 e2e 覆盖，但还没有独立真实 Go test，后续可按优先级补齐：

1. `WithMCP` 启动 MCP gateway 并实际访问 MCP endpoint。
2. Template `FromBaseTemplate` + `RunCmd` / `SetEnv` / `Copy` 的独立 `test/e2e` Go test。当前已有 `test/e2e/main.go` full 路径和根包 `E2B_TEMPLATE_COPY_INTEGRATION`。
3. Template `AssignTemplateTags` / `RemoveTemplateTags` 的真实 E2E。当前单元测试覆盖，`test/e2e/main.go` full 路径有 best-effort 验证。
4. Git 真实远程私有仓库认证 clone/push/pull。当前单元测试覆盖 token 不落盘逻辑，runtime E2E 使用 sandbox 内 bare repo。
5. `SandboxLifecycle{OnTimeout:"pause", AutoResume:true}` 的真实超时行为。当前单元测试覆盖请求参数和参数校验。
6. public traffic / `GetHost(port)` 对外 HTTP 服务访问的真实 E2E。当前 network E2E 覆盖 egress，`GetHost` 仅生成 URL。
