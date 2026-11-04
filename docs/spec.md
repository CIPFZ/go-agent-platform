# go-agent-platform 规格说明

状态：待评审。本文档定义 go-agent-platform 下一阶段的目标、架构、协议和实施范围，评审通过后替代 `architecture.md`、`protocol.md`、`security.md` 中与之冲突的内容。

## 0. 决策记录

以下决策是全文的根基，评审时如有异议请在此处提出。

| 编号 | 决策 | 理由 |
| --- | --- | --- |
| D1 | 所有坐标使用物理像素；进程启动时声明 Per-Monitor V2 DPI 感知 | 截图库返回物理像素，若坐标用逻辑像素，高缩放比下点击会偏移 |
| D2 | 输入、命令、启动、写文件类动作不可自动重试；服务端按 `request_id` 在 60 秒窗口内去重 | 输入动作非幂等，超时重试会导致重复点击或重复写入 |
| D3 | Windows 零运行时依赖；Linux 仅依赖运行中的 X11 服务器，不依赖外部命令行工具；录屏两端均依赖 ffmpeg | 单二进制交付，Linux 纯 Go X11 实现量可接受；Wayland 延后 |
| D4 | 同步命令、后台 job 与录屏子进程纳入 Job Object 或进程组；`process/launch` 启动的程序脱离 Job，不随服务退出 | 用户程序必须在服务重启后存活，而命令与 ffmpeg 不能成为孤儿 |
| D5 | 请求超时通过请求头传递；流式响应用裸 body 加元数据响应头 | GET 无请求体；流式 body 无法包裹 JSON 信封 |
| D6 | 阶段 1 只覆盖 Windows 与 Linux X11，Wayland 整体进入阶段 3 | Wayland 的输入与截图需要 portal 授权，路径未验证 |
| D7 | 命令只接受 `argv` 数组，不提供 shell 字符串字段 | shell 语法可由调用方把解释器放入 argv 首项表达，单一形式减少协议分支与转义歧义 |
| D8 | 接口全部同步；长时间操作用"立即返回标识、按标识查询与收尾"处理，不引入队列 | 保持一次请求一次结果的简单模型，同时避免长连接挂起导致结果丢失 |
| D9 | 当前阶段不实现鉴权与 TLS。`vm-agent` 是 VM 内执行服务，调用方是平台；是否需要鉴权取决于部署网络拓扑，由部署方决定。代码中预留 HTTP 中间件位置，当前唯一实现为直通。凭据生命周期、用户权限、API 密钥等均为平台职责，不在本项目范围 | 先交付服务功能本身；避免为尚未确定的部署形态过早设计 |

## 1. 目标

go-agent-platform 是一个运行在虚拟机内的 Agent 服务（下文称 `vm-agent`），让外部的 AI Agent 或自动化程序能够像一个真人坐在这台机器前一样操作它：看屏幕、动鼠标键盘、开关窗口、启动程序、读写文件、执行命令、录屏。

核心要求：

1. 调用方只需知道 VM 的地址、端口和凭据，即可同步调用任意动作并立即得到结果。
2. 动作集覆盖桌面 Agent 推理循环所需的全部观测与操作能力。
3. 单二进制、纯 Go。Windows 无运行时依赖；Linux 依赖 X11 服务器；录屏依赖预装 ffmpeg。
4. Windows 与 Linux X11 桌面环境同等支持。
5. 服务自身可靠：超时、取消、输出边界、进程清理、审计、热轮换凭据、可升级可回滚。

## 2. 能力需求

桌面 Agent 的工作方式是一个循环：观测屏幕，推理下一步，执行动作，再观测。服务需要提供的能力按此划分为观测、操作、环境三类。

| 类别 | 能力 | 说明 |
| --- | --- | --- |
| 观测 | 截图 | 全屏或区域，可叠加光标，返回像素尺寸与时间戳 |
| 观测 | 屏幕信息 | 显示器数量、分辨率、DPI 缩放、光标位置 |
| 观测 | 窗口列表 | 标题、类名、进程、矩形、是否前台 |
| 观测 | 剪贴板读取 | 文本与图像 |
| 观测 | 无障碍树 | 控件层级、角色、名称、坐标，可选能力 |
| 操作 | 鼠标 | 移动、点击、多击、右键、按下、抬起、拖拽、滚轮 |
| 操作 | 键盘 | 文本输入、单键、按下、抬起、组合键 |
| 操作 | 窗口 | 激活、关闭、最大化最小化还原、全部关闭 |
| 操作 | 剪贴板写入 | 文本与图像 |
| 操作 | 批量动作 | 一次请求顺序执行多个输入动作 |
| 环境 | 命令执行 | 同步等待退出，收集输出 |
| 环境 | 程序启动 | 不等待退出，用于 GUI 程序 |
| 环境 | 打开文件或 URL | 使用系统默认关联程序 |
| 环境 | 进程管理 | 列举、终止 |
| 环境 | 文件 | 读、写、属性、列表、创建、删除、移动、复制、从 URL 下载、上传、特殊目录 |
| 环境 | 录屏 | 开始、结束、取回录像 |
| 环境 | 探针与能力上报 | 服务是否可用、桌面会话是否就绪、支持哪些动作 |

由这些需求推出的设计约束：

1. 推理循环每步都要截图与动作，调用频率高、对延迟敏感、动作有严格顺序，因此接口必须是同步请求响应，不能经过队列。
2. 截图与输入依赖图形会话，服务必须运行在登录用户的交互式桌面内，而不是系统服务。
3. 截图、窗口枚举、无障碍树在某些应用上可能长时间阻塞，必须有超时并与主服务隔离。
4. 启动 GUI 程序与执行命令是两种语义，前者不能等待退出，后者必须收集输出，需要分开建模。
5. 录屏和长命令跨越多个请求，需要可按标识查询与收尾的模式。
6. 服务以桌面用户身份运行，其能做的事与用户本人相同，边界控制放在服务入口而不是动作内部。
7. 输入动作非幂等，协议必须定义重试语义。
8. 高 DPI 缩放下截图像素与系统坐标可能不一致，协议必须固定一种坐标系。

## 3. 架构

```text
   调用方 (AI Agent / 自动化程序)
            |
            |  HTTP, JSON, 同步请求响应
            v
   +----------------------------+
   |  vm-agent  (VM 内, 桌面会话) |
   |  HTTP server :7070          |
   |  中间件 -> 去重 -> 路由 -> 处理器 |
   |  审计 JSONL                 |
   +----------------------------+
            |
   screen / input / window / process / file / clipboard / record
```

设计决定：

