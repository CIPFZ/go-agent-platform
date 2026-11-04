# 协议

`vm-agent` 的 HTTP 协议概要。逐接口的字段定义以 [`openapi.yaml`](openapi.yaml) 为准；错误码与 `details` 见 [`errors.md`](errors.md)；键名见 [`keys.md`](keys.md)。Go 调用方可直接使用生成的客户端 [`pkg/client`](../pkg/client)。

## 传输

- HTTP/1.1，路径前缀 `/v1`（协议版本，与服务版本独立）。每个响应带 `X-Agent-Version`。
- JSON 请求体上限 8 MiB，拒绝未知字段，空请求体视为 `{}`。`file/upload` 使用 multipart，上限 1 GiB。
- 服务器参数：`ReadHeaderTimeout` 10 秒、`IdleTimeout` 120 秒、`MaxHeaderBytes` 64 KiB。
- 当前不鉴权、不加密（spec D9），由网络层隔离端口。

## 请求头

| 头 | 说明 |
| --- | --- |
| `X-Request-Id` | 可选，16 到 128 个 `[A-Za-z0-9._-]`；用于审计关联与去重 |
| `X-Timeout-Ms` | 可选，覆盖默认超时；一般接口截断到 600000，`file/download`、`file/upload`、`record/{id}` 下载截断到 3600000 |

## 响应

成功：

```json
{ "ok": true, "request_id": "...", "elapsed_ms": 42, "result": { } }
```

失败（HTTP 状态由 `code` 决定）：

```json
{ "ok": false, "request_id": "...", "error": { "code": "not_found", "message": "...", "retryable": false, "details": { } } }
```

流式响应（`screen/capture` 与 `file/read` 的 `encoding=stream`、`record/{id}`、`metrics`）：body 为原始内容，`Content-Type` 为实际类型，元数据在 `X-Result-Width`、`X-Result-Height`、`X-Result-Sha256`、`X-Result-Size`、`X-Cursor-X`、`X-Cursor-Y`、`X-Captured-At`、`X-File-Mtime`。流式请求出错时仍返回 JSON 错误信封。

列表类结果统一包一层对象，例如 `{"windows":[...]}`、`{"jobs":[...]}`、`{"sessions":[...]}`。

## 坐标系

物理像素，以主显示器左上角为原点；副显示器坐标可为负。Windows 进程声明 Per-Monitor V2 DPI 感知，`probe` 上报实际级别。`display` 0 为主显示器，`-1` 为整个虚拟桌面。

## 重试与去重

| 类别 | 接口 |
| --- | --- |
| 可重试 | 全部 GET，以及 `screen/capture`、`file/read`、`file/stat`、`file/list` |
| 不可重试 | 其余全部 POST 与 DELETE |

不可重试动作携带 `X-Request-Id` 时按 `方法 + 路径 + request_id` 去重（`-dedup-window`，默认 60 秒，上限 10000 条）：

- 首次请求仍在执行：`409 conflict`，`details.reason=in_flight`，不并发执行。
- 首次请求已完成：HTTP 200 原样回放首次响应（包括首次的错误），附 `X-Dedup-Hit: true`。
- 首次响应是流式：`409 conflict`，`details.reason=dedup_stream`。
- 调用方在执行中断开：结果记为 `timeout`、`details.reason=client_disconnected`，同 id 重试得到该回放，不会重复执行。`input/batch` 整体使用一个 id。

## 并发

只读动作并发执行；全部 `input/*` 在服务内串行，排队时间计入该请求的超时，排队中超时的请求不会被执行。全局在途请求超过 `-max-concurrency` 返回 `503 busy`。

## 默认超时（spec 第 7 节）

| 动作 | 默认超时 |
| --- | --- |
| `screen/*`、`window/*`、`clipboard` | 5 秒 |
| `input/*` 单动作 | 5 秒加 `duration_ms`（`key/type` 加输入耗时） |
| `input/batch` | 各动作默认超时之和加 5 秒，最多 60 秒 |
| `exec` 同步 | 30 秒；后台模式请求本身 5 秒 |
| `file/*` | 30 秒；`file/read`、`file/remove` 60 秒；`file/download`、`file/upload` 10 分钟 |
| `process/*` | 10 秒，`kill` 另加 `grace_ms` |
| `exec/jobs/*` | 5 秒 |
| `record/start`、`record/stop` | 10 秒；`record/{id}` 下载 10 分钟 |
| `probe`、`capabilities`、`metrics`、`file/known_dirs` | 5 秒 |
| `a11y/tree` | 15 秒 |

## 接口一览

| 组 | 接口 |
| --- | --- |
| 系统 | `GET /v1/probe`、`GET /v1/capabilities`、`GET /v1/metrics` |
| 屏幕 | `POST /v1/screen/capture`、`GET /v1/screen/info`、`GET /v1/screen/cursor` |
| 输入 | `POST /v1/input/mouse/{move,click,down,up,drag,scroll}`、`POST /v1/input/key/{type,press,down,up,hotkey}`、`POST /v1/input/batch` |
| 窗口 | `GET /v1/window/list`、`GET /v1/window/active`、`POST /v1/window/{activate,close,resize,close_all}` |
| 命令与进程 | `POST /v1/exec`、`GET /v1/exec/jobs`、`GET|DELETE /v1/exec/jobs/{job_id}`、`POST /v1/exec/jobs/{job_id}/cancel`、`POST /v1/process/{launch,open,kill}`、`GET /v1/process/list` |
| 文件 | `POST /v1/file/{read,write,upload,stat,list,mkdir,remove,move,copy,download}`、`GET /v1/file/known_dirs` |
| 剪贴板 | `GET /v1/clipboard`、`POST /v1/clipboard` |
| 录屏 | `POST /v1/record/{start,stop}`、`GET /v1/record/list`、`GET|DELETE /v1/record/{session_id}` |
| 无障碍 | `POST /v1/a11y/tree`（阶段 3） |

无桌面会话或已锁屏时，`capabilities.actions` 不含 `screen`、`input`、`window`、`clipboard`、`record`，这些接口返回 `503 no_display`（可重试）。ffmpeg 不可用时不含 `record`，`record/start` 返回 `unsupported`。
