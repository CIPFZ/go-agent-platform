# Windows 虚机功能验收证据

验收日期：2026-10-08（Asia/Shanghai）

本次测试直接针对 libvirt 中的 `win10` 虚机，地址为 `192.168.122.142`，VM UUID 为 `0f2eb371-c0d4-46eb-aca1-7d2c5bd77012`，Gateway 使用无连字符的 UUID 作为 Agent ID：`0f2eb371c0d446ebaca17d2c5bd77012`。Windows Agent 在真实桌面登录会话中启动并注册，主机名为 `DESKTOP-8LEU455`。

可重复脚本：[deploy/windows-vm-functional-e2e.sh](../../deploy/windows-vm-functional-e2e.sh)。每个任务都由 Windows 虚机内的 Windows Agent 执行。

通过场景：

- PowerShell 命令执行和标准输出。
- 幂等重放、同键冲突、文件写入、文件读取和制品下载。
- 文件覆盖保护、跨工作区路径拒绝、3 MiB 文件大小上限。
- 输出长度限制、命令超时、策略拒绝。
- PowerShell 启动的 `ping.exe` 子进程取消；Windows Job Object 确认整棵进程树被清理。
- Windows Agent 重启后的重新注册和任务执行。
- Windows 桌面截图通过 PowerShell/System.Drawing 生成 PNG 制品并下载校验。

恢复回归：停止 Windows 虚机后 Gateway 将 Agent 标记为 `offline`；再次启动并进入桌面后同一 Agent ID 恢复为 `online`。

## CMS 实际联动

CMS 使用实际数据库和登录会话，通过 `win10` 的 VM UUID 找到 Windows Agent：

- 状态接口返回 `online`，能力为命令、文件读取和文件写入。
- CMS 提交的 PowerShell 命令在 Windows 虚机内返回 `cms-windows-ok`。
- CMS 发起的长命令取消后最终状态为 `cancelled`。
- CMS 发起的文件写入返回 Base64 制品，页面可预览或下载。

验收结束后已恢复 Windows SAM、系统注册表、用户注册表、临时启动目录和 libvirt QEMU Agent 通道配置。
