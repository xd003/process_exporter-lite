# process_exporter-lite

# Docker Compose
```
services:
  process-exporter-lite:
    image: image: ghcr.io/xd003/process-exporter-lite:latest
    container_name: process-exporter-lite
    restart: unless-stopped
    environment:
      - PROC_MOUNT=/host/proc
      - SYS_MOUNT=/host/sys
    network_mode: "host"
    pid: "host"
    volumes:
      - /proc:/host/proc:ro
      - /sys:/host/sys:ro
```
