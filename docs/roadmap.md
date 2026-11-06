# 路线图

按 spec 第 13 节分阶段推进；验收证据与未验证项记录在各阶段汇报中。

## 阶段 1：跑通 Agent 主循环（已完成）

HTTP 骨架与直通鉴权位、审计、去重、`probe`、`capabilities`、`screen/*`、`input/*` 含 batch、`window/list|active|activate|close`、`exec` 同步模式、`process/launch|open`、`file/read|write|stat|list|mkdir|known_dirs`、`keys.md`、`errors.md`、`openapi.yaml` 与 `pkg/client`、Linux Xvfb CI、Windows E2E 脚本、三平台二进制。

## 阶段 2：补齐操作面与交付（已完成）

`file/remove|move|copy|download|upload`、`clipboard`（含 Linux 纯 Go X selection）、`process/list|kill`、`exec` 后台模式与 `exec/jobs/*`、`window/resize|close_all`、`record/*`、`metrics`、Linux user service 与 Windows 计划任务安装脚本、会话前置条件脚本、升级回滚、`legacy/` 删除、本组文档。

## 阶段 3：可选能力（已完成）

- `a11y/tree`：Windows 与 Linux 都由 vm-agent 自身作为 helper 的限时子进程采集：Windows 走原生 UI Automation COM 接口（`IUIAutomation`，含标准 Win32 控件的代理），Linux 走 AT-SPI；`window_id` 省略时取前台窗口。
- Wayland：截图走 portal Screenshot，输入走 uinput 虚拟设备；uinput 不可写且存有 RemoteDesktop restore token 时改用 portal RemoteDesktop。窗口、剪贴板、录屏与无障碍在 Wayland 下返回 `unsupported`。
- 多显示器矩阵测试：Xorg dummy 驱动上的六种布局（单屏、主屏在左、主屏在右且错位、三屏、副屏在上、混合缩放）。
- 可选对象存储上传器：`POST /v1/record/{session_id}/upload`，`-upload-s3-*` 参数。
- 鉴权与 TLS：按 D9 不实现，中间件位置保留。

## 需要真实环境的验证

以下验收在开发环境中无法执行，需在目标 VM 上运行脚本确认（已在真实 VM 上完成的部分见 spec 13.1 末尾的验证范围）：

- Windows 10/11：`deploy/windows-e2e.ps1` 在 100%、125%、150% 缩放下的点击误差、截图延迟、锁屏、重启与 Notepad 回归；安装、升级、回滚脚本；`a11y/tree`。以上各项已在 Windows Server 2022（10.0.20348）真实 VM 上通过，10/11 仍待验证。
- Windows 多显示器与混合 DPI。
- Ubuntu 24.04 真实桌面（真实 GNOME 只在 Ubuntu 22.04.3 on Xorg 上验证过）。
- GNOME/KDE Wayland 真实会话：冷启动 probe、Screenshot portal 的弹窗与授权行为、uinput 输入，以及 `VM_AGENT_PORTAL_SETUP=1` 一次性批准后的 RemoteDesktop 回退（只在 sway 与伪造的 portal 上测试过）。
- `deploy/linux/preconditions.sh --system`（需要 root 的真实 VM）。