- 调用方直连 `vm-agent`，中间没有 Gateway。原有的 Gateway、任务队列、租约、长轮询、制品索引从主路径移除。
- `vm-agent` 是 HTTP 服务端，每个请求同步执行并返回。不维护任务队列，不做调度。
- 长时间操作统一采用"立即返回标识，再按标识查询与收尾"的模式：录屏用 `session_id`，后台命令用 `job_id`，独立程序用 `pid`。这只是服务内的一张在途表，没有队列、租约和重投。
- 将来若需要管理多台 VM，由外部的薄转发层负责发现与路由，`vm-agent` 不感知它。

## 4. 运行模型

### 4.1 进程身份与启动

`vm-agent` 必须以登录的桌面用户身份运行在交互式会话中。

| 平台 | 启动方式 | 不可采用 |
| --- | --- | --- |
| Windows | 计划任务，触发器为该用户登录，以 `-supervise` 模式启动，配合自动登录账户 | Windows 服务（Session 0 无桌面）；计划任务自带的失败重启（最小间隔 1 分钟且有次数上限） |
| Linux | `systemd --user` 单元，`Restart=always`，`RestartSec=5`，`After=graphical-session.target`，环境含 `DISPLAY` 与 `XAUTHORITY` | 系统级 systemd 单元、`ProtectSystem=strict`、非登录用户 |

`-supervise` 模式：父进程不加载任何业务逻辑，只负责以相同参数（去掉 `-supervise`）启动子进程，子进程退出后等待 5 秒再拉起，连续 10 次在 60 秒内退出则等待 60 秒；父进程收到终止信号时转发给子进程并等待其优雅退出。父进程逻辑极简并 `recover` 所有 panic；作为最后兜底，计划任务额外配置每日一次的触发器，新实例启动时检查锁文件（位于 `-job-dir` 的上级目录，名为 `vm-agent.lock`，内容为父进程 pid），若对应进程仍存活则立即退出。Linux 下由 systemd 负责同样的事，不使用 `-supervise`。

Windows 二进制以 GUI 子系统构建（`go build -ldflags -H=windowsgui`），由计划任务在用户登录时启动时不会在自动化桌面上出现控制台窗口；控制台窗口会遮挡截图内容、抢占前台焦点，也可能被误关闭而终止服务。由此带来的约束：

- 进程没有控制台，诊断输出不可见：`-supervise` 父进程把子进程的 stdout、stderr 与自身日志写入锁文件旁的 `vm-agent.log`（超过 10 MiB 轮转，保留一个备份）。手动运行排障时输出不会显示在启动它的控制台中，需重定向到文件（如 `vm-agent.exe <参数> > out.log 2>&1`）。
- 服务启动的控制台子进程（`exec` 与后台 job、ffmpeg 及其启动探测、`a11y/tree` 的 PowerShell helper）以 `CREATE_NO_WINDOW` 创建，不弹出窗口，输出仍经管道返回；GUI 程序忽略该标志。`process/launch` 以 `DETACHED_PROCESS` 启动，不继承控制台。

Windows 进程启动后首先调用 `SetProcessDpiAwarenessContext(DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2)`，失败则回退 `SetProcessDPIAware`，并在 `probe` 中上报实际生效的感知级别。

### 4.2 桌面会话前置条件

以下条件任一不满足时，截图可能返回黑屏，输入可能无效。安装脚本负责设置，`probe` 负责检测并上报。portal RemoteDesktop 的批准必须由人在桌面上完成一次，脚本无法代为批准；这是 Wayland 的安全设计，因此 uinput 是首选通道，portal 只在已存有 restore token 时作为回退。

| 条件 | Windows 设置 | Linux 设置 | probe 字段 |
| --- | --- | --- | --- |
| 不锁屏 | 组策略禁用锁屏与屏保，`powercfg` 关闭显示器关闭与睡眠 | 禁用 `xss`、`gnome-screensaver`，`xset s off -dpms` | `session_locked` |
| 不休眠 | `powercfg /h off` | `systemctl mask sleep.target` | 无 |
| 无安全桌面 | UAC 设为不提示，或服务账户为管理员且 `ConsentPromptBehaviorAdmin=0` | 无 | `uac_prompt` |
| 控制台会话 | 禁止 RDP 登录，或 RDP 断开后用 `tscon <id> /dest:console` 切回 | 无 | `session_type` 为 `console` 或 `rdp` |
| 图形会话存在 | 用户已登录 | `DISPLAY` 可连接 | `display.ready` |
| Wayland 输入通道（阶段 3） | 无 | 首选 uinput：`sudo deploy/linux/preconditions.sh --system --wayland` 开机加载 `uinput` 模块，并以 udev `uaccess` 规则把 `/dev/uinput` 只授予当前活动本地会话的用户。无法授予时改用 portal RemoteDesktop：在该会话中以桌面用户身份、用与服务相同的 `-job-dir` 执行一次 `VM_AGENT_PORTAL_SETUP=1 vm-agent -job-dir <目录>`，由人在合成器弹出的对话框中批准（最多等待 5 分钟），restore token 以 0600 权限写入 `-job-dir` 上级目录的 `wayland-remote-desktop.token`，之后服务无需再次批准；每次使用后 portal 返回的新 token 会替换旧 token。合成器不返回 token（不支持持久化批准）时该命令失败退出 | `capabilities.actions` 含 `input`；两者都不满足时 `input/*` 返回 `unsupported`，消息说明两种配置方式 |

### 4.3 启动参数

```text
-listen             监听地址，默认 0.0.0.0:7070
-audit-log          审计 JSONL 路径
-audit-max-size     审计文件按大小轮转，默认 100MiB
-audit-max-files    审计文件保留个数，默认 10
-audit-verbose      审计记录完整 argv 与输入文本，仅排障用，默认关闭
-supervise          守护模式，见 4.1，仅 Windows 使用
-exec-allowlist-file 可选，命令 allowlist 文件，每行一个可执行文件名或绝对路径；为空表示不限制
-exec-denylist-file  可选，命令 denylist 文件，格式同上；allowlist 与 denylist 同时存在时先查 denylist
-dedup-window       request_id 去重窗口，默认 60s，0 关闭
-max-concurrency    全局并发请求上限，默认 32
-root               可选，文件操作允许的根目录列表，逗号分隔；为空表示不限制
-ffmpeg             可选，ffmpeg 可执行文件路径；为空则在 PATH 查找
-record-dir         录像存储目录，默认用户临时目录下 vm-agent/records
-record-quota       录像目录总量上限，默认 2GiB，超出按最旧删除
-record-retention   已完成录像保留时长，默认 24h，与配额任一触发即清理
-job-dir            后台命令输出目录，默认用户临时目录下 vm-agent/jobs
-job-retention      已完成 job 保留时长，默认 24h
-upload-s3-endpoint  可选，录像上传的 S3 兼容端点，host[:port]，也可写 http:// 或 https:// 前缀；为空表示不启用上传，见 6.8
-upload-s3-bucket    上传目标 bucket，启用上传时必填
-upload-s3-prefix    可选，对象键前缀，不得含空段、. 或 ..
-upload-s3-access-key-file  存放访问密钥的文件，启用上传时必填，每次上传重新读取
-upload-s3-secret-key-file  存放私钥的文件，启用上传时必填，每次上传重新读取
-upload-s3-insecure  使用明文 HTTP 连接端点，默认关闭，仅用于受信任的内网
```

