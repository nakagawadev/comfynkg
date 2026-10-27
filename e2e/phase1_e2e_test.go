//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

const phase1Config = `# test config
host: devbox
root: /home/comfy/instances
flags: --disable-metadata --cpu
models:
  image: /home/comfy/config/image/extra_model_paths.yaml
  video: /home/comfy/config/video/extra_model_paths.yaml

comfy_a:
  type: image
  port: 8188
  gpu: 0
  enabled: true

comfy_b:   # the video one
  type: video
  port: 8189
  gpu: 0
  enabled: true
`

// Phase 1 story: apply creates and starts two instances, status shows them, disable one,
// set a flag on the other, re-apply is a no-op, removing an instance removes its unit.
func TestPhase1(t *testing.T) {
	cleanup(t)
	t.Cleanup(func() { cleanup(t) })
	newInstance(t, "comfy_a")
	newInstance(t, "comfy_b")
	writeConfig(t, phase1Config)

	// dry run changes nothing
	out := mustOK(t, "apply", "--dry-run")
	contains(t, out, "> devbox:8188 (comfy_a) would create unit comfynkg-comfy_a.service", "would enable and start", "nothing changed (dry run)")
	if _, err := os.Stat("/etc/systemd/system/comfynkg-comfy_a.service"); err == nil {
		t.Fatal("dry run wrote a unit")
	}

	out = mustOK(t, "apply")
	contains(t, out, "(comfy_a) unit created", "(comfy_a) enabled, starting... ok", "(comfy_b) enabled, starting... ok", "done: 0 restarted, 2 started, 0 unchanged")
	if !healthy(8188) || !healthy(8189) {
		t.Fatal("instances not healthy after apply")
	}
	// listens on loopback and the "tailscale" IP, owned by comfy
	sh(t, fmt.Sprintf("curl -sf http://%s:8188/system_stats >/dev/null", vmIP(t)))
	if u := strings.TrimSpace(sh(t, "ps -o user= -p $(systemctl show -p MainPID --value comfynkg-comfy_a)")); u != "comfy" {
		t.Fatalf("comfy_a runs as %q", u)
	}
	cmdline := sh(t, "tr '\\0' ' ' < /proc/$(systemctl show -p MainPID --value comfynkg-comfy_b)/cmdline")
	contains(t, cmdline, "main.py --listen 127.0.0.1,"+vmIP(t), "--port 8189", "--extra-model-paths-config /home/comfy/config/video/extra_model_paths.yaml", "--disable-metadata --cpu --enable-manager --disable-manager-ui")
	contains(t, sh(t, "journalctl -u comfynkg-comfy_b --no-pager -o cat | grep 'comfynkg run comfy_b:'"), "comfynkg run comfy_b: /home/comfy/instances/comfy_b/.venv/bin/python main.py")

	out = mustOK(t, "status")
	contains(t, out, "devbox · comfynkg", "NAME", "comfy_a  image  yes", "running", "ok")
	contains(t, mustOK(t, "run", "comfy_a", "--print"), "/home/comfy/instances/comfy_a/.venv/bin/python main.py --listen 127.0.0.1,")

	// apply again: nothing to do
	out = mustOK(t, "apply")
	contains(t, out, "(comfy_a) unit unchanged, enabled", "done: 0 restarted, 0 started, 2 unchanged")

	// disable comfy_b: stopped and disabled, file edited with comment kept
	out = mustOK(t, "disable", "comfy_b")
	contains(t, out, "(comfy_b) disabled, stopped")
	if a := active(t, "comfynkg-comfy_b"); a != "inactive" {
		t.Fatalf("comfy_b is %s", a)
	}
	cfg, _ := os.ReadFile(cfgPath)
	contains(t, string(cfg), "comfy_b: # the video one", "enabled: false", "# test config")

	// set a flag: only comfy_a restarts, the flag reaches ComfyUI
	pid := sh(t, "systemctl show -p MainPID --value comfynkg-comfy_a")
	out = mustOK(t, "set", "comfy_a", "flags", "--preview-method auto")
	contains(t, out, "set comfy_a.flags = --preview-method auto", "(comfy_a) restarting... ok", "done: 1 restarted")
	if sh(t, "systemctl show -p MainPID --value comfynkg-comfy_a") == pid {
		t.Fatal("comfy_a did not restart")
	}
	contains(t, sh(t, "tr '\\0' ' ' < /proc/$(systemctl show -p MainPID --value comfynkg-comfy_a)/cmdline"), "--disable-metadata --cpu --preview-method auto")

	// invalid edits are refused and leave the file alone
	before, _ := os.ReadFile(cfgPath)
	r := nkg(t, "set", "comfy_a", "port", "8189")
	if r.code != 2 || !strings.Contains(r.out, "already used by comfy_a") && !strings.Contains(r.out, "already used by") {
		t.Fatalf("duplicate port: exit %d", r.code)
	}
	r = nkg(t, "set", "comfy_a", "flags", "--port 9000")
	if r.code != 2 || !strings.Contains(r.out, "--port is set by comfynkg") {
		t.Fatalf("owned flag: exit %d", r.code)
	}
	if after, _ := os.ReadFile(cfgPath); string(after) != string(before) {
		t.Fatal("refused edit changed the file")
	}

	// memory_max changes the unit -> restart with diff
	out = mustOK(t, "set", "global", "memory_max", "6G")
	contains(t, out, "(comfy_a) unit updated:", "+MemoryMax=6G", "(comfy_a) restarting... ok")
	contains(t, sh(t, "systemctl show -p MemoryMax --value comfynkg-comfy_a"), "6442450944")

	// stop / start
	contains(t, mustOK(t, "stop", "comfy_a"), "(comfy_a) stopping... ok")
	contains(t, mustOK(t, "start", "comfy_a"), "(comfy_a) starting... ok")

	// an instance removed from the file loses its unit
	writeConfig(t, strings.Split(phase1Config, "comfy_b:")[0])
	out = mustOK(t, "apply")
	contains(t, out, "comfynkg-comfy_b.service removed (not in the file)")
	if _, err := os.Stat("/etc/systemd/system/comfynkg-comfy_b.service"); err == nil {
		t.Fatal("unit not removed")
	}

	// a unit with the same name not made by comfynkg is never overwritten
	os.WriteFile("/etc/systemd/system/comfynkg-comfy_b.service", []byte("[Service]\nExecStart=/bin/true\n"), 0o644)
	writeConfig(t, phase1Config)
	r = nkg(t, "apply")
	if r.code == 0 || !strings.Contains(r.out, "not managed by comfynkg") {
		t.Fatalf("overwrote an unmanaged unit: exit %d", r.code)
	}
	os.Remove("/etc/systemd/system/comfynkg-comfy_b.service")

	// usage errors
	if r := nkg(t, "start", "nope"); r.code != 2 {
		t.Fatalf("unknown instance exit %d", r.code)
	}
	_ = time.Second
}
