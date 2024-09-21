# process_exporter-lite

# Docker Compose
```
services:
  process-exporter-lite:
    image: ghcr.io/xd003/process-exporter-lite:latest
    container_name: process-exporter-lite
    restart: unless-stopped
    network_mode: "host"
    pid: "host"
    volumes:
      - /proc:/host/proc:ro
      - /sys:/host/sys:ro
```