启用上传时上述参数在启动时校验（不连接端点），不合法则拒绝启动。密钥只通过文件传入，不出现在命令行与审计中。

### 4.4 优雅关闭

收到终止信号后按以下顺序执行：

1. 停止接受新请求。
2. 等待在途请求完成，上限 10 秒。
3. 对所有进行中的录屏会话执行 stop 流程：向 ffmpeg stdin 写 `q`，等待其正常退出，上限 5 秒，确保文件完整落盘。
4. 不等待后台 job 完成，直接终止其进程树，状态标记为 `killed`，`reason` 为 `agent_shutdown`。
5. 关闭 Job Object 句柄，清理残留子进程。
6. 写入关闭审计记录后退出。

`process/launch` 启动的程序不受影响。

## 5. 协议

### 5.1 传输与版本

HTTP/1.1。路径前缀 `/v1`。响应头 `X-Agent-Version` 返回服务版本。

JSON 请求体上限 8 MiB，超出返回 `too_large`。大文件写入使用 `file/upload` 的 multipart，上限 1 GiB。

### 5.2 鉴权

当前阶段不实现。服务在 HTTP 处理链中预留一个中间件位置，当前唯一实现为直通；若将来部署拓扑需要，在此位置加入校验实现，不改动任何接口。凭据的生成、分发、轮换以及用户级权限均属平台职责，不在本项目范围。部署方应通过网络层（私有网段、安全组、仅平台可路由）保证 VM 端口不对非平台组件开放。

### 5.3 请求头

| 头 | 说明 |
| --- | --- |
| `X-Request-Id` | 调用方生成，用于审计关联与去重，可选。格式为 16 到 128 个 `[A-Za-z0-9._-]` 字符，不符返回 `invalid_request` |
| `X-Timeout-Ms` | 本次请求超时，缺省按动作默认值，见第 7 节。硬上限：一般同步请求 10 分钟；`file/download`、`file/upload`、`record/{id}` 下载与 `record/{id}/upload` 1 小时；超出按上限截断 |

HTTP 服务器参数：`ReadHeaderTimeout` 10 秒，`IdleTimeout` 120 秒，`MaxHeaderBytes` 64 KiB，请求体读取受第 5.1 节上限约束。

### 5.4 响应

JSON 响应：

```json
{
  "ok": true,
  "request_id": "...",
  "elapsed_ms": 42,
  "result": { }
}
```

流式响应（`encoding=stream`）：body 为裸二进制，`Content-Type` 为实际类型，元数据放响应头 `X-Result-Width`、`X-Result-Height`、`X-Result-Sha256`、`X-Result-Size`、`X-Cursor-X`、`X-Cursor-Y`、`X-Captured-At`。

错误响应：

```json
{
  "ok": false,
  "request_id": "...",
  "error": { "code": "timeout", "message": "...", "retryable": false, "details": { } }
}
```

### 5.5 错误码

| code | HTTP | retryable | 场景 |
| --- | --- | --- | --- |
| `invalid_request` | 400 | 否 | 参数缺失或非法；`X-Request-Id` 格式不符；命令策略拒绝（`details.reason=policy_denied`）；URL scheme 不允许 |
| `not_found` | 404 | 否 | 窗口、进程、文件、会话不存在 |
| `ambiguous` | 409 | 否 | 窗口匹配多个，`details.candidates` 列出 |
| `conflict` | 409 | 否 | 录屏已在进行；文件已存在且未允许覆盖；去重在途（`details.reason=in_flight`）；流式响应重复请求（`details.reason=dedup_stream`） |
| `too_large` | 413 | 否 | 请求体、文件、输出超出上限 |
| `unsupported` | 501 | 否 | 本平台或当前能力集不支持 |
| `no_display` | 503 | 是 | 图形会话不可用或已锁屏 |
| `busy` | 503 | 是 | 达到并发上限 |
| `timeout` | 504 | 视动作 | 动作超时；只读动作可重试，输入与写类不可 |
| `io_failed` | 500 | 否 | 文件系统或子进程错误；`details.reason` 可为 `disk_full`、`foreground_lock` 等 |
| `internal` | 500 | 否 | 服务内部错误 |

命令非零退出不是错误：`exec` 返回 `ok=true`，退出码放在 `result.exit_code`。

去重命中不是错误：见 5.6。

### 5.6 重试与去重

动作按幂等性分两类：

| 类别 | 动作 | 调用方策略 |
| --- | --- | --- |
| 可重试 | 所有 GET 接口（`probe`、`capabilities`、`metrics`、`screen/info|cursor`、`window/list|active`、`file/known_dirs`、`clipboard` 读、`process/list`、`exec/jobs` 列表与查询、`record/list` 与 `record/{id}` 下载）以及 POST 中的 `screen/capture`、`file/read|stat|list` | 超时或 `no_display`、`busy` 可直接重试 |
| 不可重试 | `input/*`、`exec`、`exec/jobs/{id}/cancel`、`exec/jobs/{id}` 删除、`process/launch`、`process/open`、`process/kill`、`window/activate|close|resize|close_all`、`file/write|mkdir|remove|move|copy|download|upload`、`clipboard` 写、`record/start|stop`、`record/{id}` 删除 | 超时后状态未知，调用方应先观测再决定 |

服务端对携带 `X-Request-Id` 的不可重试动作做去重，键为 `方法 + 路径 + request_id`，不同接口或不同路径的相同 id 互不影响。

去重分两个阶段：

1. 在途：首次请求仍在执行时，相同键的第二个请求返回 `conflict` 并附 `details.reason=in_flight`，不并发执行。
2. 已完成：在 `-dedup-window` 内缓存首次响应。重复请求不再执行，以 HTTP 200 原样回放首次响应（含首次的 `ok`、`result` 或 `error`），并附响应头 `X-Dedup-Hit: true`，调用方无需特殊处理。

