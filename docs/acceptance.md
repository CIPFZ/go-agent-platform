# 完整版本验收

本清单记录实际验收证据；不能用接口骨架代替功能完成。

| 范围 | 必须通过的场景 | 当前状态 |
| --- | --- | --- |
| Agent 生命周期 | 稳定身份、重复注册、撤销、独立心跳、断网退避重连、优雅停止 | 已通过本地测试与 Ubuntu 真实回归 |
| 任务可靠性 | FIFO、幂等参数冲突、租约令牌、续租、过期结果拒绝、结果重放、限制不安全重试 | 已通过单元、HTTP 集成与重启回归 |
| 执行与取消 | 独立有界 worker、服务端取消到进程树、超时、输出上限 | 已通过；补充了执行器取消超时兜底 |
| 持久化 | 重启不丢任务、Agent 注册表、结果；异常写入可观测 | 任务、Agent 注册表和外部制品卷已实现并通过重启回归；本地 JSONL 与外部 HTTP 审计收集器均支持失败降级告警 |
| 身份权限 | 控制面与 Agent 凭据分离、跨 Agent 拒绝、撤销、轮换、HTTPS | Bearer 边界、目标 Agent 与租约校验、撤销、凭据文件和 TLS 证书热轮换、0600 原子密钥文件轮换已实现；外部 Vault/KMS 接入仍属于部署环境配置 |
| 文件与制品 | 符号链接边界、并发覆盖、大小/哈希、二进制上传下载、期限清理 | 路径/符号链接/大小/哈希、受限 Base64 制品传输、独立持久化制品卷、S3/MinIO 适配器、受保护下载端点和访问时的过期清理已通过；Ubuntu MinIO 实证见 `docs/evidence/ubuntu-minio-integration.md` |
| 桌面 | 真实显示会话能力探测、截图上传和预览、无桌面明确错误 | Guest Agent 会探测显示会话和截图 helper；无显示会话明确失败，截图制品可上传并由 CMS 预览 |
| CMS | VM UUID 绑定、状态、提交/取消、结果/制品展示、权限/审计 | 后端路由、权限、UUID 解析、前端提交/刷新/取消和图片制品预览已实现；Ubuntu 真实 CMS→Gateway→Agent 状态、命令、取消和制品链路已通过 |
| 部署 | systemd/镜像安装、健康指标、升级/回滚、凭据注入、CI | Gateway 参数、CMS Compose 环境注入、健康检查、systemd 模板、可重复安装器和 CI 已实现；本机与 Ubuntu 均已完成 staged install、release install、升级和 rollback 演练 |
| 回归与性能 | 真实 HTTP 并发、故障/重启回归、可重复负载、时延/错误率/资源记录 | Ubuntu 真实 E2E、5 秒 HTTP 并发基准和 `/usr/bin/time -v` 资源采样、本地 race/vet 已通过；`deploy/resource_curve.sh` 已提供 1/2/5 秒资源曲线采集，当前样本见 `docs/evidence/ubuntu-resource-curve.csv`，后续按发布周期持续归档 CSV |

CMS 工作副本已定位到 Ubuntu `/home/ytq/work/go-web-frame`，分支为 `base-frame`。该副本包含用户尚未提交的虚拟机与其他模块改动；集成应基于现状，用逐文件比较保护现有内容，禁止覆盖或把无关改动纳入提交。

Agent Platform 工作副本 `/home/ytq/work/go-agent-platform`，对应本机仓库和 GitHub main。远端操作仅通过 rdev。rdev 自身仓库仍不可修改。
