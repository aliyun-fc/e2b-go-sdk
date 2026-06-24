# E2B Go SDK

E2B Go SDK 是面向 Go 用户的 E2B 官方 SDK。它参考 E2B Python SDK 的 API 面、控制面协议和 envd 数据面协议实现，同时采用 Go 惯用写法：`context.Context`、显式错误返回、函数选项模式和类型化模块。

当前推荐优先使用这些能力：

- Sandbox 生命周期：创建、连接、查询状态、延长超时、停止
- Commands：同步执行、后台执行、流式 stdout/stderr、stdin、kill
- Filesystem：读写文件、流式读取、目录列表、stat、rename、remove、watch
- PTY：创建交互式 shell、发送输入、流式读取终端输出
- Git：clone/init/status/branch/add/commit/push/pull/config
- Template：from image 构建、查询、删除，并用构建出的模板创建 sandbox

`Volume` 和 `Snapshot` 相关 API 正在跟随服务端能力继续完善，暂不放入快速开始示例。

## 安装

在你的 Go 项目中执行：

```sh
go get github.com/e2b-dev/e2b-go-sdk
```

如果是在本仓库内体验示例，先设置 API Key：

```sh
export E2B_API_KEY="e2b_..."
```

中国区用户通常还需要指定控制面地址和 sandbox 域名：

```sh
export E2B_API_URL="https://api.cn-beijing.e2b.fc.aliyuncs.com"
export E2B_DOMAIN="cn-beijing.e2b.fc.aliyuncs.com"
```

## 5 分钟跑通

新建 `main.go`：

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	e2b "github.com/e2b-dev/e2b-go-sdk"
)