缓存只保存 JSON 结果，流式响应的重复请求返回 `conflict` 并附 `details.reason=dedup_stream`。`input/batch` 整体使用一个 `request_id`，不逐项去重。

### 5.7 并发

只读动作可并发。`input/*` 在服务内串行化，输入请求的超时从进入串行队列时开始计时，排队等待计入超时；排队中超时的请求不会被执行。`-max-concurrency` 限制全局在途请求，超出返回 `busy`。

## 6. 接口清单

### 6.1 系统

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/v1/probe` | 返回 `version`、`api_version`（当前为 `v1`）、平台、启动时间、`uptime_seconds`、`last_input_at`（最近一次成功输入动作的时间，用于判断假死；启动后尚无输入时为 null）、`display.ready`、`session_locked`、`session_type`、`uac_prompt`、DPI 感知级别、主屏物理分辨率与缩放 |
| GET | `/v1/capabilities` | 支持的动作列表与限制，见第 9 节 |
| GET | `/v1/metrics` | Prometheus 文本：请求计数、错误计数、按动作耗时直方图、在途请求数、goroutine 数、去重命中数、去重在途冲突数、`busy` 拒绝数 |

### 6.2 屏幕

坐标系：以主显示器左上角为原点的物理像素。多显示器时虚拟桌面坐标可为负。

| 方法 | 路径 | 请求 | 响应 |
| --- | --- | --- | --- |
| POST | `/v1/screen/capture` | `display`（默认 0，`-1` 为整个虚拟桌面）、`region`{x,y,w,h}（虚拟桌面坐标，可选，超出显示器边界的部分裁剪，完全越界返回 `invalid_request`）、`cursor`(bool)、`format`(png/jpeg，默认 jpeg)、`quality`(jpeg 默认 85)、`encoding`(base64/stream) | 图像、宽高、光标坐标、截取时间戳、sha256 |
| GET | `/v1/screen/info` | | 显示器数组：index、bounds（物理像素）、scale、primary |
| GET | `/v1/screen/cursor` | | 光标坐标与是否可见 |

说明：GDI 与 X11 截图无法获取 DRM 保护内容与独占全屏的 DirectX 画面。PNG 使用最快压缩级别。截图在独立 goroutine 执行并受超时约束，默认 5 秒。

### 6.3 输入

| 方法 | 路径 | 请求 |
| --- | --- | --- |
| POST | `/v1/input/mouse/move` | `x`,`y`、`duration_ms` 可选，大于 0 时按线性插值分步移动，每步不少于 8 毫秒且不少于 2 像素 |
| POST | `/v1/input/mouse/click` | `x`,`y` 可选，缺省为当前位置，指定时先移动到该位置再点击；`button`(left/right/middle)；`count` 默认 1，上限 5 |
| POST | `/v1/input/mouse/down` / `up` | `button` |
| POST | `/v1/input/mouse/drag` | `from`{x,y}、`to`{x,y}、`button`、`duration_ms` 默认 300；在 `from` 处按下，按 `mouse/move` 的步长规则插值移动到 `to`，再抬起 |
| POST | `/v1/input/mouse/scroll` | `dx`,`dy`，单位为滚轮刻度，Windows 一刻度为 120 delta，正值向上或向右 |
| POST | `/v1/input/key/type` | `text`（Unicode）、`interval_ms` 每字符间隔，默认 5；`method`(unicode/scancode，默认 unicode)，`scancode` 仅支持 ASCII，用于不响应 Unicode 注入的应用 |
| POST | `/v1/input/key/press` | `key`；单独按 `win` 或 `super` 会触发系统菜单，`keys.md` 中注明 |
| POST | `/v1/input/key/down` / `up` | `key` |
| POST | `/v1/input/key/hotkey` | `keys` 数组，按数组顺序按下，逆序释放 |
| POST | `/v1/input/batch` | `actions` 有序数组，每项含 `type` 与对应参数及可选 `delay_after_ms`；`type` 取值：`mouse.move`、`mouse.click`、`mouse.down`、`mouse.up`、`mouse.drag`、`mouse.scroll`、`key.type`、`key.press`、`key.down`、`key.up`、`key.hotkey`、`wait`（仅 `ms` 参数，作为显式等待步骤，用于等待界面响应；`delay_after_ms` 则是动作完成后的固定间隔，两者可并存）；顺序执行，遇错即停，返回 `executed` 计数与失败项错误；连接中途断开则停止执行，已执行的动作不回滚，审计记录 `executed` |

键名表以 `docs/keys.md` 维护，阶段 1 交付。覆盖字母、数字、F1 到 F24、修饰键（`ctrl`、`alt`、`shift`、`win`/`super`）、导航键、编辑键、小键盘、媒体键。Windows 映射到虚拟键码与扫描码，Linux 映射到 X keysym。

### 6.4 窗口

| 方法 | 路径 | 请求 | 响应 |
| --- | --- | --- | --- |
| GET | `/v1/window/list` | `visible_only` 默认 true | 每个窗口：`id`、`title`、`class`、`pid`、`rect`（整窗，物理像素）、`client_rect`（客户区，物理像素）、`active`、`minimized`；最小化时 `rect` 与 `client_rect` 为 null |
| GET | `/v1/window/active` | | 前台窗口，字段同上 |
| POST | `/v1/window/activate` | `id`，或 `title`+`match`(exact/contains/regex) 加可选 `class`、`pid` 过滤（与 `title` 取交集）+`pick`(first/topmost/error，默认 error)；`topmost` 指 Z 序最前者 | 激活后的窗口信息 |
| POST | `/v1/window/close` | 同上 | 发送关闭请求，不强杀 |
| POST | `/v1/window/resize` | `id`、`rect` 或 `state`(maximize/minimize/restore)；对最大化或最小化窗口设置 `rect` 时先 restore 再应用 | |
| POST | `/v1/window/close_all` | `exclude_titles`、`exclude_pids` 可选、`wait_ms` 默认 3000 | `closed` 已消失的窗口列表、`remaining` 等待后仍存在的窗口列表，供调用方决定是否 `process/kill` |

匹配到多个窗口且 `pick=error` 时返回 `ambiguous`。

`close_all` 内置排除：桌面外壳窗口（Windows 类名 `Progman`、`WorkerW`、`Shell_TrayWnd`、`Windows.UI.Core.CoreWindow` 等；Linux 根窗口与 `_NET_WM_WINDOW_TYPE_DESKTOP`、`DOCK`）、`vm-agent` 自身进程、不可见与工具窗口。

Windows 前台激活顺序：`ShowWindow(SW_RESTORE)` 若最小化；`SetForegroundWindow`；失败则 `AttachThreadInput` 到当前前台线程后重试；再失败则注入一次 `Alt` 按下与释放后重试；仍失败返回 `io_failed` 并附 `details.reason=foreground_lock`。

### 6.5 进程与命令

| 方法 | 路径 | 请求 | 响应 |
| --- | --- | --- | --- |
| POST | `/v1/exec` | `argv` 数组；`cwd` 默认桌面用户主目录；`env` 合并到当前环境且同名覆盖，`env_clear`(bool) 为 true 时只使用请求提供的变量；`stdin`(base64，写入后立即关闭，不支持交互式)、`output_limit_bytes`；`detach` 默认 false；`job_timeout_ms` 仅后台模式有效 | 同步模式：`exit_code`、`stdout`、`stderr`、`truncated`、`duration_ms`，等待退出；后台模式：立即返回 `job_id` |
| GET | `/v1/exec/jobs` | | 在途与已完成 job 列表 |
| GET | `/v1/exec/jobs/{job_id}` | `tail_bytes` 可选，默认 64 KiB，0 表示全量（仍受该 job 的 `output_limit_bytes` 约束） | `status`(running/exited/killed)、`exit_code`、`stdout`、`stderr`、`started_at`、`finished_at`、`reason` |
| POST | `/v1/exec/jobs/{job_id}/cancel` | | 终止整棵进程树 |
| DELETE | `/v1/exec/jobs/{job_id}` | | 删除已完成 job 的记录与输出文件 |
| POST | `/v1/process/launch` | `argv`、`cwd`、`env`、`env_clear`，语义与 `exec` 相同 | `pid`；脱离 Job，`DETACHED_PROCESS` 或 `setsid`，不收集输出 |
| POST | `/v1/process/open` | `path` 或 `url`；`url` 仅允许 `http`、`https`、`file` scheme，其他返回 `invalid_request` | `ShellExecute` / `xdg-open` 等价实现 |
| GET | `/v1/process/list` | `name` 过滤可选，不区分大小写的包含匹配 | pid、ppid、name、exe、启动时间 |
| POST | `/v1/process/kill` | `pid`、`tree`、`grace_ms` 默认 3000、`force` 默认 false | 先礼后兵：Linux 发 SIGTERM，Windows 向主窗口发 `WM_CLOSE`（无窗口则跳过）；等待 `grace_ms` 后仍存活则 SIGKILL 或 `TerminateProcess`；`force=true` 跳过宽限直接强杀；`tree=true` 对整棵进程树执行 |

命令只接受 `argv` 数组，不提供 shell 字符串。需要管道、重定向、通配符等 shell 语法时，由调用方把解释器作为 `argv` 首项，例如 `["cmd","/c","dir *.txt | find \"x\""]`、`["powershell","-NoProfile","-Command","Get-Process | Select Name"]`、`["sh","-c","ls *.txt | wc -l"]`。审计规则见第 8 节。

同步模式默认超时 30 秒；超时或连接断开时终止整棵进程树。stdout/stderr 按 UTF-8 解码，非法字节替换；argv 首项为 `cmd` 或 `cmd.exe` 时额外尝试当前代码页解码。非零退出是正常结果，`ok=true`，`exit_code` 非零。

后台模式（`detach=true`）用于预计超过同步超时的长命令。命令进入 Job，输出写入 `-job-dir` 下该 job 的目录并受 `output_limit_bytes` 约束。job 元数据以磁盘为准：状态变化先写磁盘再更新内存；服务启动时扫描 `-job-dir` 重建列表，所有状态仍为 `running` 的记录一律改为 `killed` 并附 `reason=agent_restart`。已完成 job 默认保留 `-job-retention`，上限 1000 条，超出按最旧删除。后台 job 与 `process/launch` 的区别：前者收集输出、受服务生命周期管理、可按 id 取消；后者不收集输出、脱离服务独立存活。

### 6.6 文件

| 方法 | 路径 | 请求 | 响应 |
| --- | --- | --- | --- |
| POST | `/v1/file/read` | `path`、`offset`、`limit`、`encoding`(base64/stream) | base64 模式：内容、大小、sha256、mtime；stream 模式：裸 body，响应头 `X-Result-Size`、`X-Result-Sha256`、`X-File-Mtime` |
| POST | `/v1/file/write` | `path`、`content`(base64)、`overwrite`、`mkdir`、`sha256` 可选校验 | 大小、sha256；原子写；受 8 MiB JSON 上限约束 |
| POST | `/v1/file/upload` | multipart；每个文件 part 的 `name` 为目标绝对路径；可选名为 `options` 的 part 为 JSON，含 `overwrite`、`mkdir`，作用于本次全部文件 | 每个文件的大小与 sha256 |
| POST | `/v1/file/stat` | `path` | 存在性、类型、大小、mtime、hidden、readonly |
| POST | `/v1/file/list` | `path`、`recursive`、`max_entries` 默认 1000 | 条目数组，每项：`name`、`path`、`type`(file/dir/symlink)、`size`、`mtime`、`hidden`；超过 `max_entries` 时 `truncated=true` |
| POST | `/v1/file/mkdir` | `path` | |
| POST | `/v1/file/remove` | `path`、`recursive` | |
| POST | `/v1/file/move` / `copy` | `from`、`to`、`overwrite` | |
| POST | `/v1/file/download` | `url`（仅 `http`、`https`）、`path`、`max_bytes` 默认 1 GiB、`retries` 默认 3、`headers` 可选 | 大小、sha256；跟随最多 10 次重定向；所有重试共用本请求的总超时；网络权限等同于桌面用户，不做地址过滤 |
| GET | `/v1/file/known_dirs` | | home、desktop、documents、downloads、pictures、temp |

路径为绝对路径；`-root` 非空时限制在根目录列表内，否则不限制。`file/read` 单次上限 64 MiB，更大文件分段或流式。

### 6.7 剪贴板

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/v1/clipboard` | 返回 `formats` 列表及其中可用的 `text`、`image`(base64 PNG)；两者同时存在时都返回；为空时 `formats` 为空数组 |
| POST | `/v1/clipboard` | 写入 `text` 或 `image` |

