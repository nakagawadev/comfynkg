//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const createConfig = `host: devbox
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
`

// Create story: create installs ComfyUI with comfy-cli, copies the nodes (git + single-file),
// checks they import and adds a disabled entry; refused names and ports; remove --purge.
func TestCreate(t *testing.T) {
	cleanup(t)
	t.Cleanup(func() { cleanup(t); sh(t, "rm -rf /home/comfy/instances/comfy_c") })
	newInstance(t, "comfy_a")
	sh(t, fmt.Sprintf("install -o comfy -g comfy -m 644 testdata/sleep_node.py %s/comfy_a/custom_nodes/nkg_sleep.py", root))
	sh(t, "rm -rf /home/comfy/instances/comfy_c")
	writeConfig(t, createConfig)
	mustOK(t, "apply")

	// create: installs ComfyUI with comfy-cli, copies nodes (git + single-file), adds a disabled entry
	os.Setenv("COMFYNKG_INSTALL_ARGS", "--cpu")
	out := mustOK(t, "create", "comfy_c", "--type", "image", "--port", "8190", "--gpu", "1", "--nodes-from", "comfy_a")
	contains(t, out, "(comfy_c) installing ComfyUI in /home/comfy/instances/comfy_c...", "(comfy_c) copying custom nodes from /home/comfy/instances/comfy_a...",
		"(comfy_c) copied nkg_sleep.py", "(comfy_c) unit created", "(comfy_c) checking that every custom node imports...", "done: comfy_c created (disabled)")
	contains(t, sh(t, "stat -c %U /home/comfy/instances/comfy_c /home/comfy/instances/comfy_c/.venv /home/comfy/instances/comfy_c/custom_nodes/nkg_sleep.py"), "comfy\ncomfy\ncomfy\n")
	cfg, _ := os.ReadFile(cfgPath)
	contains(t, string(cfg), "comfy_c:\n  type: image\n  port: 8190\n  gpu: 1\n  enabled: false")
	if a := active(t, "comfynkg-comfy_c"); a != "inactive" {
		t.Fatalf("created instance is %s", a)
	}
	if r := nkg(t, "create", "comfy_c", "--type", "image", "--port", "8191", "--gpu", "1"); r.code != 2 {
		t.Fatalf("create over an existing instance: exit %d", r.code)
	}
	if r := nkg(t, "create", "comfy_d", "--type", "image", "--port", "8188", "--gpu", "1"); r.code != 2 || !strings.Contains(r.out, "already used by") {
		t.Fatalf("create on a used port: exit %d", r.code)
	}
	contains(t, mustOK(t, "enable", "comfy_c"), "(comfy_c) enabled, starting... ok")

	// remove --purge
	out = mustOK(t, "remove", "comfy_c", "--purge")
	contains(t, out, "(comfy_c) removed from /etc/comfynkg.yaml and systemd", "(comfy_c) deleted /home/comfy/instances/comfy_c")
	if _, err := os.Stat("/home/comfy/instances/comfy_c"); err == nil {
		t.Fatal("folder still there")
	}
	if cfg, _ := os.ReadFile(cfgPath); strings.Contains(string(cfg), "comfy_c") {
		t.Fatal("entry still in the file")
	}
	if err := exec.Command("systemctl", "cat", "comfynkg-comfy_c").Run(); err == nil {
		t.Fatal("unit still there")
	}
}

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
func TestCreateFromLegacy(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: builds a classic install and fetches the whole registry")
	}
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

	// a model kept in the source's own models/ folder: create warns that the new instance won't see it
	sh(t, "runuser -u comfy -- sh -c 'mkdir -p "+legacyDir+"/models/sams && head -c 2097152 /dev/zero > "+legacyDir+"/models/sams/sam_fake.pth'")
	out := mustOK(t, "create", "comfy_l", "--type", "image", "--port", "8196", "--gpu", "0", "--nodes-from", legacyDir, "--nodes-venv", legacyVenv)
	contains(t, out, "snapshot saved with the classic ComfyUI-Manager (custom_nodes/ComfyUI-Manager)",
		"(comfy_l) copied legacy_file.py", "(comfy_l) checking that every custom node imports...",
		"the source keeps models in its own models/ folder", "    sams (1)", "done: comfy_l created (disabled)")
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

	// a classic Manager whose cm-cli.py breaks: the error says why (the typer/click clash seen in production)
	cli := legacyDir + "/custom_nodes/ComfyUI-Manager/cm-cli.py"
	os.WriteFile(cli, []byte(`raise TypeError("Secondary flag is not valid for non-boolean flag.")`+"\n"), 0o644)
	sh(t, "chown comfy:comfy "+cli)
	r0 := nkg(t, "create", "comfy_m", "--type", "image", "--port", "8197", "--gpu", "0", "--nodes-from", legacyDir, "--nodes-venv", legacyVenv)
	if r0.code != 1 || !strings.Contains(r0.out, "custom_nodes/ComfyUI-Manager/cm-cli.py: TypeError: Secondary flag") || !strings.Contains(r0.out, "click<8.2") {
		t.Errorf("broken classic Manager: exit %d", r0.code)
	}

	// no Manager at all: fails in seconds, nothing installed, nothing added to the file
	sh(t, "rm -rf "+legacyDir+"/custom_nodes/ComfyUI-Manager")
	start := time.Now()
	r := nkg(t, "create", "comfy_m", "--type", "image", "--port", "8197", "--gpu", "0", "--nodes-from", legacyDir, "--nodes-venv", legacyVenv)
	if r.code != 1 || !strings.Contains(r.out, "no classic ComfyUI-Manager in custom_nodes") || strings.Contains(r.out, "installing ComfyUI") {
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
