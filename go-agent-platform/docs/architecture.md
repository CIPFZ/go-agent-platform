# Architecture

```text
GoWebFrame CMS  -- VM lifecycle -->  Ubuntu / QEMU VM
                                      |
                                      | outbound HTTP/HTTPS
                                      v
                              Agent Gateway
```

## Boundary

The CMS creates, starts, stops, edits and deletes virtual machines. The gateway does not own VM lifecycle. A guest agent runs inside each VM and maintains an outbound, authenticated connection to the gateway. This keeps the guest network surface small and makes the transport consistent across hosts.

## Initial service split

1. `gateway`: registration, heartbeat, task routing, queueing, result storage and audit events.
2. `guest-agent`: capability probe, bounded worker pool, command/screenshot/file handlers and local policy checks.
3. `protocol`: versioned request and result envelopes shared by both binaries.

The first protocol version will use ordinary HTTP requests with long polling. Streaming and HTTP/2 can be added without changing the task model.
