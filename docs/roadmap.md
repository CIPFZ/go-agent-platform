# 路线图

按 spec 第 13 节分阶段推进；验收证据与未验证项记录在各阶段汇报中。

## 阶段 1：跑通 Agent 主循环（已完成）

HTTP 骨架与直通鉴权位、审计、去重、`probe`、`capabilities`、`screen/*`、`input/*` 含 batch、`window/list|active|activate|close`、`exec` 同步模式、`process/launch|open`、`file/read|write|stat|list|mkdir|known_dirs`、`keys.md`、`errors.md`、`openapi.yaml` 与 `pkg/client`、Linux Xvfb CI、Windows E2E 脚本、三平台二进制。

## 阶段 2：补齐操作面与交付（已完成）

`file/remove|move|copy|download|upload`、`clipboard`（含 Linux 纯 Go X selection）、`process/list|kill`、`exec` 后台模式与 `exec/jobs/*`、`window/resize|close_all`、`record/*`、`metrics`、Linux user service 与 Windows 计划任务安装脚本、会话前置条件脚本、升级回滚、`legacy/` 删除、本组文档。

## 阶段 3：可选能力（已完成）

- `a11y/tree`：Windows 通过内嵌 PowerShell 脚本（`System.Windows.Automation`）限时子进程，Linux 通过 vm-agent 自身作为 AT-SPI helper 的限时子进程；`window_id` 省略时取前台窗口。
- Wayland：截图走 portal Screenshot，输入走 uinput 虚拟设备；uinput 不可写且存有 RemoteDesktop restore token 时改用 portal RemoteDesktop。窗口、剪贴板、录屏与无障碍在 Wayland 下返回 `unsupported`。
- 多显示器矩阵测试：Xorg dummy 驱动上的六种布局（单屏、主屏在左、主屏在右且错位、三屏、副屏在上、混合缩放）。
- 可选对象存储上传器：`POST /v1/record/{session_id}/upload`，`-upload-s3-*` 参数。
- 鉴权与 TLS：按 D9 不实现，中间件位置保留。

## 需要真实环境的验证

以下验收在开发环境中无法执行，需在目标 VM 上运行脚本确认：

- Windows 10/11：`deploy/windows-e2e.ps1` 在 100%、125%、150% 缩放下的点击误差、截图延迟、锁屏、重启与 Notepad 回归；安装、升级、回滚脚本。
- 24 小时稳定性：`test/soak` 在最终构建上的完整运行（Linux 与 Windows 各一次）。
- 真实桌面环境（GNOME 等）的锁屏检测与剪贴板管理器交互。
- Windows 上的 `a11y/tree`（UI Automation 脚本只做了语法与转义检查）。
- GNOME/KDE Wayland 会话：Screenshot portal 的权限行为，以及 RemoteDesktop 回退路径（只对伪造的 portal 测试过调用序列）。
- Windows 多显示器与混合 DPI。
