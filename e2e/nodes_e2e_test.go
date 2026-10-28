//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

const nodesConfig = `host: devbox
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

comfy_c:
  type: image
  port: 8190
  gpu: 1
  enabled: true
`

// Nodes story: check finds nodes that don't import, a node installed from a git ref brings its
// requirements, a canary that breaks ComfyUI or doesn't import is rolled back, snapshots, the comfy-cli
// passthrough guard, node and core updates.
func TestNodes(t *testing.T) {
	cleanup(t)
	t.Cleanup(func() { cleanup(t); sh(t, "rm -rf /home/comfy/instances/comfy_c /home/comfy/instances/.snapshots") })
	sh(t, "rm -rf /home/comfy/instances/comfy_c /home/comfy/instances/.snapshots")
	newInstance(t, "comfy_a")
	newInstance(t, "comfy_c")
	sh(t, fmt.Sprintf("install -o comfy -g comfy -m 644 testdata/sleep_node.py %s/comfy_a/custom_nodes/nkg_sleep.py", root))
	writeConfig(t, nodesConfig)
	mustOK(t, "apply")
	var out string

	// check: every custom node imports; a broken one is named with its reason; the instance keeps running
	contains(t, mustOK(t, "check", "comfy_a"), "(comfy_a) custom nodes... ok")
	brokenFile := root + "/comfy_a/custom_nodes/nkg_broken.py"
	sh(t, "runuser -u comfy -- sh -c 'echo import nkg_missing_module_xyz > "+brokenFile+"'")
	r := nkg(t, "check", "comfy_a")
	if r.code != 1 || !strings.Contains(r.out, "1 failed to import") || !strings.Contains(r.out, "nkg_broken.py: No module named 'nkg_missing_module_xyz'") {
		t.Errorf("check with a broken node: exit %d", r.code)
	}
	os.Remove(brokenFile)
	if a := active(t, "comfynkg-comfy_a"); a != "active" {
		t.Fatalf("check touched the running instance: %s", a)
	}

	// node install --ref from git: cloned as comfy, requirements installed with uv, instance restarted
	good := gitRepo(t, "goodnode", map[string]string{
		"__init__.py":      "NODE_CLASS_MAPPINGS = {}\n",
		"requirements.txt": "six\n",
	})
	out = mustOK(t, "node", "install", "comfy_c", good, "--ref", "main")
	contains(t, out, "(comfy_c) snapshot saved:", "(comfy_c) installing "+good+"@main...", "restarting... ok", "done: 1 updated")
	contains(t, sh(t, "stat -c %U /home/comfy/instances/comfy_c/custom_nodes/goodnode; /home/comfy/instances/comfy_c/.venv/bin/python -c 'import six; print(\"six-ok\")'"), "comfy", "six-ok")

	// canary: a node that kills ComfyUI on import fails on the first instance, is rolled back, the rest is untouched
	killer := gitRepo(t, "killer", map[string]string{"__init__.py": "import os\nos._exit(3)\n"})
	r = nkg(t, "node", "install", "all", killer, "--ref", "main")
	if r.code != 1 {
		t.Fatalf("killer canary: exit %d", r.code)
	}
	contains(t, r.out, "(comfy_a) canary: installing "+killer+"@main...", "import check: ComfyUI didn't start", "(comfy_a) rolling back to", "(comfy_a) restarting... ok", "stopped: comfy_a failed and was rolled back; 0 updated")
	if strings.Contains(r.out, "(comfy_c) installing") {
		t.Error("kept going after the canary failed")
	}
	for _, n := range []string{"comfy_a", "comfy_c"} {
		if _, err := os.Stat(fmt.Sprintf("%s/%s/custom_nodes/killer", root, n)); err == nil {
			t.Errorf("%s still has the killer node", n)
		}
	}
	if !healthy(8188) || !healthy(8190) {
		t.Fatal("instances not healthy after rollback")
	}

	// a node whose dependency is missing never reaches the running instance: the import check catches
	// it before the restart, names the node and the reason, and the change is rolled back
	broken := gitRepo(t, "brokennode", map[string]string{"__init__.py": "import nkg_missing_dependency_xyz\nNODE_CLASS_MAPPINGS = {}\n"})
	r = nkg(t, "node", "install", "comfy_c", broken, "--ref", "main")
	if r.code != 1 {
		t.Fatalf("broken node: exit %d", r.code)
	}
	contains(t, r.out, "brokennode doesn't import: No module named 'nkg_missing_dependency_xyz'", "(comfy_c) rolling back to", "stopped: comfy_c failed and was rolled back")
	if _, err := os.Stat(root + "/comfy_c/custom_nodes/brokennode"); err == nil {
		t.Error("comfy_c still has the broken node")
	}
	if !healthy(8190) {
		t.Fatal("comfy_c not healthy after the rollback")
	}

	// snapshot + rollback
	out = mustOK(t, "snapshot", "comfy_c")
	contains(t, out, "(comfy_c) snapshot saved: /home/comfy/instances/.snapshots/comfy_c/")
	contains(t, mustOK(t, "rollback", "comfy_c"), "(comfy_c) restoring", "restarting... ok")

	// passthrough: allowed and refused
	contains(t, mustOK(t, "comfy", "comfy_c", "--", "node", "simple-show", "installed"), "> devbox:8190 (comfy_c)")
	for _, bad := range [][]string{{"--workspace", "/tmp", "node", "list"}, {"launch"}, {"set-default", "/x"}, {"update", "cli"}} {
		r := nkg(t, append([]string{"comfy", "comfy_c", "--"}, bad...)...)
		if r.code != 2 || !strings.Contains(r.out, "refused:") {
			t.Errorf("passthrough %v: exit %d", bad, r.code)
		}
	}

	// update nodes (single instance: no canary label) and core to the latest stable release
	contains(t, mustOK(t, "update", "nodes", "comfy_c"), "(comfy_c) updating nodes...", "done: 1 updated")
	if testing.Short() {
		t.Log("-short: skipping update core (downloads ComfyUI)")
	} else {
		out = mustOK(t, "update", "core", "comfy_c", "--version", "latest")
		contains(t, out, "(comfy_c) updating ComfyUI to latest...", "restarting... ok", "done: 1 updated")
		if v := comfyVersion(t, 8190); v == "" || strings.Contains(v, "-") {
			t.Errorf("version after update core latest: %q", v)
		}
	}

	// dry runs change nothing
	contains(t, mustOK(t, "update", "nodes", "all", "--dry-run"), "(comfy_a) would updating nodes", "nothing changed (dry run)")
}
