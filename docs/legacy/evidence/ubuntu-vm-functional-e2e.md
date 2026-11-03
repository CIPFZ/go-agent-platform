# Ubuntu 虚机功能验收证据

验收日期：2026-10-08（Asia/Shanghai）

本次测试直接针对 libvirt 中运行的 `ubuntu2404`，虚机地址为 `192.168.122.253`。Guest Agent 通过虚机网卡连接 Gateway，Agent ID 为 `agent-6f5cf20537034139b36fd1b78a7c4ba2`。验收结束后已移除临时注入的二进制、systemd 服务、凭据和工作目录，虚机恢复运行。

可重复脚本：`deploy/vm-functional-e2e.sh`。脚本在 Ubuntu 主机上运行，但每个任务都由虚机内的 Guest Agent 执行，完成后轮询 Gateway 的任务结果。

通过场景：

- `command.exec` 命令执行和标准输出。
- 同一幂等键重放返回同一任务；同键不同动作返回 `409`。
- `file.write`、`file.read`、制品下载和内容校验。
- 已存在文件拒绝覆盖、软链接越界拒绝、文件大小上限拒绝。
- 输出长度上限、命令超时、进程树取消及取消后的子进程检查。
- 禁止命令策略拒绝。
- 无图形会话时截图返回明确的 `no_display`，没有伪造成功结果。

恢复回归：优雅关闭虚机后 Gateway 将 Agent 标记为 `offline`；再次启动后同一 Agent ID 自动恢复为 `online`，重启后的命令返回 `reboot-ok`。

## CMS 实际联动

CMS 后端通过 `AGENT_GATEWAY_URL`、`AGENT_GATEWAY_TOKEN` 配置连接 Gateway，并以 libvirt VM UUID（无连字符）作为 Agent ID。使用 CMS 实际数据库和登录会话验证了 `ubuntu2404`：

- CMS Agent 状态接口返回 `online`，能力为命令、文件读取和文件写入。
- CMS 提交的命令在虚机内返回 `cms-vm-ok`。
- CMS 发起的长命令取消后最终状态为 `cancelled`。
- CMS 发起的文件写入返回 Base64 制品，页面可据此预览或下载。

该 CMS 配置已提交到 `go-web-frame` 的 `base-frame` 分支，提交为 `6b3db91`。
