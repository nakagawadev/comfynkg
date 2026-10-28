//go:build e2e

// Package e2e walks each feature's story inside the Linux test machine, as root, against real
// systemd and a real ComfyUI. Run with `make e2e`; `make e2e-quick` (-short) skips the slow parts
// that only exercise installs and the network (the classic Manager migration, a core update).
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	bin     = "/usr/local/bin/comfynkg"
	cfgPath = "/etc/comfynkg.yaml"
	root    = "/home/comfy/instances"
	base    = "/home/comfy/base"
)

type result struct {
	out  string
	code int
}

// nkg runs comfynkg and returns combined output and exit code.
func nkg(t *testing.T, args ...string) result {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = "/root" // like an admin in a root shell: owner commands must not inherit it
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("comfynkg %v: %v", args, err)
	}
	t.Logf("$ comfynkg %s  (exit %d)\n%s", strings.Join(args, " "), code, buf.String())
	return result{buf.String(), code}
}

func mustOK(t *testing.T, args ...string) string {
	t.Helper()
	r := nkg(t, args...)
	if r.code != 0 {
		t.Fatalf("comfynkg %v: exit %d", args, r.code)
	}
	return r.out
}

func sh(t *testing.T, script string) string {
	t.Helper()
	out, err := exec.Command("bash", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", script, err, out)
	}
	return string(out)
}

func contains(t *testing.T, out string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("output missing %q", w)
		}
	}
}

func writeConfig(t *testing.T, body string) {
	t.Helper()
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newInstance copies the base CPU install into {root}/{name}, owned by comfy.
func newInstance(t *testing.T, name string) {
	t.Helper()
	sh(t, fmt.Sprintf("test -d %[2]s/%[1]s || runuser -u comfy -- cp -a %[3]s %[2]s/%[1]s", name, root, base))
}

func healthy(port int) bool {
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/system_stats", port))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

func waitHealthy(t *testing.T, port int, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !healthy(port) {
		if time.Now().After(deadline) {
			t.Fatalf("port %d not healthy after %s", port, d)
		}
		time.Sleep(time.Second)
	}
}

func active(t *testing.T, unit string) string {
	out, _ := exec.Command("systemctl", "is-active", unit).Output()
	return strings.TrimSpace(string(out))
}

// cleanup stops every comfynkg unit and removes them, so each test starts clean.
func cleanup(t *testing.T) {
	sh(t, `systemctl disable --now comfynkg-releases@check.timer >/dev/null 2>&1; rm -f /etc/systemd/system/comfynkg-releases@check.timer
for u in $(ls /etc/systemd/system/comfynkg-*.service 2>/dev/null); do n=$(basename $u); systemctl disable --now $n >/dev/null 2>&1; rm -f $u; done; systemctl daemon-reload; rm -f /etc/comfynkg.yaml`)
}

func vmIP(t *testing.T) string { return strings.TrimSpace(sh(t, "tailscale ip -4")) }

// pluginSrc is the comfyui_nakagawa checkout next to this repo (the Mac's, mounted by OrbStack at
// the same path), or NKG_PLUGIN_SRC.
var pluginSrc = func() string {
	if p := os.Getenv("NKG_PLUGIN_SRC"); p != "" {
		return p
	}
	p, _ := filepath.Abs("../../comfyui_nakagawa")
	return p
}()

const phase2Config = `host: devbox
root: /home/comfy/instances
flags: --disable-metadata --cpu
models:
  image: /home/comfy/config/image/extra_model_paths.yaml
  video: /home/comfy/config/video/extra_model_paths.yaml
plugin:
  stats:
    enabled: false
    dir: /home/comfy/stats
  instances:
    comfy_a: { stats: { enabled: true, save_graph: "off" } }

comfy_a:
  type: image
  port: 8188
  gpu: 0
  enabled: true

comfy_b:
  type: video
  port: 8189
  gpu: 1
  enabled: true
`

// queueSleep posts a prompt that sleeps for the given seconds. Returns the HTTP status.
func queueSleep(t *testing.T, port int, seconds float64) int {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"prompt": map[string]any{
		"1": map[string]any{"class_type": "NkgSleep", "inputs": map[string]any{"seconds": seconds, "seed": rand.Int()}},
	}})
	resp, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d/prompt", port), "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// background starts comfynkg and returns a func that waits and returns output and exit code.
func background(t *testing.T, args ...string) (*exec.Cmd, func() result) {
	cmd := exec.Command(bin, args...)
	cmd.Dir = "/root" // like an admin in a root shell: owner commands must not inherit it
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd, func() result {
		err := cmd.Wait()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		t.Logf("$ comfynkg %s  (exit %d)\n%s", strings.Join(args, " "), code, buf.String())
		return result{buf.String(), code}
	}
}

func drainState(t *testing.T, port int) map[string]any {
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/nkg/drain", port))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	json.NewDecoder(resp.Body).Decode(&m)
	return m
}

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

func onNode(t *testing.T, cmd string) string {
	return sh(t, "runuser -u comfy -- ssh -o BatchMode=yes comfy@node0 "+fmt.Sprintf("%q", cmd))
}

func absent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err == nil {
		t.Errorf("%s was created", path)
		os.Remove(path)
	}
}

const pluginConfig = `host: devbox
root: /home/comfy/instances
flags: --disable-metadata --cpu
models:
  image: /home/comfy/config/image/extra_model_paths.yaml
  video: /home/comfy/config/video/extra_model_paths.yaml
plugin:
  stats:
    enabled: false
    dir: /home/comfy/stats
  instances:
    comfy_a: { stats: { enabled: true, save_graph: "off" } }

comfy_a:
  type: image
  port: 8188
  gpu: 0
  enabled: true

comfy_b:
  type: video
  port: 8189
  gpu: 1
  enabled: true
`

// pluginInstances starts comfy_a (with the comfyui_nakagawa plugin, stats on) and comfy_b (without),
// both with the sleep node, from pluginConfig.
func pluginInstances(t *testing.T) {
	for _, n := range []string{"comfy_a", "comfy_b"} {
		newInstance(t, n)
		sh(t, fmt.Sprintf("install -o comfy -g comfy -m 644 testdata/sleep_node.py %s/%s/custom_nodes/nkg_sleep.py", root, n))
	}
	sh(t, fmt.Sprintf("ln -sfn %s %s/comfy_a/custom_nodes/comfyui_nakagawa", pluginSrc, root))
	sh(t, "rm -rf /home/comfy/stats /home/comfy/config/*/comfyui_nakagawa.yaml")
	writeConfig(t, pluginConfig)
}
