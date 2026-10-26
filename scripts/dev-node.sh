#!/bin/bash
# Provision the second test machine, a sync worker. Runs inside it as root.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq rsync openssh-server >/dev/null
id comfy >/dev/null 2>&1 || useradd -m -s /bin/bash comfy
install -d -o comfy -g comfy -m 700 /home/comfy/.ssh
install -d -o comfy -g comfy /home/comfy/models /home/comfy/models/image /home/comfy/models/video
systemctl enable --now ssh >/dev/null 2>&1 || systemctl enable --now sshd
echo "dev node ready"