### 6.8 录屏

| 方法 | 路径 | 请求 | 响应 |
| --- | --- | --- | --- |
| POST | `/v1/record/start` | `fps` 默认 10、`display`、`region` 可选、`preset` 默认 ultrafast、`crf` 默认 28 | `session_id`、`started_at` |
| POST | `/v1/record/stop` | `session_id` | `session_id`、`size`、`duration_ms`、`sha256` |
| GET | `/v1/record/{session_id}` | | 流式返回 mp4 |
| DELETE | `/v1/record/{session_id}` | | 删除录像 |
| GET | `/v1/record/list` | | 进行中与已完成的会话 |
| POST | `/v1/record/{session_id}/upload` | `key` 可选，对象键，相对 `-upload-s3-prefix`，默认 `<session_id>.mp4` | `session_id`、`bucket`、`key`（含前缀的完整对象键）、`size`、`etag`、`sha256`（本地录像文件的 sha256） |

同一时刻只允许一个进行中的录制，重复 start 返回 `conflict`。ffmpeg 进入 Job；stop 时向其 stdin 写 `q` 并等待正常退出，上限 5 秒后强杀。ffmpeg 输出参数固定包含 `-movflags +frag_keyframe+empty_moov`，保证进程被强杀时文件仍可播放。服务退出时的处理顺序见 4.4。录像存于 `-record-dir`，总量超过 `-record-quota` 或已完成会话超过 `-record-retention` 时按最旧删除，两个条件任一触发即清理；调用方应在 stop 后尽快下载。ffmpeg 缺失时 `capabilities` 不含 `record`。

