# Go Agent Platform

面向虚拟机环境的独立 Agent 平台。它与 GoWebFrame CMS 分开维护：CMS 负责虚拟机生命周期，Agent Platform 负责虚拟机内的操作能力。

## 目标

- 由虚拟机内的 `guest-agent` 通过 HTTP/HTTPS 主动连接 `gateway`，不要求对虚拟机开放入站 SSH 或 Agent 端口。
- 为命令执行、截图、文件传输和后续桌面操作提供统一的任务接口。
- 使用任务 ID、队列、并发上限、超时、取消、输出限制和审计记录，保证长任务和高并发下行为可追踪。
- 对高风险命令进行策略检查并返回明确的阻断原因或人工确认信号。

## 目录

- `cmd/gateway`：中心网关服务。
- `cmd/guest-agent`：安装在虚拟机内的轻量 Agent。
- `internal/protocol`：网关和 Agent 共用的 HTTP 数据模型与版本约束。
- `docs`：项目背景、架构、协议、安全边界、路线图和开发约定。
- `deploy`：服务化和镜像部署样例。

## 当前状态

当前提交建立项目边界和最小可运行入口。后续按以下顺序实现：注册与心跳、任务队列和幂等、命令执行、截图、文件操作、策略与审计、CMS 对接。

## 文档入口

- [项目背景与决策](docs/project-context.md)
- [系统架构](docs/architecture.md)
- [协议与任务模型](docs/protocol.md)
- [安全与运行边界](docs/security.md)
- [开发与协作约定](docs/development.md)
- [实施路线图](docs/roadmap.md)

## 本地运行

```bash
go run ./cmd/gateway -addr :8081
go run ./cmd/guest-agent -gateway http://127.0.0.1:8081
```

## 设计参考

任务协议、幂等、超时、输出上限和后台任务的设计参考了 `/Volumes/workspace/project/rdev` 的经验，但本项目不复用 rdev 的 SSH 传输、Agent 或内部包。
