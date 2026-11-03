# 开发与协作约定

## 工作副本

- GitHub：`git@github.com:CIPFZ/go-agent-platform.git`，作为正式源仓库。
- Ubuntu：`/home/ytq/work/go-agent-platform`，用于 Linux 构建、QEMU/VM 验证和服务部署。
- 本机：`/Volumes/workspace/project/ubuntu-home/go-agent-platform`，用于编辑、评审和准备提交。

两份工作副本都跟踪 `main`。开始工作前先同步；完成后先在本机或 Ubuntu 运行验证，再提交并推送。远程工作副本只通过 rdev 操作，不直接绕过既有的远程开发通道。

## 分支和提交

- 小步提交，每个提交只解决一个明确问题。
- 提交信息使用简短的动词开头，例如 `feat: add agent heartbeat`、`fix: bound command output`。
- 协议、状态机和安全策略的变化要同时更新 `docs`。
- 不把密钥、VM 镜像、临时截图、日志和本地 `.env` 提交到仓库。

## 常用验证

```bash
gofmt -w ./cmd ./internal
go test ./...
go vet ./...
go run ./cmd/gateway -addr :8081
```

当前骨架只有 Gateway 健康接口和 Agent 启动入口；注册、任务和截图接口在对应路线图阶段实现后再补集成测试。

## 变更顺序

实现新动作时按以下顺序：先写协议和错误模型，再写 Gateway 状态流转，然后写 Agent 执行器，最后接 CMS。每个动作都要同时说明能力检测、超时、取消、输出/制品限制和审计字段。

## 与 CMS 的协作

CMS 通过 Gateway API 依赖 Agent，不直接 import 本仓库的内部包。需要共享类型时，优先使用明确版本的 HTTP schema 或生成的客户端，避免两个仓库通过复制内部 Go 文件形成隐式耦合。
