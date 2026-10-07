# 协议与任务模型

## 传输

初版接口使用 JSON over HTTP/HTTPS。每个请求都带协议版本、请求 ID 和 Agent 身份；需要较长时间等待任务时使用长轮询。所有生产连接都应使用 TLS，开发环境才允许本机明文 HTTP。

建议的接口分组：

| 目的 | 方向 | 说明 |
| --- | --- | --- |
| 注册 | Agent -> Gateway | 首次启动上报 Agent ID、版本和能力 |
| 心跳 | Agent -> Gateway | 上报在线状态、资源摘要和最后任务进度 |
| 拉取任务 | Agent -> Gateway | 长轮询取得分配给该 Agent 的任务 |
| 提交任务 | CMS/调用方 -> Gateway | 创建任务并返回任务 ID |
| 更新结果 | Agent -> Gateway | 回传状态、退出信息、受限输出和制品引用 |
| 查询任务 | CMS/调用方 -> Gateway | 查询状态、日志摘要和错误原因 |
| 取消任务 | CMS/调用方 -> Gateway | 请求停止尚未开始或正在运行的任务 |

当前版本固定为 `/api/v1`，所有 JSON 请求使用 `protocol_version`、`request_id` 和 UTC `timestamp`。

| 方法 | 路径 | 作用 |
| --- | --- | --- |
| `GET` | `/healthz` | Gateway 健康状态和在线 Agent/任务计数 |
| `GET` | `/metrics` | Prometheus 文本格式的基础计数 |
| `POST` | `/api/v1/agents/register` | 注册或更新 Agent 能力摘要 |
| `POST` | `/api/v1/agents/{agent_id}/heartbeat` | 更新心跳、版本、能力和资源 |
| `POST` | `/api/v1/agents/{agent_id}/offline` | 优雅退出并保持身份注册 |
| `GET` | `/api/v1/agents/{agent_id}` | 查询在线、离线或撤销状态 |
| `POST` | `/api/v1/tasks` | 创建任务，支持 `caller_id` + `idempotency_key` |
| `GET` | `/api/v1/tasks/{task_id}` | 查询任务和结果 |
| `POST` | `/api/v1/tasks/{task_id}` | 取消尚未完成的任务 |
| `POST` | `/api/v1/agents/{agent_id}/tasks/pull` | Agent 获取一个租约任务 |
| `POST` | `/api/v1/tasks/{task_id}/ack` | Agent 确认并进入 `running` |
| `POST` | `/api/v1/tasks/{task_id}/result` | Agent 提交成功或失败结果 |
| `GET` | `/api/v1/tasks/{task_id}/artifacts/{artifact_id}` | 下载未过期且仍可从内联或对象存储取得的制品，返回哈希和 `ETag` |

任务状态由 Gateway 维护：`queued -> dispatched -> running -> succeeded/failed`，取消进入 `cancelled`，租约耗尽进入 `expired`。失败任务在达到 `max_attempts` 前会重新排队。

## 任务状态

任务建议经过以下状态：

```text
queued -> dispatched -> running -> succeeded
                         |       -> failed
                         |       -> cancelled
                         v
                       expired
```

状态转换必须由服务端记录时间和原因。`queued` 表示已接收但未分配，`dispatched` 表示已交给 Agent，`running` 表示 Agent 已开始执行。网络断开时不能简单把任务改成成功；应保留“结果未知”或重连后的补偿查询语义。

## 幂等和重试

- 每个任务有不可变的 `task_id`。
- 创建任务的调用方可以提供 `idempotency_key`，Gateway 在有效窗口内返回同一个任务，而不是重复创建。
- Gateway 可通过 `-artifact-dir` 把制品正文落到独立的持久化卷；任务 JSON 仅保留 `storage_key`、大小、哈希和期限，下载接口会校验哈希并在过期访问时清理对象。
- Gateway 也可通过 `-artifact-s3-endpoint`、`-artifact-s3-bucket` 和两个凭据文件参数接入 S3/MinIO；凭据文件按每次对象操作读取，支持轮换。
- 审计可同时写入本地 JSONL（`-audit-log`）和外部 HTTP 收集器（`-audit-url`，凭据由 `-audit-token-file` 热轮换）。外部收集器失败会让 `/healthz` 进入 degraded，避免静默丢失安全事件。
- Agent 拉取任务时带租约或确认标记；未确认的任务可以在租约过期后重新分配。
- 对具有副作用的任务，重试策略必须由动作类型声明，不能由通用 HTTP 重试库盲目重放。
- 结果回传应可重复提交，Gateway 根据任务 ID 和结果版本去重。

## 动作类型

第一版按明确类型建模，不把所有事情都压成一条 shell 字符串：

1. `command.exec`：参数使用 argv 数组，工作目录、环境变量、超时和输出上限显式传递。
2. `desktop.screenshot`：指定屏幕、窗口或区域，返回制品引用、尺寸和格式。
3. `file.read` / `file.write`：路径、大小、哈希和覆盖策略明确声明。
4. 后续再加入浏览器、鼠标键盘和其他桌面操作，并为每种动作声明能力和风险等级。

## 结果和制品

任务结果应包括状态、开始和结束时间、Agent 版本、错误分类、退出信息和受限输出。小型制品可随结果受限内联并通过制品下载端点读取；过期后内联数据会清除，保留 ID、大小、哈希、媒体类型和过期时间。截图、较大的文件和完整日志接入外部制品存储后，结果只保存制品元数据。