上传（阶段 3，可选）：`record/{session_id}/upload` 把已完成的录像流式上传到 4.3 中 `-upload-s3-*` 配置的 S3 兼容对象存储，大文件自动分片上传，本地文件保留，仍受配额与保留期清理。上传不依赖桌面会话与 ffmpeg；配置了上传时 `capabilities.actions` 含 `record_upload`，否则不含，接口返回 `unsupported`。默认超时 10 分钟，属于传输类接口，`X-Timeout-Ms` 硬上限 1 小时（见 5.3）；不可重试，带 `X-Request-Id` 时按 5.6 去重。错误：

| 情形 | code |
| --- | --- |
| 未配置对象存储 | `unsupported` |
| `session_id` 不存在 | `not_found` |
| 会话仍在录制 | `conflict` |
| `key` 为空、以 `/` 开头、含反斜杠或 NUL，或含空段、`.`、`..` 路径段 | `invalid_request` |
| 读取密钥文件失败、端点不可达、对象存储拒绝 | `io_failed` |
| 超时或调用方断开 | `timeout` |

### 6.9 无障碍树（阶段 3，可选）

| 方法 | 路径 | 请求 | 响应 |
| --- | --- | --- | --- |
| POST | `/v1/a11y/tree` | `window_id` 可选，省略时取前台窗口；`max_depth` 默认 10，取值 1 到 100；`max_nodes` 默认 5000，取值 1 到 50000 | 节点树：role、name、value、rect、state、children |

在限时子进程中执行，默认 15 秒，超时返回 `timeout`。`max_depth` 或 `max_nodes` 超出范围返回 `invalid_request`；两个上限在 `capabilities.limits` 中以 `a11y_max_depth`、`a11y_max_nodes` 上报（见第 9 节）。上限用于约束 helper 子进程的耗时与响应体大小。

## 7. 可靠性边界

以下限制与安全无关，用于保证服务不被单个请求拖垮，始终生效。

默认超时按动作：

| 动作 | 默认超时 |
| --- | --- |
| `screen/capture`、`screen/info`、`window/*`、`clipboard` | 5 秒 |
| `input/*` 单动作 | 5 秒加 `duration_ms` |
| `input/batch` | 各动作默认超时之和加 5 秒，默认上限 60 秒；超过需通过 `X-Timeout-Ms` 显式指定，硬上限 10 分钟 |
| `exec` 同步模式 | 30 秒 |
| `exec` 后台模式 | 请求本身 5 秒；job 本身默认无上限，可通过请求体 `job_timeout_ms` 设置 |
| `file/*` 本地操作 | 30 秒；`file/read` 与 `file/remove` 为 60 秒 |
| `file/download`、`file/upload` | 10 分钟 |
| `process/launch|open|list|kill` | 10 秒，`kill` 另加 `grace_ms` |
| `exec/jobs` 列表与查询、取消、删除 | 5 秒 |
| `record/{id}` 下载 | 默认 10 分钟，硬上限 1 小时，见 5.3 |
| `probe`、`capabilities`、`metrics`、`file/known_dirs` | 5 秒 |
| `a11y/tree` | 15 秒 |
| `record/start|stop` | 10 秒 |

其他边界：

- 命令输出默认 1 MiB，按请求最高 64 MiB，超出截断并标记 `truncated`。
- 文件读取单次 64 MiB；JSON 体 8 MiB；multipart 1 GiB。
- 批量输入最多 200 个动作。
- 全局并发默认 32，超出返回 `busy`。
- 命令超时或连接断开时终止整棵进程树。
- 文件写入先写临时文件再原子替换。
- 截图、窗口枚举、无障碍树在独立 goroutine 或子进程执行，超时后放弃结果，不阻塞后续请求；连续 3 次截图超时时 `probe` 上报 `display.degraded`。超时后被放弃的 goroutine 必须有自然退出路径（子进程被杀、系统调用返回），`metrics` 暴露 goroutine 数供监控。
- 去重缓存按窗口过期，上限 10000 条。
- 磁盘空间下限 500 MiB：启动时与每次 `record/start`、`exec detach`、`file/write`、`file/download`、`file/upload` 前检查 `-record-dir`、`-job-dir` 或目标路径所在卷的可用空间，低于下限返回 `io_failed` 并附 `details.reason=disk_full`。

## 8. 安全模型

VM 是隔离单元。`vm-agent` 以桌面用户身份运行，权限等同该用户，服务内不设沙箱。控制点在服务入口：

- 鉴权与 TLS 当前不实现，见 5.2；VM 端口的访问控制由部署方在网络层保证。
- 监听地址可限制为特定网卡。
- 每个请求一条审计记录：时间、request_id、来源地址、动作、参数摘要、结果码、耗时、是否去重命中。参数摘要规则：`exec` 与 `launch` 记录 argv 前两项、参数个数与完整 argv 的 sha256，不记录完整参数；`env` 只记录键名；`key/type` 记录字符数不记录内容；文件操作记录路径不记录内容；`file/download` 的 `headers` 只记录键名；截图记录尺寸与 sha256。可通过 `-audit-verbose` 开启记录完整 argv 与输入文本，仅用于排障，默认关闭。审计文件按 `-audit-max-size` 轮转，保留 `-audit-max-files` 个。
- `-root` 可选限制文件范围；`-exec-allowlist-file` 与 `-exec-denylist-file` 可选限制命令，默认不启用，命中时返回 `invalid_request` 并附 `details.reason=policy_denied`。

## 9. 能力上报

