# Config

Everything comfynkg manages on a server is described in `/etc/comfynkg.yaml`. Every command reads and validates it first; an invalid file changes nothing (exit 2).

The file must belong to root and not be writable by group or others (`chown root:root`, `chmod 644`), otherwise comfynkg refuses to run. Use `--config <path>` or `COMFYNKG_CONFIG` for another location.

## Example

```yaml
host: node0                        # label in messages; default: hostname up to the first dot
root: /home/comfy/instances        # every instance lives in {root}/{name}; its owner runs them
flags: --disable-metadata          # for every instance
memory_max: 48G                    # optional systemd MemoryMax=, global or per instance
models:                            # extra_model_paths.yaml per type
  image: /home/comfy/config/image/extra_model_paths.yaml
  video: /home/comfy/config/video/extra_model_paths.yaml

plugin:                            # optional, writes comfyui_nakagawa.yaml (see below)
  stats: { enabled: true, dir: /home/comfy/stats }

hosts: [node1, node2]              # optional: other servers `status` also shows, asked over ssh

sync:                              # optional, only on the server that sends models
  image:
    source: /home/comfy/models/image/
    to: [comfy@node1:/home/comfy/models/image/, comfy@node2:/home/comfy/models/image/]

comfy_a:                           # any other top-level key is an instance
  type: image
  port: 8188
  gpu: 0
  enabled: true

comfy_b:
  type: video
  port: 8189
  gpu: 1
  flags: --lowvram                 # appended after the global flags
  memory_max: 64G
  enabled: false
```

## Instance keys

| Key | Required | Notes |
|---|---|---|
| `type` | yes | `image` or `video`: picks the `models` file |
| `port` | yes | Unique on the server |
| `gpu` | yes | Becomes `--cuda-device` |
| `flags` | no | Extra ComfyUI flags |
| `memory_max` | no | Overrides the global one |
| `enabled` | no | Default `false`: installed but not running |

comfynkg always sets `--listen 127.0.0.1,<Tailscale IP>`, `--port`, `--cuda-device` and `--extra-model-paths-config`; putting them in `flags` is an error.

Warnings (the command still runs): an instance without `--disable-metadata`, an enabled instance whose folder doesn't exist yet, and an owner that can't write in its own home (Hestia leaves it to root) while the folders tools keep there (`.cache`, `.config`, `.local`, `.triton`, `.nv`) are missing; the warning prints the `install -d` that creates them.

## Changing the file

Edit it and run `comfynkg apply`, or let comfynkg edit it for you: `enable`, `disable`, `set <inst|global> <key> <value>`, `create` and `remove` validate the change and apply it.

## Other servers in `status`

With `hosts:`, `status` also runs `comfynkg status --local` on each of those servers over ssh, as the owner of the instances (the same key and `~/.ssh/config` that model sync uses; a plain name means `<owner>@name`), and prints one table per server. A server that doesn't answer shows as `unreachable` and the rest still print. `status <inst>` finds the instance on whichever server has it. The other servers need comfynkg too, and nothing else is run there.

## Plugin

With a `plugin:` section, `apply` writes a `comfyui_nakagawa.yaml` next to each models file for the [comfyui_nakagawa](https://github.com/nakagawadev/comfyui_nakagawa) node, with drain turned on and `stats` passed through. `plugin.instances` takes per-instance overrides by name (comfynkg translates them to ports). A file comfynkg didn't write is never overwritten.
