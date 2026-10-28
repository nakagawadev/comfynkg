# Commands

`comfynkg <command> [args] [flags]`, as root; `status`, `drain status` and `sync status` only read, so any user can run them. `<inst>` is an instance name, several are allowed; `all` means every enabled instance.

Global flags: `--config <path>`, `--dry-run` (show what would change, change nothing), `--verbose` (print every external command and HTTP call).

## Instances

| Command | Does |
|---|---|
| `status [inst...] [-w] [--local]` | Versions (comfynkg, comfy-cli, uv, NVIDIA driver, CUDA), then per instance: state, port, GPU, ComfyUI version, RSS, VRAM, queue, prompts ok/error and uptime since the last start, restarts, health; also the servers in `hosts:` (over ssh) unless `--local`; `-w` refreshes every 2 s |
| `apply` | Make systemd match the file |
| `start\|stop <inst\|all>` | Start or stop the unit |
| `enable\|disable <inst>` | Set `enabled` in the file and apply |
| `set <inst\|global> <key> <value>` | Change `type`, `port`, `gpu`, `flags` or `memory_max` and apply |
| `logs <inst> [-f] [-n 200]` | The instance's journal |
| `restart <inst\|all> [--wait] [--timeout 600] [--force] [--parallel]` | Restart; `--wait` drains and waits for the queue first |
| `drain on\|off <inst\|all>` · `drain status` | Stop or resume taking prompts (needs the plugin) |

## Install and update

| Command | Does |
|---|---|
| `create <inst> --type T --port N --gpu N [--nodes-from <path\|inst>] [--nodes-venv <venv>] [--version X]` | Install ComfyUI for a new instance (disabled), optionally copying custom nodes from another install |
| `remove <inst> [--purge]` | Remove the unit and the file entry; `--purge` also deletes its folder |
| `rename <inst> <new> [--timeout 600] [--force]` | New name for the key, the folder and the unit, same port; drains and waits first, undoes everything if a step fails |
| `node install\|uninstall\|enable\|disable <inst\|all> <node> [--ref <branch\|tag>]` | Custom nodes; `--ref` installs from git |
| `update core <inst\|all> [--version X]` | Update ComfyUI, canary first, rollback on failure |
| `update nodes <inst\|all> [node...] [--remote]` | Update custom nodes, same way |
| `update cli` | Upgrade comfy-cli |
| `check <inst\|all>` | Load every custom node in a throwaway ComfyUI (CPU, temporary user folder) and list the ones that fail to import, with the reason; the running instance isn't touched |
| `snapshot <inst>` · `rollback <inst> [file]` | Save or restore a custom node snapshot |
| `comfy <inst> -- <args>` | Run comfy-cli on that instance (options that would break comfynkg are refused) |

## Migration, sync, the binary

| Command | Does |
|---|---|
| `takeover <inst> --old <unit> [--wait]` | Replace a hand-made unit with the managed one, same port |
| `sync status [image\|video] [-w]` · `sync now\|dry-run [image\|video]` | Model sync state (while copying: done / total and speed; `-w` refreshes every 2 s), a round now, or what a round would copy and delete |
| `releases [--fetch]` | Latest stable ComfyUI, comfynkg and comfy-cli, as last checked (a timer runs `--fetch` every 6 hours) |
| `self-update [vX.Y.Z\|binary]` | Install the latest release, a given one, or a local binary |
| `version` | comfynkg, comfy-cli and uv versions |

`run <inst>` and `sync loop <type>` are what the systemd units start; you don't call them yourself (`run <inst> --print` shows the exact ComfyUI command).

## Output and exit codes

Every line about an instance starts with `> <host>:<port> (<name>)`:

```
> node0:8188 (comfy_a) draining... queue empty (12s), restarting... ok (18s)
> node0:8189 (comfy_b) draining... timeout after 10m0s (1 running, 0 pending), drain off, skipped
done: 1 restarted, 1 skipped (comfy_b)
```

| Exit | Meaning |
|---|---|
| 0 | Everything ok |
| 1 | An operation failed |
| 2 | Usage or config error, nothing changed |
| 3 | Some instances failed or were skipped, others succeeded |
