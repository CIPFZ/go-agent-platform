# 架构

本文描述 `vm-agent` 的实现结构。设计依据是 [`spec.md`](spec.md)；协议细节见 [`protocol.md`](protocol.md)、[`openapi.yaml`](openapi.yaml)、[`errors.md`](errors.md)、[`keys.md`](keys.md)。

## 运行形态

```text
调用方 (AI Agent / 自动化程序)
        │  HTTP/1.1, JSON, 同步请求响应 (/v1)
        ▼
┌───────────────── vm-agent（VM 内，桌面用户身份，交互式会话）────────────────┐
│ recover → observe(审计/版本头/request_id) → 请求头校验 → 鉴权位(直通)        │
│        → 并发上限(busy) → 路由 → 去重(不可重试动作) → 处理器                 │
│                                                                            │
│ internal/api      处理器：解码、默认超时、no_display 判定、审计参数摘要        │
│ internal/actions  screen input window clipboard exec process file record   │
│                   session a11y                                            │
│ internal/platform x11（xgb 长连接）  win（user32/gdi32/... 惰性加载）          │
└────────────────────────────────────────────────────────────────────────────┘
        │ 子进程：exec / 后台 job / ffmpeg 在进程树内（Job Object / 进程组）
        │         process/launch 脱离进程树
        ▼
     桌面会话（X11 / Windows 交互式桌面）
```

- 单进程、单二进制。Windows 由计划任务以 `-supervise` 启动：父进程只负责拉起子进程（5 秒后重启，60 秒内 10 次退出则等待 60 秒），子进程运行服务。Linux 由 systemd 用户单元负责重启。
- 没有队列与调度：每个请求在自己的 goroutine 中同步执行并应答一次。长操作（后台命令、录屏）用服务内的在途表加磁盘元数据表示，按 `job_id`、`session_id` 查询与收尾。

## 代码结构

| 路径 | 职责 |
| --- | --- |
| `cmd/vm-agent` | 参数解析、审计 sink、HTTP 服务器、优雅关闭、`-supervise` |
| `internal/config` | spec 4.3 的全部参数与默认值 |
| `internal/server` | 中间件链、信封、错误映射、超时、去重接入、`metrics` 计数 |
| `internal/dedup` | `方法 + 路径 + request_id` 的在途集合与 LRU（窗口 60 秒，上限 10000） |
| `internal/api` | 路由表与处理器；把动作包装为 spec 第 7 节的默认超时 |
| `internal/actions/*` | 各能力域：`<domain>.go` 定义接口与服务逻辑，`_linux.go` / `_windows.go` 为平台后端 |
| `internal/platform/x11` | 共享 X 连接（断线重连、原子缓存、显示器枚举、坐标换算） |
| `internal/platform/win` | Win32 声明（DPI、显示器、光标、SendInput、窗口、会话） |
| `internal/imaging/jpeg` | 由标准库移植的并行基线 JPEG 编码器（分条带 + RSTn），保证截图延迟 |
| `internal/async` | 把可能阻塞的平台调用放到可放弃的 goroutine 中执行 |
| `internal/audit` | JSONL 审计，按大小轮转 |
| `internal/policy` | 可选的命令 allowlist / denylist |
| `internal/supervisor` | `-supervise` 父进程与锁文件 |
| `pkg/client` | 由 `docs/openapi.yaml` 生成的 Go 客户端（`tools/clientgen`，独立模块） |
| `deploy/` | 安装、升级、回滚、前置条件、回归与 CI 脚本 |
| `test/` | X11 集成测试、测试窗口（`xapp`、`winapp`）、稳定性压测工具 |

## 请求处理