func main() {
	ctx := context.Background()

	client, err := e2b.NewClient(
		e2b.WithAPIKey(os.Getenv("E2B_API_KEY")),
		e2b.WithAPIURL("https://api.cn-beijing.e2b.fc.aliyuncs.com"),
		e2b.WithDomain("cn-beijing.e2b.fc.aliyuncs.com"),
	)
	if err != nil {
		log.Fatal(err)
	}

	sandbox, err := client.CreateSandbox(
		ctx,
		e2b.WithTemplate("code-interpreter-v1"),
		e2b.WithTimeout(900),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer sandbox.Kill(context.Background())

	result, err := sandbox.Commands.Run(
		ctx,
		`python3 -c 'print("helloworld")'`,
		e2b.WithCommandTimeout(60*time.Second),
	)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(strings.TrimSpace(result.Stdout))
}
```

运行：

```sh
go run .
```

看到输出：

```text
helloworld
```

## 运行本仓库示例

本仓库提供两个可直接运行的示例。

Sandbox 核心流程：

```sh
go run ./examples/sandbox
```

也支持直接运行入口文件：

```sh
go run ./examples/sandbox/main.go
```

Template 构建流程：

```sh
go run ./examples/template
```

或：

```sh
go run ./examples/template/main.go
```

示例默认使用北京区域：

```text
https://api.cn-beijing.e2b.fc.aliyuncs.com
cn-beijing.e2b.fc.aliyuncs.com
```

如果你要覆盖示例使用的地址，可以设置：

```sh
export E2B_SAMPLE_API_URL="https://api.cn-beijing.e2b.fc.aliyuncs.com"
export E2B_SAMPLE_DOMAIN="cn-beijing.e2b.fc.aliyuncs.com"
export E2B_SAMPLE_TEMPLATE="code-interpreter-v1"
```

## 创建和管理 Sandbox

```go
sandbox, err := client.CreateSandbox(
	ctx,
	e2b.WithTemplate("code-interpreter-v1"),
	e2b.WithTimeout(900),
	e2b.WithMetadata(map[string]string{"app": "demo"}),
	e2b.WithEnv("APP_ENV", "dev"),
	e2b.WithInternetAccess(true),
)
if err != nil {
	return err
}
defer sandbox.Kill(context.Background())

running, err := sandbox.IsRunning(ctx)
if err != nil {
	return err
}
fmt.Println("running:", running)

info, err := sandbox.GetInfo(ctx)
if err != nil {
	return err
}
fmt.Println(info.SandboxID, info.TemplateID, info.State)
```

列出当前 sandbox：

```go
page, err := client.ListSandboxes(ctx, &e2b.SandboxQuery{
	Metadata: map[string]string{"app": "demo"},
}, 10, "")
if err != nil {
	return err
}

for _, item := range page.Items {
	fmt.Println(item.SandboxID, item.State)
}
```

## 执行命令

同步执行：

```go
result, err := sandbox.Commands.Run(
	ctx,
	"python3 -c 'print(1 + 1)'",
	e2b.WithCommandTimeout(60*time.Second),
	e2b.WithCommandRequestTimeout(120*time.Second),
)
if err != nil {
	return err
}
fmt.Println(result.Stdout)
```

后台执行并流式读取输出：

```go
handle, err := sandbox.Commands.Start(ctx, "for i in 1 2 3; do echo tick-$i; sleep 1; done")
if err != nil {
	return err
}

result, err := handle.Wait(ctx, e2b.WithWaitStdout(func(chunk string) {
	fmt.Print(chunk)
}))
if err != nil {
	return err
}
fmt.Println("exit:", result.ExitCode)
```

向进程 stdin 发送数据：

```go
handle, err := sandbox.Commands.Start(ctx, "cat", e2b.WithCommandStdin(true))
if err != nil {
	return err
}

if err := handle.SendStdin(ctx, []byte("hello\n")); err != nil {
	return err
}
if err := handle.CloseStdin(ctx); err != nil {
	return err
}

result, err := handle.Wait(ctx)
```

## 文件系统

```go
dir := "/tmp/e2b-demo"
path := dir + "/hello.txt"

_, err := sandbox.Files.MakeDir(ctx, dir)
if err != nil {
	return err
}

_, err = sandbox.Files.WriteText(ctx, path, "hello from go sdk\n")
if err != nil {
	return err
}

text, err := sandbox.Files.Read(ctx, path)
if err != nil {
	return err
}
fmt.Println(strings.TrimSpace(text))

entries, err := sandbox.Files.List(ctx, dir, e2b.WithListDepth(1))
if err != nil {
	return err
}
fmt.Println("entries:", len(entries))
```

流式读取文件：

```go
stream, err := sandbox.Files.ReadStream(ctx, path)
if err != nil {
	return err
}
defer stream.Close()

data, err := io.ReadAll(stream)
```

监听目录变化：

```go
watcher, err := sandbox.Files.WatchDir(ctx, "/tmp/e2b-demo")
if err != nil {
	return err
}
defer watcher.Stop(ctx)

events, err := watcher.GetNewEvents(ctx)
```

## PTY 交互式终端

```go
pty, err := sandbox.Pty.Create(ctx, e2b.PtySize{Rows: 24, Cols: 80})
if err != nil {
	return err
}

if err := sandbox.Pty.SendStdin(ctx, pty.PID(), []byte("echo pty-ok\nexit\n")); err != nil {
	return err
}

result, err := pty.Wait(ctx, e2b.WithWaitPty(func(chunk []byte) {
	fmt.Print(string(chunk))
}))
if err != nil {
	return err
}
fmt.Println("exit:", result.ExitCode)
```

## Git

初始化仓库并提交：

```go
repo := "/tmp/repo"

if err := sandbox.Git.Init(ctx, repo); err != nil {
	return err
}
if err := sandbox.Git.ConfigureUser(ctx, "E2B Demo", "demo@example.com", "local", repo); err != nil {
	return err
}
_, err := sandbox.Files.WriteText(ctx, repo+"/README.md", "# demo\n")
if err != nil {
	return err
}
if err := sandbox.Git.Add(ctx, repo, []string{"README.md"}, false); err != nil {
	return err
}
if err := sandbox.Git.Commit(ctx, repo, "initial commit", "", "", false); err != nil {
	return err
}
```

克隆公开仓库：

```go
err := sandbox.Git.Clone(ctx, "https://github.com/e2b-dev/E2B.git", "/tmp/e2b")
```

克隆私有仓库：

```go
err := sandbox.Git.Clone(
	ctx,
	"https://github.com/acme/private-repo.git",
	"/tmp/private-repo",
	e2b.WithGitCloneAuth("x-access-token", os.Getenv("GITHUB_TOKEN")),
)
```

SDK 会在认证 clone 成功后把 `origin` 恢复为不包含凭据的 URL，避免 token 留在 `.git/config`。

## Template 构建

从镜像构建模板：

```go
fromImage := "fc-e2b-registry.cn-beijing.cr.aliyuncs.com/swebench/sweb.eval.x86_64.astropy_1776_astropy-12907:latest"
templateName := "my-go-template"

build, err := client.BuildTemplate(
	ctx,
	e2b.NewTemplate().FromImage(fromImage),
	templateName,
	e2b.WithTemplateCPUCount(2),
	e2b.WithTemplateMemoryMB(2048),
	e2b.WithTemplateSkipCache(false),
	e2b.WithTemplatePollPeriod(5*time.Second),
)
if err != nil {
	return err
}
fmt.Println("template_id:", build.TemplateID)
fmt.Println("build_id:", build.BuildID)
```

用构建好的模板创建 sandbox：

```go
sandbox, err := client.CreateSandbox(
	ctx,
	e2b.WithTemplate(templateName),
	e2b.WithTimeout(900),
)
if err != nil {
	return err
}
defer sandbox.Kill(context.Background())

result, err := sandbox.Commands.Run(ctx, `python3 -c 'print("helloworld")'`)
```

列出并删除模板：

```go
templates, err := client.ListTemplates(ctx, "")
if err != nil {
	return err
}
for _, tpl := range templates {
	fmt.Println(tpl.TemplateID, tpl.Aliases, tpl.BuildStatus)
}

deleted, err := client.DeleteTemplate(ctx, build.TemplateID)
if err != nil {
	return err
}
fmt.Println("deleted:", deleted)
```

`Template.Copy(src, dest)` 会自动计算文件 hash、请求上传地址并上传 tar 包。注意：如果当前控制面返回 `steps are not supported`，说明该环境还未开启 template step 构建能力；此时可以先使用 `FromImage(...)` 构建，或联系服务端开启对应能力。

## 配置项

SDK 默认读取这些环境变量：

| 环境变量 | 说明 |
| --- | --- |
| `E2B_API_KEY` | API Key |
| `E2B_API_URL` | 控制面 API 地址 |
| `E2B_DOMAIN` | sandbox 域名 |
| `E2B_SANDBOX_URL` | 覆盖 sandbox 数据面地址，通常只在本地调试使用 |
| `E2B_VALIDATE_API_KEY` | 设为 `false` 可关闭 API Key 格式校验 |
| `E2B_DEBUG` | 设为 `true` 时使用本地调试默认值 |

也可以通过代码显式传入：

```go
client, err := e2b.NewClient(
	e2b.WithAPIKey(os.Getenv("E2B_API_KEY")),
	e2b.WithAPIURL("https://api.cn-beijing.e2b.fc.aliyuncs.com"),
	e2b.WithDomain("cn-beijing.e2b.fc.aliyuncs.com"),
	e2b.WithRequestTimeout(120*time.Second),
)
```

如果你的 API Key 不是 `e2b_...` 格式，可以关闭本地格式校验：

```go
client, err := e2b.NewClient(
	e2b.WithAPIKey("custom-key"),
	e2b.WithValidateAPIKey(false),
)
```

## 常见问题

### `go run ./examples/sandbox/main.go` 报 undefined

当前仓库已支持直接运行入口文件：

```sh
go run ./examples/sandbox/main.go
go run ./examples/template/main.go
```

也可以使用包路径运行：

```sh
go run ./examples/sandbox
go run ./examples/template
```

### DNS 或连接失败

确认 `E2B_API_URL` / `E2B_DOMAIN` 或示例专用的 `E2B_SAMPLE_API_URL` / `E2B_SAMPLE_DOMAIN` 指向同一个区域。例如北京：

```sh
export E2B_SAMPLE_API_URL="https://api.cn-beijing.e2b.fc.aliyuncs.com"
export E2B_SAMPLE_DOMAIN="cn-beijing.e2b.fc.aliyuncs.com"
```

### Template 示例会创建资源吗

会。`examples/template` 会创建一个模板，再用它启动 sandbox 验证 `helloworld`，最后默认删除模板和 sandbox。

如果你想保留模板用于调试：

```sh
export E2B_SAMPLE_KEEP_TEMPLATE=1
go run ./examples/template
```

### 命令超时和请求超时有什么区别

- `WithCommandTimeout(...)` 是发送给 envd 的命令执行/连接超时。
- `WithCommandRequestTimeout(...)` 是客户端请求等待超时。
- `ctx` 用于调用方主动取消等待，例如用户关闭请求或上层服务超时。

## 开发和验证

本地检查：

```sh
go test ./...
go vet ./...
```

如果你的环境需要指定 Go 缓存目录：

```sh
GOCACHE=/private/tmp/e2b-go-sdk-gocache go test ./...
GOCACHE=/private/tmp/e2b-go-sdk-gocache go vet ./...
```

真实环境验证：

```sh
go run ./examples/sandbox/main.go
go run ./examples/template/main.go
```

Template 集成测试默认关闭，避免误创建资源。需要真实验证时显式打开：

```sh
E2B_TEMPLATE_INTEGRATION=1 go test -run TestTemplateIntegrationBuildCRUD -timeout 45m -count=1 -v
```

COPY 构建验证：

```sh
E2B_TEMPLATE_COPY_INTEGRATION=1 go test -run TestTemplateIntegrationBuildCopy -timeout 45m -count=1 -v
```

如果当前控制面未开启 template step 构建能力，该测试会被跳过并清理已创建的临时模板。
