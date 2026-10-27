# Security

comfynkg runs as root because it manages systemd. The ComfyUI instances, and everything they load, do not.

## The owner is not trusted

Custom nodes run arbitrary code, and anyone who can reach ComfyUI-Manager can install more. So comfynkg assumes the owner of the instances may be compromised and makes sure that never turns into root:

- **The config belongs to root.** comfynkg refuses a `/etc/comfynkg.yaml` owned by another user or writable by group or others: only root decides what root runs.
- **Instances never run as root.** They run as the owner of `root`, and a `root` owned by uid 0 is refused.
- **Root stays out of the owner's folders.** git, comfy-cli, uv, rsync and every file comfynkg reads or writes there (plugin config, sync state, copied nodes) go through `runuser -u <owner>`. A symlink planted by the owner can only reach what the owner could already reach.
- **Unit files can't be injected.** Values that end up in a unit are restricted to safe characters and line breaks are refused.

Commands that only read (`status`, `drain status`, `sync status`) run as any user; everything that changes something requires root. With `hosts:`, `status` reaches the other servers over ssh as the owner and runs only `comfynkg status --local` there.

Root owns only `/etc/comfynkg.yaml`, the units in `/etc/systemd/system/` and the binaries in `/usr/local/bin`.

## Network

ComfyUI listens on `127.0.0.1` and the server's Tailscale address only, never on a public interface. The drain endpoints of comfyui_nakagawa answer only loopback callers with a loopback `Host` header. Model sync uses SSH over Tailscale.

## Releases

Release binaries are built by GitHub Actions (actions pinned to commit SHAs) and published with a `SHA256SUMS` file; `self-update` refuses a binary whose checksum doesn't match or that doesn't run on the machine. The checksum comes from the same release, so it protects against broken downloads, not against a compromised GitHub account. Build from source if that matters to you.