```json
{
  "platform": "windows",
  "version": "1.0.0",
  "display": { "ready": true, "degraded": false, "count": 1, "dpi_awareness": "per_monitor_v2",
               "primary": { "w": 2560, "h": 1440, "scale": 1.25 } },
  "session": { "locked": false, "type": "console", "uac_prompt": false },
  "actions": ["screen", "input", "window", "exec", "process", "file", "clipboard", "record"],
  "limits": { "json_body_max_bytes": 8388608, "multipart_max_bytes": 1073741824,
              "exec_output_max_bytes": 67108864, "file_read_max_bytes": 67108864,
              "download_max_bytes": 1073741824, "batch_max_actions": 200,
              "max_concurrency": 32, "sync_timeout_max_ms": 600000, "transfer_timeout_max_ms": 3600000,
              "a11y_max_depth": 100, "a11y_max_nodes": 50000 }
}
```

`sync_timeout_max_ms` 适用于除传输类以外的全部接口；`transfer_timeout_max_ms` 适用于 `file/download`、`file/upload`、`record/{id}` 下载与 `record/{id}/upload`。`a11y_max_depth`、`a11y_max_nodes` 是 `a11y/tree` 请求中 `max_depth`、`max_nodes` 的上限，与 `a11y` 是否在 `actions` 中无关，始终上报。

无桌面会话或已锁屏时 `display.ready` 为 false，`actions` 不含 `screen`、`input`、`window`、`clipboard`、`record`，对应接口返回 `no_display`。

`version` 是服务版本，遵循语义化版本；API 路径前缀 `/v1` 是协议版本，两者独立。服务版本的 major 变化不代表协议变化。

## 10. 实现方案

| 能力 | Windows | Linux X11 | 说明 |
| --- | --- | --- | --- |
| DPI | `SetProcessDpiAwarenessContext`，`GetDpiForMonitor` 取 scale | `Xft.dpi` 资源或 RandR 物理尺寸估算 | 启动时设置 |
| 会话检测 | `session_locked`：`OpenInputDesktop` 失败或 `GetUserObjectInformation` 返回的桌面名不是 `Default`；`uac_prompt`：读注册表 `EnableLUA` 与 `ConsentPromptBehaviorAdmin`；`session_type`：`WTSQuerySessionInformation` 的 `WTSClientProtocolType`，0 为 console，2 为 rdp | `session_locked`：通过 D-Bus 读 `org.freedesktop.login1` 当前会话的 `LockedHint`（godbus，纯 Go）；`session_type` 固定 console | `probe` 每次请求实时检测 |
| 截图 | `github.com/kbinani/screenshot`（GDI BitBlt，无 cgo） | 同库（xgb `GetImage`） | `image.RGBA`，JPEG 默认，PNG `BestSpeed` |
| 光标 | `GetCursorInfo` 取位置与句柄，`DrawIconEx` 到位图；失败回退内置箭头图标 | `XFixesGetCursorImage` via xgb | |
| 鼠标键盘 | 自行封装 `user32.SendInput`，鼠标用 `MOUSEEVENTF_ABSOLUTE | MOUSEEVENTF_VIRTUALDESK`，坐标按虚拟桌面矩形（`SM_XVIRTUALSCREEN` 等）归一化到 0 到 65535，保证副显示器坐标正确；键盘用扫描码加 `KEYEVENTF_UNICODE` 处理文本 | xgb XTest 扩展 `FakeInput`，文本通过 keysym 映射，未映射字符临时绑定空闲 keycode | 两端均纯 Go；若阶段 1 原型发现 SendInput 封装成本过高，备选 `github.com/aiwaki/makc` |
| 窗口 | `EnumWindows`、`GetWindowTextW`、`GetClassNameW`、`GetWindowThreadProcessId`、`DwmGetWindowAttribute` 取真实矩形、`SetForegroundWindow`、`AttachThreadInput`、`PostMessage(WM_CLOSE)`、`ShowWindow` | xgb 读 `_NET_CLIENT_LIST`、`_NET_WM_NAME`、`_NET_WM_PID`、`WM_CLASS`，`_NET_ACTIVE_WINDOW` 消息激活，`_NET_CLOSE_WINDOW` 关闭，`_NET_WM_STATE` 最大化最小化 | |
| 进程 | `exec` 与 ffmpeg 进 Job Object（`JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`），`launch` 用 `DETACHED_PROCESS` 加 `CREATE_NEW_PROCESS_GROUP` 且不加入 Job | `exec` 与 ffmpeg 用 `Setpgid` 进程组，`launch` 用 `Setsid` | 复用现有 `process_*.go` 的 Job 与进程组代码 |
| 文件 | 标准库；`SHGetKnownFolderPath` 取特殊目录；`GetFileAttributes` 取 hidden/readonly | 标准库；`xdg-user-dirs` 配置文件解析 | 复用原子写 |
| 剪贴板 | `OpenClipboard`、`GetClipboardData`，读取优先注册格式 `PNG` 再回退 CF_DIB；写入图像时同时放置 `PNG` 注册格式与 CF_DIB，保证 alpha 与兼容性 | xgb 实现 X selection 的 owner 与 request，支持 `UTF8_STRING` 与 `image/png`。限制：X selection 内容由 owner 进程持续提供，`vm-agent` 重启后其写入的剪贴板内容即丢失；需要持久化时镜像应预装剪贴板管理器 | 纯 Go |
| 录屏 | `ffmpeg -f gdigrab -framerate N -i desktop -c:v libx264 -preset ultrafast -crf 28 -pix_fmt yuv420p` | `ffmpeg -f x11grab -framerate N -i $DISPLAY ...` | 子进程进 Job |
| 无障碍 | PowerShell `System.Windows.Automation` 限时子进程输出 JSON | AT-SPI 通过 D-Bus，限时子进程 | 阶段 3 |
| 去重 | 内存 LRU 加在途集合，key 为 `方法 + 路径 + request_id` | 同 | |
| 审计 | 复用 `internal/audit` | 同 | `internal/credentials`、`internal/tlsreload` 保留但当前不接入 |

跨平台通过 build tag 分文件；公共接口定义在 `internal/actions/<domain>/<domain>.go`，平台实现在 `_windows.go` 与 `_linux.go`。

## 11. 与现有代码的关系

