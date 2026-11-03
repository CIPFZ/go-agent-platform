# legacy

已废弃的 gateway + guest-agent 任务队列架构代码，按 `docs/spec.md` 第 11 节保留至阶段 2 结束后删除。

所有 Go 文件带 `//go:build legacy` 标签，默认的 `go build ./...`、`go test ./...`、`go vet ./...` 与 CI 均不包含它们。如需临时查看：

```bash
go vet -tags legacy ./legacy/...
go test -tags legacy ./legacy/...
```
