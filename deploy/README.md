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

The service units are deployment guidance; certificate provisioning, credentials and durable task/audit storage remain environment-specific.
