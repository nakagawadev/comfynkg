//go:build e2e

package e2e

import (
	"os"
	"strings"
	"testing"
	"time"
)

// Rename story: a running instance is renamed after its queue empties, comes back on the same port
// with its folder, unit and plugin override renamed; refused names change nothing; a failure after
// the move is undone.
func TestRename(t *testing.T) {
	cleanup(t)
	t.Cleanup(func() { cleanup(t); sh(t, "rm -rf "+root+"/comfy_z "+root+"/comfy_y") })
	pluginInstances(t)
	mustOK(t, "apply")
	var out string
	var r result

	// rename while a prompt runs: it finishes first, then the instance comes back under the new
	// name on the same port, with its folder, unit and plugin override renamed
	queueSleep(t, 8188, 3)
	time.Sleep(500 * time.Millisecond)
	out = mustOK(t, "rename", "comfy_a", "comfy_z")
	contains(t, out, "(comfy_a) draining... queue empty", "(comfy_a) stopped", "moved to /home/comfy/instances/comfy_z",
		"comfynkg-comfy_a.service removed (not in the file)", "(comfy_z) enabled, starting... ok", "done: comfy_a is now comfy_z")
	if a := active(t, "comfynkg-comfy_z"); a != "active" {
		t.Fatalf("comfy_z is %s", a)
	}
	if _, err := os.Stat(root + "/comfy_a"); err == nil {
		t.Fatal("old folder still there")
	}
	if cfg, _ := os.ReadFile(cfgPath); strings.Count(string(cfg), "comfy_z:") != 2 || strings.Contains(string(cfg), "comfy_a") {
		t.Fatalf("file not renamed everywhere:\n%s", cfg)
	}
	if n := strings.Count(sh(t, "cat /home/comfy/stats/8188/*/events.jsonl"), `"status": "success"`); n == 0 {
		t.Fatal("the running prompt didn't finish")
	}
	// refused before touching anything: a name in use, a bad name
	before, _ := os.ReadFile(cfgPath)
	if r := nkg(t, "rename", "comfy_z", "comfy_b"); r.code != 2 || !strings.Contains(r.out, "already exists") {
		t.Fatalf("rename to an existing name: exit %d", r.code)
	}
	if r := nkg(t, "rename", "comfy_z", "Bad Name"); r.code != 2 || !strings.Contains(r.out, "not a valid instance name") {
		t.Fatalf("rename to a bad name: exit %d", r.code)
	}
	if after, _ := os.ReadFile(cfgPath); string(after) != string(before) || active(t, "comfynkg-comfy_z") != "active" {
		t.Fatal("a refused rename changed something")
	}
	contains(t, mustOK(t, "rename", "comfy_z", "comfy_a"), "done: comfy_z is now comfy_a")

	// a failure after the move is undone: a hand-made unit with the new name makes apply refuse
	os.WriteFile("/etc/systemd/system/comfynkg-comfy_y.service", []byte("[Service]\nExecStart=/bin/true\n"), 0o644)
	before, _ = os.ReadFile(cfgPath)
	r = nkg(t, "rename", "comfy_a", "comfy_y")
	os.Remove("/etc/systemd/system/comfynkg-comfy_y.service")
	if r.code == 0 || !strings.Contains(r.out, "undoing the rename") || !strings.Contains(r.out, "rename failed and was undone") {
		t.Fatalf("failed rename: exit %d", r.code)
	}
	if after, _ := os.ReadFile(cfgPath); string(after) != string(before) {
		t.Fatal("the file wasn't restored")
	}
	if _, err := os.Stat(root + "/comfy_a/main.py"); err != nil {
		t.Fatal("the folder wasn't moved back")
	}
	if a := active(t, "comfynkg-comfy_a"); a != "active" {
		t.Fatalf("comfy_a is %s after the undo", a)
	}
}
