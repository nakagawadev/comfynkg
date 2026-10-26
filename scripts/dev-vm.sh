#!/bin/bash
# Provision the local Linux test machine. Runs inside the VM as root.
set -euo pipefail

export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq git curl rsync openssh-server python3 python3-venv build-essential >/dev/null

id comfy >/dev/null 2>&1 || useradd -m -s /bin/bash comfy

# uv and comfy-cli for every user
if ! command -v uv >/dev/null; then
  curl -LsSf https://astral.sh/uv/install.sh | env UV_INSTALL_DIR=/usr/local/bin UV_NO_MODIFY_PATH=1 sh
fi
export UV_TOOL_DIR=/opt/comfynkg/tools UV_TOOL_BIN_DIR=/usr/local/bin UV_PYTHON_INSTALL_DIR=/opt/comfynkg/python
command -v comfy >/dev/null || uv tool install comfy-cli
chmod -R a+rX /opt/comfynkg

install -d -o comfy -g comfy /home/comfy/instances /home/comfy/config /home/comfy/models /home/comfy/config/image /home/comfy/config/video \
  /home/comfy/models/image/checkpoints /home/comfy/models/video/checkpoints
for t in image video; do
  cat > /home/comfy/config/$t/extra_model_paths.yaml <<EOF
$t:
  base_path: /home/comfy/models/$t
  checkpoints: checkpoints
EOF
  chown comfy:comfy /home/comfy/config/$t/extra_model_paths.yaml
done

chown -R comfy:comfy /home/comfy/models /home/comfy/config  # install -d creates parents as root

# One base ComfyUI install (CPU) to copy from in phase 1 tests; phase 3 creates them with comfynkg.
if [ ! -d /home/comfy/base/.venv ]; then
  rm -rf /home/comfy/base  # comfy-cli refuses an existing non-git folder
  runuser -u comfy -- env HOME=/home/comfy comfy --skip-prompt --workspace /home/comfy/base install --cpu
fi
# Fake `tailscale ip -4` (the VM has no Tailscale): prints the VM's own IPv4.
if [ ! -x /usr/local/bin/tailscale ] || grep -q comfynkg-fake /usr/local/bin/tailscale; then
  cat > /usr/local/bin/tailscale <<'EOF2'
#!/bin/sh
# comfynkg-fake: dev VM only
[ "$1 $2" = "ip -4" ] && ip -4 -o addr show eth0 | awk '{split($4,a,"/"); print a[1]}' && exit 0
echo "fake tailscale: only 'ip -4'" >&2; exit 1
EOF2
  chmod 755 /usr/local/bin/tailscale
fi
echo "dev vm ready"
