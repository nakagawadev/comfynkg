# How it works

## Units

`apply` writes one systemd unit per instance, `comfynkg-<inst>.service`, running as the owner of `root`. The unit doesn't hold any ComfyUI flags: its `ExecStart` is `comfynkg run <inst>`, which reads the file, waits for the Tailscale IP, logs the full command and `exec`s ComfyUI from the instance's own venv. Changing a flag means editing the file and restarting; `apply` restarts only instances whose unit changed. Units of instances removed from the file are stopped and deleted. Hand-made units are never touched.

## Restarts and drain

`restart --wait` turns drain on, waits until nothing is running or pending, then restarts and waits for ComfyUI to answer again (up to 3 minutes; a crash fails at once). While draining, the [comfyui_nakagawa](https://github.com/nakagawadev/comfyui_nakagawa) node answers new prompts with `503` and `Retry-After`, so a load balancer or client can send them elsewhere. If the queue doesn't empty within `--timeout`, drain is turned off and the instance is skipped (`--force` restarts anyway). Ctrl-C turns drain off too. Without the node, `--wait` just waits for the queue.

Drain lives in memory, so a restarted instance always comes back taking prompts.

## Installs and updates

comfynkg calls [comfy-cli](https://github.com/Comfy-Org/comfy-cli) for installs, custom nodes and snapshots, always as the owner and always on the instance's own folder. `create --nodes-from` copies the custom nodes of an existing install through a snapshot.

`update core` and `update nodes` snapshot every target first, then update one instance (the canary), restart it with `--wait` and check its health. If it fails, that instance is rolled back and the run stops; if it works, the rest follow one at a time.

## Takeover

To move an existing ComfyUI under comfynkg: `create` a new instance (copying its nodes), then `takeover <inst> --old <unit>`. comfynkg drains the old instance, stops and disables its unit, and starts the managed one on the same port. If the new one isn't healthy, the old unit is started again. The old unit file stays until you delete it.

## Model sync

The server with a `sync:` section runs one `comfynkg-sync@<type>` unit per model type. Every 60 seconds it compares the source folder with each destination (`rsync` over SSH, through Tailscale) and copies what changed. Files still being written are left for the next round, and files appear on the destination only when complete. Before anything that deletes, the source must exist, be the expected filesystem and contain files, so an unmounted disk never wipes the workers. `sync status` shows the last round per destination.

## Health and status

comfynkg talks to every instance on `127.0.0.1`: `/system_stats` for health and version, `/queue` for the queue, and the node's `/nkg/metrics` and `/nkg/drain` when it is installed.
