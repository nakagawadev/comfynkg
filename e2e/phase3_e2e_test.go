//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const phase3Config = `host: devbox
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

// gitRepo makes a local git repo (owned by comfy) with the given files, on branch main.
func gitRepo(t *testing.T, name string, files map[string]string) string {
	dir := "/home/comfy/testrepos/" + name
	sh(t, "rm -rf "+dir+" && runuser -u comfy -- mkdir -p "+dir)
	for f, body := range files {
		os.WriteFile(filepath.Join(dir, f), []byte(body), 0o644)
	}
	sh(t, fmt.Sprintf(`chown -R comfy:comfy %[1]s && runuser -u comfy -- bash -c 'cd %[1]s && git init -q -b main && git add -A && git -c user.email=t@t -c user.name=t commit -qm init'`, dir))
	return "file://" + dir
}

func comfyVersion(t *testing.T, port int) string {
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/system_stats", port))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var v struct {
		System struct {
			Version string `json:"comfyui_version"`
		} `json:"system"`
	}
	json.NewDecoder(resp.Body).Decode(&v)
	return v.System.Version
}

// Phase 3 story: create an instance copying nodes, install a node from a git ref,
// a canary install that crashes ComfyUI is rolled back, snapshots, passthrough guard,
// core and node updates, remove --purge.
func TestPhase3(t *testing.T) {
	cleanup(t)
	t.Cleanup(func() { cleanup(t); sh(t, "rm -rf /home/comfy/instances/comfy_c /home/comfy/instances/.snapshots") })
	newInstance(t, "comfy_a")
	sh(t, fmt.Sprintf("install -o comfy -g comfy -m 644 testdata/sleep_node.py %s/comfy_a/custom_nodes/nkg_sleep.py", root))
	sh(t, "rm -rf /home/comfy/instances/comfy_c /home/comfy/instances/.snapshots")
	writeConfig(t, phase3Config)
	mustOK(t, "apply")

	// create: installs ComfyUI with comfy-cli, copies nodes (git + single-file), adds a disabled entry
	os.Setenv("COMFYNKG_INSTALL_ARGS", "--cpu")
	out := mustOK(t, "create", "comfy_c", "--type", "image", "--port", "8190", "--gpu", "1", "--nodes-from", "comfy_a")
	contains(t, out, "(comfy_c) installing ComfyUI in /home/comfy/instances/comfy_c...", "(comfy_c) copying custom nodes from /home/comfy/instances/comfy_a...",
		"(comfy_c) copied nkg_sleep.py", "(comfy_c) unit created", "done: comfy_c created (disabled)")
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
	r := nkg(t, "node", "install", "all", killer, "--ref", "main")
	if r.code != 1 {
		t.Fatalf("killer canary: exit %d", r.code)
	}
	contains(t, r.out, "(comfy_a) canary: installing "+killer+"@main...", "crashed on start", "(comfy_a) rolling back to", "(comfy_a) restarting... ok", "stopped: comfy_a failed and was rolled back; 0 updated")
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
	out = mustOK(t, "update", "core", "comfy_c", "--version", "latest")
	contains(t, out, "(comfy_c) updating ComfyUI to latest...", "restarting... ok", "done: 1 updated")
	if v := comfyVersion(t, 8190); v == "" || strings.Contains(v, "-") {
		t.Errorf("version after update core latest: %q", v)
	}

	// dry runs change nothing
	contains(t, mustOK(t, "update", "nodes", "all", "--dry-run"), "(comfy_a) would updating nodes", "nothing changed (dry run)")

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
