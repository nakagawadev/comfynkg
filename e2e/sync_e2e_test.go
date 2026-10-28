//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"
)

// Sync story: dry-run, the sync unit's first round, files still being written wait, the empty-source
// guard never wipes the worker, a local destination is refused, dropping the section removes the unit.
func TestSync(t *testing.T) {
	cleanup(t)
	t.Cleanup(func() {
		cleanup(t)
		sh(t, "systemctl disable --now comfynkg-sync@image.service >/dev/null 2>&1; rm -f /etc/systemd/system/comfynkg-sync@*.service; systemctl daemon-reload")
	})
	newInstance(t, "comfy_x")
	var out string
	var r result

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
	out = mustOK(t, "sync", "dry-run")
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
}
