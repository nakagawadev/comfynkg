//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

const secConfig = `host: devbox
root: /home/comfy/instances
flags: --disable-metadata --cpu
models:
  image: /home/comfy/config/image/extra_model_paths.yaml
  video: /home/comfy/config/video/extra_model_paths.yaml

comfy_a:
  type: image
  port: 8188
  gpu: 0
  enabled: false
`

// Security story: the owner can't use the config, a symlink in its folders
// or a crafted source instance to make root write or read files outside them.
func TestSecurity(t *testing.T) {
	cleanup(t)
	t.Cleanup(func() {
		cleanup(t)
		sh(t, "rm -rf /home/comfy/config/*/comfyui_nakagawa.yaml /home/comfy/instances/comfy_s /home/comfy/instances/comfy_a/custom_nodes/leak.py /var/lib/comfynkg-sync-image /etc/nkg-pwn-*")
	})
	newInstance(t, "comfy_a")
	sh(t, "rm -rf /etc/nkg-pwn-* /home/comfy/config/*/comfyui_nakagawa.yaml")

	// 1. the config must belong to root and not be writable by others
	writeConfig(t, secConfig)
	sh(t, "chown comfy "+cfgPath)
	if r := nkg(t, "status"); r.code != 2 || !strings.Contains(r.out, "must be owned by root") {
		t.Fatalf("owner-owned config: exit %d", r.code)
	}
	sh(t, "chown root "+cfgPath+" && chmod 666 "+cfgPath)
	if r := nkg(t, "status"); r.code != 2 {
		t.Fatalf("world-writable config: exit %d", r.code)
	}
	sh(t, "chmod 644 "+cfgPath)

	// 2. a line break in root can't inject unit directives
	evil := "/home/comfy/evil\nExecStartPre=+/tmp/nkg-pwn"
	sh(t, fmt.Sprintf("runuser -u comfy -- mkdir -p %q", evil))
	writeConfig(t, strings.Replace(secConfig, "root: /home/comfy/instances", fmt.Sprintf("root: %q", evil), 1))
	if r := nkg(t, "apply"); r.code != 2 || !strings.Contains(r.out, "may only contain") {
		t.Fatalf("newline in root: exit %d", r.code)
	}
	if strings.Contains(sh(t, "cat /etc/systemd/system/comfynkg-*.service 2>/dev/null || true"), "ExecStartPre") {
		t.Fatal("directive injected into a unit")
	}
	sh(t, "rm -rf /home/comfy/evil*")

	// 3. a symlink in place of the plugin file is refused, nothing written through it
	writeConfig(t, secConfig+"plugin: {}\n")
	sh(t, "runuser -u comfy -- ln -s /etc/nkg-pwn-plugin /home/comfy/config/image/comfyui_nakagawa.yaml")
	if r := nkg(t, "apply"); r.code == 0 || !strings.Contains(r.out, "not a regular file") {
		t.Fatalf("plugin symlink: exit %d", r.code)
	}
	absent(t, "/etc/nkg-pwn-plugin")
	sh(t, "rm -f /home/comfy/config/image/comfyui_nakagawa.yaml")
	contains(t, mustOK(t, "apply"), "> plugin config written: /home/comfy/config/image/comfyui_nakagawa.yaml")
	contains(t, sh(t, "stat -c %U /home/comfy/config/image/comfyui_nakagawa.yaml"), "comfy")

	// 4. symlinks planted in the sync state dir: root never writes through them
	sh(t, `runuser -u comfy -- bash -c 'mkdir -p /home/comfy/models/image/checkpoints && echo x > /home/comfy/models/image/checkpoints/s.bin && touch -d "-2 hours" /home/comfy/models/image/checkpoints/s.bin'`)
	sh(t, "rm -rf /var/lib/comfynkg-sync-image && install -d -o comfy -g comfy /var/lib/comfynkg-sync-image")
	sh(t, "runuser -u comfy -- ln -s /etc/nkg-pwn-tmp /var/lib/comfynkg-sync-image/state.json.comfynkg-tmp")
	sh(t, "runuser -u comfy -- ln -s /etc/nkg-pwn-state /var/lib/comfynkg-sync-image/state.json")
	writeConfig(t, secConfig+"sync:\n  image:\n    source: /home/comfy/models/image/\n    to: [comfy@node0:/home/comfy/models/image/]\n")
	nkg(t, "sync", "now") // the state write fails as the owner; the sync itself may succeed
	absent(t, "/etc/nkg-pwn-tmp")
	absent(t, "/etc/nkg-pwn-state")
	sh(t, "rm -f /var/lib/comfynkg-sync-image/state.json.comfynkg-tmp")
	mustOK(t, "sync", "now")
	if fi, err := os.Lstat("/var/lib/comfynkg-sync-image/state.json"); err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("state.json after a clean round: %v %v", fi, err)
	}
	absent(t, "/etc/nkg-pwn-state")

	if testing.Short() {
		t.Log("-short: skipping 5 (a real create)")
		return
	}
	// 5. create --nodes-from: a single-file node that is a symlink to a root-only file is not copied
	writeConfig(t, secConfig)
	sh(t, "runuser -u comfy -- ln -sf /etc/shadow /home/comfy/instances/comfy_a/custom_nodes/leak.py")
	sh(t, "rm -rf /home/comfy/instances/comfy_s")
	os.Setenv("COMFYNKG_INSTALL_ARGS", "--cpu")
	out := mustOK(t, "create", "comfy_s", "--type", "image", "--port", "8195", "--gpu", "0", "--nodes-from", "comfy_a")
	contains(t, out, "leak.py: not readable by its owner, skipped")
	absent(t, "/home/comfy/instances/comfy_s/custom_nodes/leak.py")
	if strings.Contains(sh(t, "cat /home/comfy/instances/comfy_s/custom_nodes/*.py 2>/dev/null || true"), "root:") {
		t.Fatal("a root-only file leaked into the new instance")
	}
	mustOK(t, "remove", "comfy_s", "--purge")
}
