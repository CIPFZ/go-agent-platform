# Deployment

The gateway will run as a standalone systemd service or container on the Ubuntu host. The guest agent will be installed into VM images and started as a systemd service.

Deployment manifests are intentionally kept separate from the CMS repository. The CMS may later call the gateway's registration and task APIs, but it does not embed this service.
