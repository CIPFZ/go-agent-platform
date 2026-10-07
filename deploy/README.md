# Deployment

The gateway will run as a standalone systemd service or container on the Ubuntu host. The guest agent will be installed into VM images and started as a systemd service.

Deployment manifests are intentionally kept separate from the CMS repository. The CMS may later call the gateway's registration and task APIs, but it does not embed this service.

## systemd shape

The binaries can be installed under `/usr/local/bin` with a dedicated unprivileged user. The agent workspace should be a private directory and the Gateway URL should use HTTPS in production.

```ini
# /etc/systemd/system/go-agent-gateway.service
[Service]
ExecStart=/usr/local/bin/go-agent-gateway -addr 127.0.0.1:8081
Restart=on-failure
User=agent-gateway
NoNewPrivileges=true
PrivateTmp=true
```

```ini
# /etc/systemd/system/go-guest-agent.service
[Service]
ExecStart=/usr/local/bin/go-guest-agent -gateway https://gateway.example -state-file /var/lib/go-guest-agent/id -workspace /var/lib/go-guest-agent/workspace
Restart=on-failure
User=guest-agent
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ReadWritePaths=/var/lib/go-guest-agent
```

Ready-to-install unit templates are in `deploy/systemd/`. Copy them to `/etc/systemd/system/`, create the service users and state directories, install the binaries, and place the token files with mode `0600`. The gateway and agent reread token files on every request, so rotating a file does not require a process restart. Add `-tls-cert` and `-tls-key` to the gateway unit when terminating HTTPS locally; certificate files are reread on each new TLS handshake, so an atomic replacement rotates HTTPS without a restart. Otherwise terminate TLS at the trusted reverse proxy. Set `-artifact-dir` to a private persistent volume for production-sized artifact payloads, and use `-audit-url` plus `-audit-token-file` to forward audit events to the external collector.

`deploy/install.sh GATEWAY_BINARY AGENT_BINARY` performs the directory, release, and systemd-unit installation as one repeatable step. Set `ROOT=/tmp/staging PREFIX=/usr/local VERSION=...` for a filesystem-only rehearsal; omit `ROOT` on the host installation. `ENABLE=1` additionally reloads systemd and starts both units on a real host. `deploy/install_test.sh` validates the staged path without changing host services.

For a binary upgrade, run `deploy/release.sh install VERSION GATEWAY_BINARY AGENT_BINARY` with `PREFIX=/usr/local` (or a temporary prefix for rehearsal). The script keeps the prior `current` target as `previous`; `deploy/release.sh rollback` switches the symlink targets back. State files remain under `/var/lib/go-agent-gateway` and `/var/lib/go-guest-agent`, so a binary rollback does not discard task or identity state. Run `deploy/release_test.sh` before installing on a host to rehearse the install, upgrade, and rollback flow without changing system paths.

On Ubuntu, run `deploy/resource_curve.sh output.csv` to collect repeated HTTP benchmark durations and maximum RSS samples. The default 1/2/5-second curve can be extended with `DURATIONS=5,10` when the host can absorb the larger queue; the CSV is suitable for comparing releases over time.

For S3-compatible artifact storage, set `-artifact-s3-endpoint`, `-artifact-s3-bucket`, `-artifact-s3-access-key-file`, and `-artifact-s3-secret-key-file`; use `-artifact-s3-insecure` only for a trusted local MinIO endpoint. The bucket must exist before starting the Gateway.
