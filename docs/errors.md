# 错误码与 details.reason

本文件集中维护 `docs/spec.md` 5.5 的错误码表，以及各接口 `error.details` 中出现的全部字段与 `details.reason` 取值。代码中的每个 `details.reason` 都定义为 `internal/protocol` 的 `Reason*` 常量，测试 `TestErrorsDocListsEveryReason` 保证本文件与代码一致。

## 错误响应

```json
{
  "ok": false,
  "request_id": "...",
  "error": { "code": "invalid_request", "message": "...", "retryable": false, "details": { "reason": "path_not_absolute" } }
}
```

- `code` 只取下表的值；HTTP 状态码由 `code` 唯一决定。
- `message` 面向人阅读，内容不保证稳定，调用方不应解析。
- `retryable` 除 `timeout` 外由 `code` 决定；`timeout` 在可重试动作（spec 5.6 的 GET 接口与 `screen/capture`、`file/read|stat|list`）上为 `true`，其余为 `false`。
- `details` 可省略；出现时其字段见下文。

## 错误码

| code | HTTP | retryable | 场景 |
| --- | --- | --- | --- |
| `invalid_request` | 400 | 否 | 参数缺失或非法；`X-Request-Id` 格式不符；命令策略拒绝；URL scheme 不允许；路径不合规；路由存在但方法不对 |
| `not_found` | 404 | 否 | 窗口、进程、文件、会话不存在；路由不存在；命令可执行文件不存在；没有可打开目标的关联程序 |
| `ambiguous` | 409 | 否 | 窗口选择器匹配多个窗口且 `pick=error` |
| `conflict` | 409 | 否 | 文件已存在且未允许覆盖、目标是目录；去重在途；流式响应的重复请求 |
| `too_large` | 413 | 否 | JSON 请求体超过 8 MiB；单次读取超过 64 MiB |
| `unsupported` | 501 | 否 | 本平台或当前能力集不支持（例如阶段 1 的 `exec` 后台模式） |
| `no_display` | 503 | 是 | 图形会话不可用或已锁屏 |
| `busy` | 503 | 是 | 在途请求达到 `-max-concurrency` |
| `timeout` | 504 | 视动作 | 动作超过默认超时或 `X-Timeout-Ms`；调用方中途断开 |
| `io_failed` | 500 | 否 | 文件系统、子进程或桌面 API 调用失败 |
| `internal` | 500 | 否 | 服务内部错误 |

## details.reason 取值

| reason | code | 接口 | 含义 |
| --- | --- | --- | --- |
| `invalid_request_id` | `invalid_request` | 全部 | `X-Request-Id` 不是 16 到 128 个 `[A-Za-z0-9._-]` 字符 |
| `invalid_timeout` | `invalid_request` | 全部 | `X-Timeout-Ms` 不是正整数 |
| `invalid_json` | `invalid_request` | 全部 JSON 请求体接口 | 请求体不是合法 JSON、含未知字段、类型不符，或 batch 动作参数不合法 |
| `unknown_route` | `not_found` | 任意未定义路径 | 没有该路由 |
| `method_not_allowed` | `invalid_request` | 任意已定义路径 | 路径存在但不支持该 HTTP 方法 |
| `in_flight` | `conflict` | 不可重试动作 | 相同 `方法 + 路径 + X-Request-Id` 的首次请求仍在执行（spec 5.6） |
| `dedup_stream` | `conflict` | 不可重试动作 | 首次响应为流式，无法回放（spec 5.6） |
| `client_disconnected` | `timeout` | 全部 | 调用方在动作完成前断开连接；仅在去重回放中可见，`input/batch` 同时给出 `executed` |
| `policy_denied` | `invalid_request` | `exec`、`process/launch` | 命中 `-exec-denylist-file`，或配置了 `-exec-allowlist-file` 而未命中 |
| `url_scheme` | `invalid_request` | `process/open` | `url` 的 scheme 不是 `http`、`https`、`file` |
| `path_not_absolute` | `invalid_request` | `file/*`、`process/open` | 路径不是绝对路径 |
| `outside_root` | `invalid_request` | `file/*` | 路径解析符号链接后不在 `-root` 列出的任何根目录内 |
| `sha256_mismatch` | `invalid_request` | `file/write` | 内容的 sha256 与请求中的 `sha256` 不一致，文件未写入 |
| `permission_denied` | `io_failed` | `file/*`、`exec`、`process/*` | 操作系统拒绝访问 |
| `disk_full` | `io_failed` | `file/write` | 目标卷可用空间低于 500 MiB（spec 7） |
| `region_out_of_bounds` | `invalid_request` | `screen/capture` | `region` 与所选显示器完全不相交 |
| `unknown_key` | `invalid_request` | `input/key/*`、`input/batch` | 键名不在 `docs/keys.md` |
| `foreground_lock` | `io_failed` | `window/activate` | 系统拒绝把窗口置于前台：Windows 依次尝试 `SetForegroundWindow`、`AttachThreadInput`、注入 Alt 后仍失败；Linux 窗口管理器 1 秒内未激活该窗口 |

## details 其他字段

| 字段 | 类型 | 接口 | 含义 |
| --- | --- | --- | --- |
| `reason` | string | 见上表 | 细分原因 |
| `candidates` | 窗口数组 | `window/activate`、`window/close` 等使用选择器的接口，`code=ambiguous` | 匹配到的全部窗口，字段同 `window/list` |
| `executed` | integer | `input/batch` | 已完成的动作数；校验失败时为 0 |
| `index` | integer | `input/batch` | 失败（或校验不通过）的动作下标，从 0 开始 |
| `type` | string | `input/batch` | 失败动作的 `type` |
