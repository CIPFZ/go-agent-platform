# 安全模型

依据 spec 第 8 节与决策 D9。

## 边界

- VM 是隔离单元。`vm-agent` 以登录的桌面用户身份运行，权限等同该用户，服务内不设沙箱：能截图、输入、读写该用户可访问的文件、运行任意程序。
- **当前不实现鉴权与 TLS。** HTTP 处理链中保留了鉴权中间件位置（`server.Options.Authenticate`），唯一实现是直通。部署方必须在网络层保证端口只对平台可达：私有网段、安全组、仅平台可路由；必要时用 `-listen` 绑定到面向平台的网卡。
- 凭据生成、分发、轮换与用户级权限属于平台职责，不在本项目范围。`internal/credentials` 与 `internal/tlsreload` 保留，当前不接入。

## 入口控制

| 控制 | 默认 | 说明 |
| --- | --- | --- |
| `-listen` | `0.0.0.0:7070` | 可限制到特定网卡 |
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
- 剪贴板：Linux 上由 `vm-agent` 写入的内容只存在于本进程，退出即消失；需要持久化时由镜像中的剪贴板管理器负责。
- 录像、job 输出与审计写在桌面用户的目录中，目录权限 0700。

## 可选能力的安全要点

- Wayland 输入：`/dev/uinput` 通过 udev `uaccess` 标签只授予当前活动本地会话的用户，而不是 `input` 组（该组还能读取所有输入设备）。RemoteDesktop 回退的 restore token 以 0600 权限保存在锁文件旁。
- 对象存储上传：访问密钥与私钥只从 `-upload-s3-access-key-file`、`-upload-s3-secret-key-file` 读取（每次上传重读，不进审计）；对象键不能逃出 `-upload-s3-prefix`。`-upload-s3-insecure` 使用明文 HTTP，仅用于受信任的内网。
- 无障碍树：helper 子进程以同一桌面用户身份运行，超时即整树终止。

## 部署建议

- 只在专用的自动化 VM 上运行；不要在含个人数据的桌面上启用。
- Windows 前置条件脚本可写入自动登录密码（Winlogon 以明文保存在注册表），仅用于专用 VM。
- `-RunLevel Highest`（Windows）使服务与其启动的程序以管理员身份运行，只在需要向提权窗口输入时启用。
