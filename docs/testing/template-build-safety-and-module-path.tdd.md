# Template 构建安全与 Go module 路径 TDD 证据

## 来源与用户保障

本次测试保障由缺陷报告直接推导，没有外部 plan 文件：

1. 调用者使用已有模板的 name 或 template ID 发起 rebuild 时，COPY 准备、文件查询、文件上传或 trigger 失败只能返回 `BuildError`，不得隐式删除整个模板。
2. SDK 不使用 `TemplateExists` 作为 build 前置条件，合法的同名 rebuild 仍然可用。
3. 公开仓库的 canonical module path、示例 self-import 和安装文档统一为 `github.com/aliyun-fc/e2b-go-sdk`。

行为基线来自用户指定环境中的 `e2b 2.24.0`：同步和异步 Python SDK 在 prepare、upload、trigger、poll 失败时均只抛出异常，不执行模板删除。当前 Python SDK 源码与 FC Java SDK 行为相同。

## RED / GREEN 记录

模板删除缺陷的 RED 命令：

```sh
GOCACHE=/tmp/e2b-go-sdk-gocache go test -run 'TestBuildTemplate.*DoesNotDeleteExistingTemplate' -count=1 -v
```

修复前新增的 5 个回归场景全部失败，每个场景都观测到：

```text
unexpected DELETE of existing template: calls = 1
```

移除隐式 cleanup 后，同一命令全部通过；扩展边界场景后的 GREEN 命令为：

```sh
GOCACHE=/tmp/e2b-go-sdk-gocache go test -run 'TestBuildTemplate(.*DoesNotDeleteExistingTemplate|RebuildExistingNameWithoutPreflight)' -count=1 -v
```

结果：8 个场景全部 PASS。

module path 缺陷在干净临时 module 中复现为：

```text
module declares its path as: github.com/e2b-dev/e2b-go-sdk
        but was required as: github.com/aliyun-fc/e2b-go-sdk
```

路径迁移后，使用本地 `replace` 的隔离 consumer 成功执行 `go get github.com/aliyun-fc/e2b-go-sdk` 并通过编译。真实远端 `go get` 仍需在修复提交同步到 GitHub 后验证。

## 测试规格

| # | 保证 | 测试或命令 | 类型 | 结果 |
|---|---|---|---|---|
| 1 | 本地 COPY 文件不存在时不删除已有模板 | `TestBuildTemplatePrepareFilesErrorDoesNotDeleteExistingTemplate` | 单元/HTTP 集成 | PASS |
| 2 | COPY 文件查询返回 5xx 时不删除已有模板 | `TestBuildTemplateFileLookupErrorDoesNotDeleteExistingTemplate` | HTTP 集成 | PASS |
| 3 | 文件上传 URL 为空时不删除已有模板 | `TestBuildTemplateEmptyUploadURLDoesNotDeleteExistingTemplate` | HTTP 集成 | PASS |
| 4 | 预签名上传失败时不删除已有模板 | `TestBuildTemplateFileUploadErrorDoesNotDeleteExistingTemplate` | HTTP 集成 | PASS |
| 5 | trigger 返回 API 4xx 时不删除已有模板 | `TestBuildTemplateTriggerErrorDoesNotDeleteExistingTemplate` | HTTP 集成 | PASS |
| 6 | trigger 发生传输错误时不删除已有模板 | `TestBuildTemplateTriggerTransportErrorDoesNotDeleteExistingTemplate` | HTTP 集成 | PASS |
| 7 | trigger context 取消时不删除已有模板 | `TestBuildTemplateTriggerContextCancellationDoesNotDeleteExistingTemplate` | HTTP 集成 | PASS |
| 8 | 同名 rebuild 不执行 exists 预检并正常完成 | `TestBuildTemplateRebuildExistingNameWithoutPreflight` | HTTP 集成 | PASS |
| 9 | module、自引用 import、README 和 lint 路径无旧值残留 | `rg`、`go list ./...`、`go list -m` | 静态/编译 | PASS |
| 10 | 完整 SDK 无测试或竞态回归 | `go test -race -coverprofile=... -count=1 ./...` | 全量 | PASS |
| 11 | 真实控制面把原 `TemplateID` 误作 name rebuild；若随机 ID 以数字开头、不符合 name 语法，则确认其被安全拒绝，并改用唯一 alias 进入 COPY 准备失败路径。两条路径都要求保留原模板和运行中的 sandbox；在 90 秒观察窗口及独立最终 cleanup 中检测、清理基线外的同名孤儿模板 | `TestTemplateFromImageBuildQueryDeleteAndSpawn/failed_rebuild_preserves_template_and_sandbox` | 云端 E2E | 已接入 GitHub Actions，需新加坡 E2E 凭证 |
| 12 | 模板 E2E 仅在可信 `master` 或同仓库 pull request 使用密钥，且 CI 配置语法有效 | PyYAML、`actionlint v1.7.7` | CI 静态验证 | PASS |
| 13 | E2E 轮询在预取消时不发请求，超时/取消保留标准 context cause 并附带最后一次请求错误 | `TestPollUntil*` | 单元 | PASS |

## 覆盖率与已知边界

- 根 SDK 包语句覆盖率：`94.6%`；`BuildTemplate` 与 `BuildTemplateInBackground` 均为 `100.0%`。
- 全仓 profile 因示例入口和默认关闭的真实云端 E2E 未执行，总计 `65.5%`；这些路径不影响本次生产包覆盖率判断。
- 模板 E2E 已改用公共 `runtime/base:v0.0.47` 镜像，并扩展为真实失败 rebuild 安全回归；本地默认仍跳过，可信 `master` 或同仓库 pull request CI 使用独立测试凭证执行。
- 当前在新加坡 from-image 控制面观察到 `WithTemplateTags("from-image-e2e")` 被归一化为 `default`；SDK 请求字段已有单元测试，真实 E2E 记录实际值但不把该控制面差异判为 SDK 失败。
- 当前工作环境未配置 `E2B_API_KEY`，因此没有在本机创建云端资源；真实 E2E 的最终运行结果应由合并后的新加坡 CI job 验收。
- GitHub 上的新提交/tag 尚未发布，因此真实代理链路的最终 `go get @latest` 是发布后的验收项。

## 合并证据

缺陷修复流程要求提交前人工确认，因此本轮没有创建 RED/GREEN checkpoint commit。若后续压缩为单个提交，应在提交或 MR 描述中保留上述 RED/GREEN 证据。