1. `observe` 记录到达时间、分配或接受 `X-Request-Id`，响应写出后写一条审计记录（时间、request_id、来源、动作、参数摘要、结果码、耗时、是否去重命中）。
2. 请求头校验：`X-Request-Id` 格式、`X-Timeout-Ms` 解析（一般接口截断到 10 分钟，传输类到 1 小时）。
3. 鉴权位：当前唯一实现 `server.Passthrough`（spec 5.2、D9）。
4. 并发上限：超过 `-max-concurrency` 返回 `busy`。
5. 路由后，不可重试动作若带 `X-Request-Id` 先查去重：在途返回 `conflict/in_flight`；已完成以 HTTP 200 回放首次响应并加 `X-Dedup-Hit: true`；流式首次响应的重复请求返回 `conflict/dedup_stream`。
6. 处理器以 `Call.Context(默认超时)` 取得截止时间（从到达算起，排队时间计入），超时映射为 `timeout`，可重试动作的 `retryable` 为 true。客户端断开会取消上下文。

## 能力域实现要点

| 域 | Linux X11 | Windows |
| --- | --- | --- |
| 屏幕 | kbinani/screenshot（xgb GetImage / MIT-SHM），XFixes 光标图像 | kbinani/screenshot（GDI BitBlt），`DrawIconEx` 黑白两次绘制恢复光标 alpha |
| 输入 | XTest；布局中没有的字符临时绑定空闲 keycode，请求结束恢复 | 自封装 `SendInput`：`MOUSEEVENTF_ABSOLUTE|VIRTUALDESK` 归一化，`KEYEVENTF_UNICODE` 文本 |
| 窗口 | EWMH（`_NET_CLIENT_LIST_STACKING`、`_NET_ACTIVE_WINDOW`、`_NET_CLOSE_WINDOW`、`_NET_WM_STATE`、`_NET_MOVERESIZE_WINDOW`） | `EnumWindows`、DWM 扩展边框、spec 6.4 的前台激活序列 |
| 剪贴板 | 纯 Go X selection owner / requestor，大数据走 INCR | 专用线程 + message-only 窗口，PNG 注册格式优先，写入同时放 CF_DIB |
| 命令 | 进程组 + `Pdeathsig`；进程树根记录在 `-job-dir/.trees`，崩溃后下次启动回收 | 挂起创建 → 加入 `KILL_ON_JOB_CLOSE` 的 Job → 恢复线程 |
| 启动程序 | `setsid` | `DETACHED_PROCESS|CREATE_NEW_PROCESS_GROUP`，可能时 `CREATE_BREAKAWAY_FROM_JOB` |
| 打开 | 纯 Go 的 xdg-open 等价实现（shared-mime-info、mimeapps.list、desktop entry） | `ShellExecuteEx` |
| 录屏 | ffmpeg x11grab + libx264 | ffmpeg gdigrab + libx264（`__COMPAT_LAYER=HighDpiAware`） |
| 会话 | login1 `LockedHint`（godbus） | `OpenInputDesktop`、注册表 UAC、`WTSClientProtocolType` |

## 生命周期

- 启动：声明 Per-Monitor V2 DPI 感知（Windows）→ 打开审计 → 回收上次崩溃遗留的命令树（Linux）→ 载入 job 目录（仍为 `running` 的标记为 `killed/agent_restart`）与录像目录 → 探测 ffmpeg → 检查磁盘下限 → 监听。
- 后台维护：每分钟按保留期与上限清理 job 与录像。
- 关闭（spec 4.4）：停止接受新请求并等待在途请求最多 10 秒 → 停止进行中的录屏（向 ffmpeg 写 `q`，最多 5 秒）→ 终止后台 job（`killed/agent_shutdown`）→ 释放进程树 → 写关闭审计。`process/launch` 启动的程序不受影响。

## 状态与文件

| 内容 | 位置 | 持久化 |
| --- | --- | --- |
| 审计 | `-audit-log`，按 `-audit-max-size` 轮转、保留 `-audit-max-files` 个 | 是 |
| 后台 job | `-job-dir/<job_id>/{job.json,stdout,stderr}`；元数据先写盘再更新内存 | 是，保留 `-job-retention`、最多 1000 个 |
| 录像 | `-record-dir/<session_id>.{mp4,json}` | 是，受 `-record-quota` 与 `-record-retention` 约束 |
| 去重缓存、在途表 | 内存 | 否 |
| 锁文件与 supervisor 日志 | `-job-dir` 上级目录的 `vm-agent.lock`、`vm-agent.log` | 是 |
