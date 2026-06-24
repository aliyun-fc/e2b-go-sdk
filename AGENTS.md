# AGENTS.md — E2B Go SDK

你的任务是参照 E2B Python SDK（`/Users/xl/work/github/E2B/packages/python-sdk`），构建**官方 E2B Go SDK**。Python SDK 是 API 面、数据面协议和业务逻辑的唯一权威来源。

## 目标

产出一个生产可用、符合 Go 惯用模式的 SDK，要求：
- 完整镜像 Python SDK 的公开 API（Sandbox 生命周期、文件系统、命令执行、PTY、Git、模板、卷、快照、网络）
- 使用原生 Go 模式（接口、`context.Context`、error 值、函数选项模式）
- 与 E2B 控制面（REST）和数据面（Connect-RPC + HTTP 对接 envd）通信

## 权威来源

Python SDK 位于 `/Users/xl/work/github/E2B/packages/python-sdk`（与本 Go SDK 项目同级）。需要验证行为、参数、错误码或数据模型时，随时查阅。
