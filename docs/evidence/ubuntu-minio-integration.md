# Ubuntu MinIO 制品存储验收

在 Ubuntu `ubuntu-home` 上使用现有 MinIO 容器的临时凭据文件运行：

```text
AGENT_S3_INTEGRATION=1 AGENT_S3_ENDPOINT=http://127.0.0.1:9000 \
AGENT_S3_BUCKET=agent-platform-integration \
AGENT_S3_ACCESS_FILE=<temporary-file> AGENT_S3_SECRET_FILE=<temporary-file> \
AGENT_S3_INSECURE=1 go test ./internal/artifacts -run TestS3StoreMinIOIntegration -count=1
```

结果：`ok github.com/CIPFZ/go-agent-platform/internal/artifacts 0.037s`。

测试覆盖凭据可用性、桶创建/复用、对象写入、读取、哈希内容回读和删除；临时凭据文件在命令结束后清理，未修改 CMS 或长期服务配置。
