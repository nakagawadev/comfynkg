//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

const legacyUnit = `[Unit]
Description=legacy ComfyUI (test)
[Service]
User=comfy
WorkingDirectory=/home/comfy/legacy/comfy_y
ExecStart=/bin/bash -c 'cd /home/comfy/legacy/comfy_y && exec .venv/bin/python main.py --listen 127.0.0.1 --port 8192 --cpu --extra-model-paths-config /home/comfy/config/image/extra_model_paths.yaml'
Restart=on-failure
[Install]
WantedBy=multi-user.target
`

const phase4Config = `host: devbox
root: /home/comfy/instances
flags: --disable-metadata --cpu
models:
  image: /home/comfy/config/image/extra_model_paths.yaml
  video: /home/comfy/config/video/extra_model_paths.yaml
plugin: {}

comfy_x:
  type: image
  port: 8192
  gpu: 0
  flags: --bogus-flag
  enabled: false
`

const syncSection = `
sync:
  image:
    source: /home/comfy/models/image/
    to:
      - comfy@node0:/home/comfy/models/image/
`

func onNode(t *testing.T, cmd string) string {
	return sh(t, "runuser -u comfy -- ssh -o BatchMode=yes comfy@node0 "+fmt.Sprintf("%q", cmd))
}

// Phase 4 story: takeover of a legacy unit (rolled back when the new one crashes, then done),
// model sync to a worker with the empty-source guard, sync units, self-update.
func TestPhase4(t *testing.T) {
	cleanup(t)
	t.Cleanup(func() {
		cleanup(t)
		sh(t, "systemctl disable --now comfy_legacy.service >/dev/null 2>&1; rm -f /etc/systemd/system/comfy_legacy.service /etc/systemd/system/comfynkg-sync@*.service; systemctl daemon-reload")
	})
	newInstance(t, "comfy_x")
	sh(t, "rm -rf /home/comfy/config/*/comfyui_nakagawa.yaml; test -d /home/comfy/legacy/comfy_y || (runuser -u comfy -- mkdir -p /home/comfy/legacy && runuser -u comfy -- cp -a /home/comfy/base /home/comfy/legacy/comfy_y)")
	sh(t, fmt.Sprintf("install -o comfy -g comfy -m 644 testdata/sleep_node.py /home/comfy/legacy/comfy_y/custom_nodes/nkg_sleep.py; ln -sfn %s /home/comfy/legacy/comfy_y/custom_nodes/comfyui_nakagawa", pluginSrc))
	os.WriteFile("/etc/systemd/system/comfy_legacy.service", []byte(legacyUnit), 0o644)
	writeConfig(t, phase4Config)
	mustOK(t, "apply") // writes the plugin file with drain on for the legacy instance too
	sh(t, "systemctl daemon-reload && systemctl enable --now comfy_legacy.service")
	waitHealthy(t, 8192, 2*time.Minute)

	// guards
	if r := nkg(t, "takeover", "comfy_x", "--old", "comfynkg-comfy_x"); r.code != 2 {
		t.Errorf("takeover of a comfynkg unit: exit %d", r.code)
	}

	// takeover where the new instance crashes (bad flag): rolled back, legacy keeps serving
	queueSleep(t, 8192, 3)
	r := nkg(t, "takeover", "comfy_x", "--old", "comfy_legacy", "--wait")
	if r.code != 1 {
		t.Fatalf("failing takeover: exit %d", r.code)
	}
	contains(t, r.out, "old unit comfy_legacy.service: draining... queue empty", "stopping and disabling comfy_legacy.service... ok",
		"crashed on start", "rolling back to comfy_legacy.service", "comfy_legacy.service is back... ok")
	if active(t, "comfy_legacy.service") != "active" || !healthy(8192) {
		t.Fatal("legacy unit not back")
	}
	if cfg, _ := os.ReadFile(cfgPath); !strings.Contains(string(cfg), "enabled: false") {
		t.Fatal("failed takeover left comfy_x enabled in the file")
	}

	// fix the flag, take over for real
	mustOK(t, "set", "comfy_x", "flags", "--preview-method auto")
	r = nkg(t, "takeover", "comfy_x", "--old", "comfy_legacy.service", "--wait")
	if r.code != 0 {
		t.Fatalf("takeover: exit %d", r.code)
	}
	contains(t, r.out, "(comfy_x) enabled, starting... ok", "done: comfy_legacy.service replaced; remove it when you're ready: rm /etc/systemd/system/comfy_legacy.service")
	if active(t, "comfy_legacy.service") != "inactive" || active(t, "comfynkg-comfy_x") != "active" || !healthy(8192) {
		t.Fatal("takeover did not swap the units")
	}
	sh(t, "systemctl is-enabled comfy_legacy.service | grep -q disabled")

	// --- sync -----------------------------------------------------------------
	sh(t, "rm -rf /var/lib/comfynkg-sync-image") // a previous run's state would satisfy the waits below
	onNode(t, "rm -rf /home/comfy/models/image && mkdir -p /home/comfy/models/image && echo stale > /home/comfy/models/image/stale.bin")
	sh(t, `runuser -u comfy -- bash -c 'rm -rf /home/comfy/models/image.bak /home/comfy/models/image/*; mkdir -p /home/comfy/models/image/checkpoints /home/comfy/models/image/loras
		head -c 2000000 /dev/urandom > /home/comfy/models/image/checkpoints/a.safetensors
		head -c 300000 /dev/urandom > /home/comfy/models/image/loras/b.safetensors
		touch -d "-2 hours" /home/comfy/models/image/checkpoints/a.safetensors /home/comfy/models/image/loras/b.safetensors'`)

	// a destination on this machine is refused
	writeConfig(t, phase4Config+strings.Replace(syncSection, "comfy@node0", "comfy@localhost", 1))
	if r := nkg(t, "apply"); r.code != 2 || !strings.Contains(r.out, "is this machine") {
		t.Fatalf("local destination: exit %d", r.code)
	}

	writeConfig(t, phase4Config+syncSection)
	out := mustOK(t, "sync", "dry-run")
	contains(t, out, "> image -> comfy@node0:/home/comfy/models/image/", "copy    checkpoints/a.safetensors (1.9M)", "DELETE  stale.bin", "nothing changed (dry run)")
	if onNode(t, "ls /home/comfy/models/image") != "stale.bin\n" {
		t.Fatal("dry run changed the worker")
	}

	// apply creates and starts the sync unit; its first round copies everything
	out = mustOK(t, "apply")
	contains(t, out, "> comfynkg-sync@image.service started")
	contains(t, sh(t, "systemctl show -p User --value comfynkg-sync@image.service"), "comfy")
	deadline := time.Now().Add(time.Minute)
	for !strings.Contains(sh(t, "cat /var/lib/comfynkg-sync-image/state.json 2>/dev/null || true"), `"last_sync": "2`) {
		if time.Now().After(deadline) {
			t.Fatalf("sync loop did not sync:\n%s", sh(t, "journalctl -u comfynkg-sync@image --no-pager -n 20"))
		}
		time.Sleep(time.Second)
	}
	contains(t, onNode(t, "find /home/comfy/models/image -type f | sort"), "checkpoints/a.safetensors", "loras/b.safetensors")
	if strings.Contains(onNode(t, "ls /home/comfy/models/image"), "stale.bin") {
		t.Error("stale file not deleted")
	}
	contains(t, mustOK(t, "sync", "status"), "image  comfy@node0:/home/comfy/models/image/  idle", "2 ", "2.2M")
	sh(t, "systemctl stop comfynkg-sync@image.service") // the rest uses `sync now`

	// a file still being written waits for the next round
	sh(t, "runuser -u comfy -- bash -c 'head -c 1000 /dev/urandom > /home/comfy/models/image/loras/new.safetensors'")
	contains(t, mustOK(t, "sync", "now"), "> image -> comfy@node0:/home/comfy/models/image/ waiting (1 files still changing)")
	sh(t, "touch -d '-2 hours' /home/comfy/models/image/loras/new.safetensors")
	contains(t, mustOK(t, "sync", "now"), "idle (1 files, 1000 bytes in")

	// empty-source guard: an unmounted (empty) folder never wipes the worker
	sh(t, "mv /home/comfy/models/image /home/comfy/models/image.bak && runuser -u comfy -- mkdir -p /home/comfy/models/image/checkpoints")
	r = nkg(t, "sync", "now")
	if r.code != 1 || !strings.Contains(r.out, "error source missing or empty") {
		t.Fatalf("empty source: exit %d", r.code)
	}
	contains(t, mustOK(t, "sync", "dry-run"), "source missing or empty (nothing would run)")
	contains(t, onNode(t, "find /home/comfy/models/image -type f | wc -l"), "3")
	contains(t, mustOK(t, "sync", "status"), "error", "source missing or empty")
	sh(t, "rm -rf /home/comfy/models/image && mv /home/comfy/models/image.bak /home/comfy/models/image")

	// dropping the section removes the unit
	writeConfig(t, phase4Config)
	contains(t, mustOK(t, "apply"), "> comfynkg-sync@image.service removed (no sync section)")
	if r := nkg(t, "sync", "status"); r.code != 2 {
		t.Errorf("sync without a section: exit %d", r.code)
	}

	// --- self-update ------------------------------------------------------------
	sh(t, "cp /usr/local/bin/comfynkg /tmp/comfynkg-new && printf '#!/bin/sh\\necho nope\\n' > /tmp/not-comfynkg && chmod +x /tmp/not-comfynkg")
	if r := nkg(t, "self-update", "/tmp/not-comfynkg"); r.code != 1 || !strings.Contains(r.out, "is not a working comfynkg") {
		t.Fatalf("bad binary: exit %d", r.code)
	}
	contains(t, mustOK(t, "self-update", "/tmp/comfynkg-new"), "is already installed")
	// a "newer" build: a wrapper that reports another version and runs the real binary otherwise
	sh(t, `printf '#!/bin/sh\n[ "$1" = version ] && echo "comfynkg v9.9.9" && exit 0\nexec /tmp/comfynkg-new "$@"\n' > /tmp/comfynkg-v9 && chmod +x /tmp/comfynkg-v9`)
	contains(t, mustOK(t, "self-update", "/tmp/comfynkg-v9"), "-> v9.9.9", "running instances keep going")
	contains(t, mustOK(t, "version"), "comfynkg v9.9.9")
	sh(t, "install -m 755 /tmp/comfynkg-new /usr/local/bin/comfynkg") // the wrapper runs the real binary, which sees its own version
	if strings.Contains(mustOK(t, "version"), "v9.9.9") {
		t.Fatal("not restored")
	}
}
