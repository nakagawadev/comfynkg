# How it works

## Units

`apply` writes one systemd unit per instance, `comfynkg-<inst>.service`, running as the owner of `root`. The unit doesn't hold any ComfyUI flags: its `ExecStart` is `comfynkg run <inst>`, which reads the file, waits for the Tailscale IP, logs the full command and `exec`s ComfyUI from the instance's own venv, with GPUs numbered as `nvidia-smi` numbers them (`CUDA_DEVICE_ORDER=PCI_BUS_ID`). Changing a flag means editing the file and restarting; `apply` restarts only instances whose unit changed. Units of instances removed from the file are stopped and deleted. Hand-made units are never touched.

## Restarts and drain

`restart --wait` turns drain on, waits until nothing is running or pending, then restarts and waits for ComfyUI to answer again (up to 3 minutes; a crash fails at once). While draining, the [comfyui_nakagawa](https://github.com/nakagawadev/comfyui_nakagawa) node answers new prompts with `503` and `Retry-After`, so a load balancer or client can send them elsewhere. If the queue doesn't empty within `--timeout`, drain is turned off and the instance is skipped (`--force` restarts anyway). Ctrl-C turns drain off too. Without the node, `--wait` just waits for the queue.

Drain lives in memory, so a restarted instance always comes back taking prompts.

## Installs and updates

comfynkg calls [comfy-cli](https://github.com/Comfy-Org/comfy-cli) for installs, custom nodes and snapshots, always as the owner and always on the instance's own folder. `create --nodes-from` copies the custom nodes of an existing install through a snapshot, taken before installing anything. Older installs that only have the classic ComfyUI-Manager clone in `custom_nodes` work too. After the copy, each node's `requirements.txt` is installed (never one that would change PyTorch), and `create` loads every custom node once: if any fails to import, it says which and why instead of reporting success. `check` runs the same test on existing instances. `create` also warns when the source keeps model files in its own `models/` folder (older setups often did, e.g. SAM or detector models): the new instance only sees the shared paths of the models file.

ComfyUI starts with `--enable-manager --disable-manager-ui` unless your flags pick another Manager mode: Manager's background work (like switching a registry node's version at startup) runs, but its UI and install endpoints stay off.

`update core` and `update nodes` snapshot every target first, then update one instance (the canary), check that every custom node still imports, restart it with `--wait` and check its health. `node install|uninstall|enable|disable` work the same way. If it fails, that instance is rolled back and the run stops; if it works, the rest follow one at a time.

## Takeover

To move an existing ComfyUI under comfynkg: `create` a new instance (copying its nodes), then `takeover <inst> --old <unit>`. comfynkg drains the old instance, stops and disables its unit, and starts the managed one on the same port. If the new one isn't healthy, the old unit is started again. The old unit file stays until you delete it.

## Model sync

The server with a `sync:` section runs one `comfynkg-sync@<type>` unit per model type. Every 60 seconds it compares the source folder with each destination (`rsync` over SSH, through Tailscale) and copies what changed. Files still being written are left for the next round, and files appear on the destination only when complete. Before anything that deletes, the source must exist, be the expected filesystem and contain files, so an unmounted disk never wipes the workers. `sync status` shows the last round per destination.

## New releases

`apply` installs `comfynkg-releases@check.timer` on every server: soon after boot and then every 6 hours it asks GitHub (ComfyUI, comfynkg) and PyPI (comfy-cli) for their latest stable release and keeps the answer in `/var/lib/comfynkg/releases.json`. A source that doesn't answer keeps its last value. `comfynkg releases` shows it; nothing else ever waits on the network for it. `status` reads it too: instances on an older ComfyUI get a yellow version, and the output ends with what's newer and how to get it, e.g. `ComfyUI 0.44.0 is out · run: comfynkg update core all`. A check older than 7 days is ignored.

## Health and status

comfynkg talks to every instance on `127.0.0.1`: `/system_stats` for health and version, `/queue` for the queue, and the node's `/nkg/metrics` (RSS, prompts ok/error, custom nodes that failed to import) and `/nkg/drain` when it is installed. systemd gives the state, restarts and uptime; `nvidia-smi` gives each instance's VRAM (by process), the GPU model, and the driver and CUDA version in the header.

An instance that is active but doesn't answer yet shows `starting` for its first 2 minutes (ComfyUI loads its custom nodes before it answers), then `down`.

ComfyUI keeps running when a custom node fails to import and only logs it. With comfyui_nakagawa 0.1.1 or later, `status` shows `ok  1 node failed` in yellow and, at the end, which nodes and `run: comfynkg check <inst>` to see why.

With `hosts:` in the config, `status` also runs `comfynkg status --local` on each of those servers over SSH, as the owner of the instances, and prints their tables below. A server that doesn't answer shows as `unreachable`.
