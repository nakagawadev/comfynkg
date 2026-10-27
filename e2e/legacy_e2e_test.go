//go:build e2e

package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// A pre-comfy-cli install, like the ones on real servers: the venv lives outside the folder,
// and ComfyUI-Manager is a clone in custom_nodes instead of the pip package.
const (
	legacyDir  = "/home/comfy/legacy/comfy_old"
	legacyVenv = "/home/comfy/.legacy-venvs/venv_old"
	managerSrc = "/home/comfy/testrepos/ComfyUI-Manager"
	gitNode    = "https://github.com/BlenderNeko/ComfyUI_ADV_CLIP_emb" // Manager can't restore file:// repos
)

const legacyConfig = `host: devbox
root: /home/comfy/instances
flags: --disable-metadata --cpu
models:
  image: /home/comfy/config/image/extra_model_paths.yaml
`

// Legacy story: create --nodes-from an install with the classic Manager snapshots it with
// cm-cli.py and restores git and single-file nodes; a source no Manager can read fails before
// anything is installed.
func TestLegacyNodes(t *testing.T) {
	cleanup(t)
	t.Cleanup(func() {
		cleanup(t)
		sh(t, "rm -rf /home/comfy/instances/comfy_l /home/comfy/instances/comfy_m")
	})
	sh(t, "rm -rf /home/comfy/instances/comfy_l /home/comfy/instances/comfy_m "+legacyDir+" /home/comfy/.legacy-venvs")
	sh(t, fmt.Sprintf(`test -d %[1]s || runuser -u comfy -- git clone -q https://github.com/Comfy-Org/ComfyUI-Manager %[1]s`, managerSrc))
	sh(t, fmt.Sprintf(`runuser -u comfy -- bash -c 'set -e
mkdir -p /home/comfy/legacy /home/comfy/.legacy-venvs
cp -a %[1]s %[2]s && mv %[2]s/.venv %[3]s
uv pip uninstall -q --python %[3]s/bin/python comfyui_manager
cp -a %[4]s %[2]s/custom_nodes/ComfyUI-Manager
git clone -q %[5]s %[2]s/custom_nodes/ComfyUI_ADV_CLIP_emb
echo "NODE_CLASS_MAPPINGS = {}" > %[2]s/custom_nodes/legacy_file.py'`, base, legacyDir, legacyVenv, managerSrc, gitNode))
	writeConfig(t, legacyConfig)
	t.Setenv("COMFYNKG_INSTALL_ARGS", "--cpu")

	out := mustOK(t, "create", "comfy_l", "--type", "image", "--port", "8196", "--gpu", "0", "--nodes-from", legacyDir, "--nodes-venv", legacyVenv)
	contains(t, out, "snapshot saved with the classic ComfyUI-Manager (custom_nodes/ComfyUI-Manager)",
		"(comfy_l) copied legacy_file.py", "done: comfy_l created (disabled)")
	if strings.Index(out, "snapshot saved") > strings.Index(out, "installing ComfyUI") {
		t.Error("snapshot taken after the install")
	}
	nodes := sh(t, "ls /home/comfy/instances/comfy_l/custom_nodes")
	contains(t, nodes, "ComfyUI_ADV_CLIP_emb", "legacy_file.py")
	if strings.Contains(nodes, "ComfyUI-Manager") {
		t.Error("the classic Manager clone was copied next to the pip Manager")
	}
	contains(t, sh(t, "stat -c %U /home/comfy/instances/comfy_l/custom_nodes/ComfyUI_ADV_CLIP_emb /home/comfy/instances/comfy_l/custom_nodes/legacy_file.py"), "comfy\ncomfy\n")
	contains(t, mustOK(t, "enable", "comfy_l"), "(comfy_l) enabled, starting... ok")

	// a registry node: update nodes switches it on the canary restart; an unknown node is an error, not "updated"
	mustOK(t, "comfy", "comfy_l", "--", "node", "install", "comfyui_nakagawa@0.0.4")
	pyproject := "/home/comfy/instances/comfy_l/custom_nodes/comfyui_nakagawa/pyproject.toml"
	contains(t, sh(t, "grep -m1 ^version "+pyproject), `"0.0.4"`)
	out = mustOK(t, "update", "nodes", "comfy_l", "comfyui_nakagawa", "--remote")
	contains(t, out, "--mode', 'remote'", "done: 1 updated")
	if strings.Contains(out, "Permission denied") {
		t.Error("an owner command ran in root's working directory")
	}
	if strings.Contains(sh(t, "grep -m1 ^version "+pyproject), `"0.0.4"`) {
		t.Error("comfyui_nakagawa still at 0.0.4 after the update and restart")
	}
	// default: Manager's cache (filled by the run above and by the instance itself), no catalog download
	if out = mustOK(t, "update", "nodes", "comfy_l", "comfyui_nakagawa"); strings.Contains(out, "'remote'") {
		t.Error("update nodes without --remote used remote mode")
	}
	if r := nkg(t, "update", "nodes", "comfy_l", "not-a-real-node"); r.code != 1 || !strings.Contains(r.out, "not updated: not-a-real-node (not installed); a registry node needs fresh registry data: retry with --remote") {
		t.Errorf("unknown node: exit %d", r.code)
	}

	// no Manager at all: fails in seconds, nothing installed, nothing added to the file
	sh(t, "rm -rf "+legacyDir+"/custom_nodes/ComfyUI-Manager")
	start := time.Now()
	r := nkg(t, "create", "comfy_m", "--type", "image", "--port", "8197", "--gpu", "0", "--nodes-from", legacyDir, "--nodes-venv", legacyVenv)
	if r.code != 1 || !strings.Contains(r.out, "no ComfyUI-Manager can snapshot it") || strings.Contains(r.out, "installing ComfyUI") {
		t.Fatalf("source without a Manager: exit %d", r.code)
	}
	if d := time.Since(start); d > time.Minute {
		t.Errorf("took %s to fail", d)
	}
	absent(t, "/home/comfy/instances/comfy_m")
	if strings.Contains(sh(t, "cat "+cfgPath), "comfy_m") {
		t.Error("comfy_m added to the file")
	}
}
