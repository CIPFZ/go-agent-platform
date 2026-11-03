# go-agent-platform

`vm-agent`：运行在虚拟机桌面会话内的单二进制 HTTP 服务，让外部 AI Agent 或自动化程序同步调用截图、输入、窗口、进程、文件、剪贴板、录屏等桌面操作。

唯一设计依据见 [`docs/spec.md`](docs/spec.md)。

## 构建

```bash
go build ./...
GOOS=windows GOARCH=amd64 go build -o dist/vm-agent.exe ./cmd/vm-agent
GOOS=linux   GOARCH=amd64 go build -o dist/vm-agent-linux-amd64 ./cmd/vm-agent
GOOS=linux   GOARCH=arm64 go build -o dist/vm-agent-linux-arm64 ./cmd/vm-agent
```

## 测试

```bash
gofmt -l .
go vet ./...
go test ./...
```
