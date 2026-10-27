#!/bin/sh
# Install comfynkg, uv and comfy-cli on a Linux server, as root. Safe to run again (it upgrades).
#
#   curl -fsSL https://raw.githubusercontent.com/nakagawadev/comfynkg/main/install.sh | sh
#   VERSION=v0.1.3 sh install.sh     # a given release instead of the latest
set -eu

repo=https://github.com/nakagawadev/comfynkg/releases
[ "$(id -u)" = 0 ] || { echo "run as root" >&2; exit 1; }
[ "$(uname -s)" = Linux ] || { echo "comfynkg runs on Linux only" >&2; exit 1; }
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac
if [ -n "${VERSION:-}" ]; then url=$repo/download/$VERSION; else url=$repo/latest/download; fi

# uv, in /usr/local/bin so every user has it
if ! command -v uv >/dev/null 2>&1; then
  echo "> installing uv"
  curl -fsSL https://astral.sh/uv/install.sh | env UV_INSTALL_DIR=/usr/local/bin UV_NO_MODIFY_PATH=1 sh
fi

# comfy-cli, shared by every user (uv-managed Python kept out of root's home)
echo "> installing comfy-cli"
export UV_TOOL_DIR=/opt/comfynkg/tools UV_TOOL_BIN_DIR=/usr/local/bin UV_PYTHON_INSTALL_DIR=/opt/comfynkg/python
uv tool install --upgrade comfy-cli
chmod -R a+rX /opt/comfynkg

# comfynkg, checked against the release's SHA256SUMS
echo "> installing comfynkg ($url)"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fsSL -o "$tmp/comfynkg-linux-$arch" "$url/comfynkg-linux-$arch"
curl -fsSL -o "$tmp/SHA256SUMS" "$url/SHA256SUMS"
(cd "$tmp" && sha256sum -c --ignore-missing --quiet SHA256SUMS)
install -o root -g root -m 755 "$tmp/comfynkg-linux-$arch" /usr/local/bin/comfynkg

comfynkg version
