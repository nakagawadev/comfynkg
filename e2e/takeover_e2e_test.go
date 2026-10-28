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

// Takeover story: a legacy unit is replaced on the same port; when the new instance crashes the
// legacy one comes back.
func TestTakeover(t *testing.T) {
	cleanup(t)
	t.Cleanup(func() {
		cleanup(t)
		sh(t, "systemctl disable --now comfy_legacy.service >/dev/null 2>&1; rm -f /etc/systemd/system/comfy_legacy.service; systemctl daemon-reload")
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
}