| 现有模块 | 处理 |
| --- | --- |
| `cmd/guest-agent` | 改造为 `cmd/vm-agent`，启动 HTTP server |
| `cmd/gateway`、`internal/gateway`、`internal/cms`、`internal/artifacts` | 移至 `legacy/`，所有文件加 `//go:build legacy` 标签，默认构建、测试、vet 均不包含；CI 不传该标签；阶段 2 结束后删除。`artifacts` 的 S3 部分若将来需要录像外传，作为可选上传器重新引入 |
| `internal/agent` Runner、Client | 删除 |
| `internal/agent` executor 命令与文件内核、`process_*.go` | 迁入 `internal/actions/exec`、`internal/actions/file`、`internal/actions/process`，去掉 workspace 限制与策略检查 |
| `internal/agent/desktop_*.go` | 由 `internal/actions/screen` 替代 |
| `internal/protocol` | 重写为新的请求响应结构；任务、租约、幂等相关类型删除 |
| `internal/policy` | 保留，由 `-exec-allowlist-file` 与 `-exec-denylist-file` 驱动，默认关闭 |
| `internal/credentials`、`internal/tlsreload`、`internal/audit` | 直接复用；`audit` 增加按大小轮转 |
| `deploy/` | 重写：Windows 计划任务安装脚本（PowerShell）、Linux user service 安装脚本、会话前置条件配置脚本、升级回滚 |
| `docs/` | 本文档通过后更新 `architecture.md`、`protocol.md`、`security.md`、`roadmap.md`，新增 `keys.md`、`errors.md`、`openapi.yaml` |

## 12. 测试策略

| 层 | 方式 | 覆盖 |
| --- | --- | --- |
| 单元 | `go test`，平台实现用接口 mock | 协议解析、错误映射、去重（已完成回放、在途冲突、键隔离）、超时、批量顺序、路径处理、键名表 |
| Linux 集成 | CI 中 Xvfb 加轻量窗口管理器（openbox）运行真实 X11 | 截图、输入、窗口、剪贴板、录屏全链路；客户端在 batch 执行中途断开，验证停止执行且 `executed` 准确 |
| Windows 集成 | CI 仅编译与单元测试；E2E 在真实交互会话的 VM 上由 `deploy/windows-e2e.ps1` 执行 | 同上加 DPI 125% 与 150% 两档 |
| 回归脚本 | `deploy/e2e.sh` 与 `deploy/windows-e2e.ps1` | 启动文本编辑器、激活窗口、输入、保存、截图、读文件、关闭 |
| 稳定性 | 24 小时循环截图加输入 | 与 13.1 验收 3 相同的基线与阈值：以启动后 1 小时为基线，24 小时后 RSS、goroutine 数、句柄数增幅均不超过 10% |
| 性能 | 1080p 与 1440p 截图 1000 次 | p50 与 p99 延迟，作为验收依据 |

## 13. 分阶段计划

### 阶段 1：跑通 Agent 主循环

范围：HTTP 骨架（含直通中间件位置）、审计、去重、`probe`、`capabilities`、`screen/capture|info|cursor`、`input/*` 含 batch、`window/list|active|activate|close`、`exec` 同步模式（`detach` 字段存在但返回 `unsupported`）、`process/launch|open`、`file/read|write|stat|list|mkdir|known_dirs`、`docs/keys.md`、`openapi.yaml`、Linux Xvfb CI、Windows E2E 脚本。

平台：Windows 10/11 与 Ubuntu 24.04 X11。

验收：

1. 两个平台上回归脚本通过，Windows 在 100%、125%、150% 缩放下点击位置误差不超过 2 像素。测量方法：E2E 脚本启动一个全屏自绘测试窗口，记录每次 `WM_LBUTTONDOWN` 的屏幕坐标，与请求坐标逐点比对。
2. 1080p JPEG 截图 p50 小于 60 ms，p99 小于 150 ms，以响应中的 `elapsed_ms` 为准，覆盖抓取、光标叠加与编码，不含网络传输；PNG 另行记录不作门槛。
3. 服务连续运行 24 小时无崩溃；以启动后 1 小时的 RSS、goroutine 数、句柄数为基线，24 小时后增幅均不超过 10%。
4. `request_id` 重复提交 `click` 不产生第二次点击。
5. 锁屏时 `probe` 正确上报且输入接口返回 `no_display`。
6. `vm-agent` 重启后由 `launch` 启动的程序仍在运行，`exec` 启动的命令已被清理。

### 阶段 2：补齐操作面与交付

范围：`file/*` 其余接口、`upload`、`clipboard`（Linux 的 X selection 纯 Go 实现若工时超预期，允许延后到阶段 3，期间 Linux 的 `capabilities` 不上报 `clipboard`）、`process/list|kill`、`exec` 后台模式与 `exec/jobs/*`、`window/resize|close_all`、`record/*`、`metrics`、Windows 计划任务与 Linux user service 安装脚本、会话前置条件配置脚本、升级回滚、`legacy/` 清理。

验收：

1. 录屏 10 分钟 stop 后得到可播放 mp4，CPU 占用单核不超过 60%。
2. 录像目录超出配额时自动清理最旧会话；超过 `-record-retention` 的已完成录像被清理。
3. `close_all` 不关闭桌面外壳与自身。
4. 升级后回滚不丢配置与录像。
5. 50 个动作的 batch 顺序正确，中途失败返回准确的 `executed`。

### 阶段 3：可选能力

`a11y/tree`、Wayland（截图走 portal Screenshot，输入走 portal RemoteDesktop 或 uinput）、多显示器矩阵测试、可选对象存储上传器、阶段 2 若延后的 Linux 剪贴板；鉴权与 TLS 若部署拓扑需要，在此阶段按 5.2 预留位置加入。

## 14. 交付物

| 交付物 | 阶段 |
| --- | --- |
| `vm-agent` 二进制：windows/amd64、linux/amd64、linux/arm64 | 1 |
| `docs/openapi.yaml` 与由此生成的 Go 客户端 `pkg/client` | 1 |
| `docs/keys.md` 键名表 | 1 |
| `docs/errors.md`：集中维护 5.5 错误码表与各接口 `details.reason` 的全部取值 | 1 |
| Linux Xvfb CI 流水线 | 1 |
| Windows E2E 脚本与 DPI 矩阵 | 1 |
| 安装、前置条件配置、升级回滚脚本 | 2 |
| 更新后的 `architecture.md`、`protocol.md`、`security.md`、`roadmap.md` | 2 |

## 15. 未决问题

无。以下事项已收敛：

| 事项 | 结论 |
| --- | --- |
| `SendInput` 自行封装还是引入 `makc` | 阶段 1 第一周验证自行封装，失败再切换 |
| 审计是否记录完整参数 | 由 `-audit-verbose` 开关解决 |
| 鉴权与 TLS | 当前不实现，预留中间件位置，由部署拓扑决定是否需要，见 D9 |
| 是否预留向外部注册自身地址的接口 | 不预留，平台已知每台 VM 的 IP |
| Linux 剪贴板工时风险 | 允许延后到阶段 3，期间 `capabilities` 不上报 |
