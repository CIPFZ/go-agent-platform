# 安全模型

依据 spec 第 8 节与决策 D9。

## 边界

- VM 是隔离单元。`vm-agent` 以登录的桌面用户身份运行，权限等同该用户，服务内不设沙箱：能截图、输入、读写该用户可访问的文件、运行任意程序。
- **默认不鉴权，不实现 TLS。** 不配置口令时，部署方必须在网络层保证端口只对平台可达：私有网段、安全组、仅平台可路由；必要时用 `-listen` 绑定到面向平台的网卡。
- 凭据的分发、轮换与用户级权限属于平台职责，不在本项目范围。`internal/tlsreload` 保留，当前不接入。

## 访问口令（可选）

以 `-auth-token-file <文件>` 启动时，除 `GET /remote/`（不含数据的静态页面）外的每个请求都要带 `Authorization: Bearer <口令>`。

- 文件每个请求重新读取，替换文件即更换口令；文件读不出来时拒绝所有请求，不退回到免鉴权。
- 比较在哈希之后以恒定时间进行。同一来源地址连续输错 10 次后一分钟内一律拒绝，包括带着正确口令的请求。
- 鉴权失败与成功一样写一条审计记录（来源地址、路径、结果码 `unauthorized`），口令不进审计与日志。
- 口令是唯一的身份：没有用户、角色或只读权限之分，拿到口令就拥有全部能力。
- 没有 TLS，口令在网络上是明文。它挡的是能连到端口的人和程序，挡不了能窃听这段网络的人。
- 网页远程桌面的视频、声音与键鼠走 WebRTC，不单独鉴权：建立连接必须先通过带口令的 `POST /v1/remote/sessions`，其后由 WebRTC 加密。网页把口令存在浏览器的本地存储里。
- 锁屏助手的命名管道不受口令约束，它只对 SYSTEM 与本机登录用户开放。

## 来自浏览器的请求

不鉴权的服务还有一个不依赖网络位置的暴露面：用户在能访问该端口的任何一台机器上打开的网页，都可以让浏览器向 `vm-agent` 发请求（表单式的 POST 不需要预检），或者把自己的域名指向 `vm-agent` 的地址来冒充同源。`vm-agent` 因此拒绝带有外来 `Origin` 的请求和使用陌生主机名的请求（`internal/server/guard.go`），始终生效，与是否鉴权无关。不带 `Origin` 的 GET（例如网页里的图片地址）仍会被执行，但 GET 接口都是只读的，网页也读不到响应。

## 入口控制

| 控制 | 默认 | 说明 |
| --- | --- | --- |
| `-listen` | `0.0.0.0:7070` | 可限制到特定网卡 |
| `-auth-token-file` | 不启用 | 访问口令，见上 |
| `-root` | 不限制 | 逗号分隔的根目录；文件路径解析符号链接后必须位于其一之内，否则 `invalid_request/outside_root` |
| `-exec-allowlist-file` / `-exec-denylist-file` | 不启用 | 每行一个可执行文件名或绝对路径；先查 denylist；作用于 `exec` 与 `process/launch`；命中返回 `invalid_request/policy_denied`。名称与路径都会和 argv[0] 及其解析出的路径（含符号链接目标）比较 |
| URL scheme | — | `process/open` 只允许 `http`、`https`、`file`；`file/download` 只允许 `http`、`https`（不做地址过滤，网络权限等同桌面用户） |
| `-max-concurrency` | 32 | 防止单个调用方拖垮服务 |

命令只接受 `argv` 数组，没有 shell 字符串字段（D7）；需要 shell 语法时调用方显式把解释器放在 argv 首项，审计中可见。

## 审计

每个请求一条 JSONL 记录：时间、request_id、来源地址、动作、方法与路径、参数摘要、结果码、耗时、是否去重命中；另有 `startup`、`shutdown`、`orphans_reaped`、`disk_low` 生命周期记录。参数摘要规则：

| 动作 | 记录 | 不记录 |
| --- | --- | --- |
| `exec`、`process/launch` | argv 前两项、参数个数、完整 argv（JSON 编码）的 sha256、env 键名 | 其余参数、env 值、stdin |
| `input/key/type` | 字符数 | 文本 |
| 文件操作 | 路径、大小 | 内容 |
| `file/download` | URL、目标路径、header 键名 | header 值 |
| `screen/capture` | 尺寸与 sha256 | 图像 |
| `clipboard` 写入 | 类型与长度 | 内容 |

`-audit-verbose` 额外记录完整 argv 与输入文本，仅用于排障，默认关闭。审计文件按 `-audit-max-size`（默认 100 MiB）轮转，保留 `-audit-max-files`（默认 10）个，权限 0600。默认路径为用户临时目录下 `vm-agent/audit/audit.jsonl`，安装脚本改为持久目录。

## 进程与数据隔离

- `exec`、后台 job、ffmpeg 运行在可整体终止的进程树中（Windows Job Object `KILL_ON_JOB_CLOSE`，Linux 进程组），超时、断开、取消与服务退出时整树终止，服务崩溃后下次启动回收。
- `process/launch` 启动的程序有意脱离服务，服务重启后继续运行。
- 剪贴板：Linux 上 `vm-agent` 写入的内容由本进程作为 X selection owner 提供，`vm-agent` 退出后是否保留取决于桌面：没有剪贴板管理器时随之消失；GNOME Shell（mutter）自带剪贴板管理，会在 owner 退出时接管并保留文本与图像（Ubuntu 22.04 GNOME 42 实测），之后读到的仍是这些内容。
- 录像、job 输出与审计写在桌面用户的目录中，目录权限 0700。

## 可选能力的安全要点

- Wayland 输入：`/dev/uinput` 通过 udev `uaccess` 标签只授予当前活动本地会话的用户，而不是 `input` 组（该组还能读取所有输入设备）。RemoteDesktop 回退的 restore token 以 0600 权限保存在锁文件旁。
- 对象存储上传：访问密钥与私钥只从 `-upload-s3-access-key-file`、`-upload-s3-secret-key-file` 读取（每次上传重读，不进审计）；对象键不能逃出 `-upload-s3-prefix`。`-upload-s3-insecure` 使用明文 HTTP，仅用于受信任的内网。
- 无障碍树：helper 子进程以同一桌面用户身份运行，超时即整树终止。

- 网页远程桌面（`-remote`，默认关闭）：`/remote/` 页面与 `/v1/remote/sessions` 同样没有鉴权，任何能访问端口的人都能看到并操作桌面，也能删除别人的会话；另占用一个 UDP 端口（默认 7071）。审计记录会话的开始与结束、使用者名称与输入事件计数，不记录按键内容。

- 锁屏支持服务（`deploy/windows/secure-desktop.ps1`，可选）：增加一个开机自启的 SYSTEM 服务和一个运行在用户会话里的 SYSTEM 助手。装上之后，能访问 `vm-agent` 端口的人可以看到锁屏界面并向其输入，锁屏只剩密码这一道防线；在没有鉴权的情况下只应装在可信网络里的机器上。助手通过命名管道只接受 SYSTEM 与登录用户的连接，只做输入注入与屏幕采集。助手只在会话处于锁定状态且没有提权弹窗时工作，UAC 弹窗不能通过它操作。

## 部署建议

- 只在专用的自动化 VM 上运行；不要在含个人数据的桌面上启用。
- Windows 前置条件脚本可写入自动登录密码（Winlogon 以明文保存在注册表），仅用于专用 VM。
- `-RunLevel Highest`（Windows）使服务与其启动的程序以管理员身份运行，只在需要向提权窗口输入时启用。
