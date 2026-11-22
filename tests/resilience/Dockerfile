FROM debian:trixie-slim
ENV DEBIAN_FRONTEND=noninteractive
RUN printf '#!/bin/sh\nexit 101\n' > /usr/sbin/policy-rc.d && chmod +x /usr/sbin/policy-rc.d \
 && apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates restic smbclient samba openssh-server postgresql postgresql-client iproute2 \
 && rm -f /usr/sbin/policy-rc.d && rm -rf /var/lib/apt/lists/*
