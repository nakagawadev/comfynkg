//go:build e2e

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
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

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

// Phase 2 story: drain-aware restarts lose nothing, a timeout turns drain off and skips,
// --force restarts anyway, instances without the plugin just wait, Ctrl-C undrains.
func TestPhase2(t *testing.T) {
	cleanup(t)
	t.Cleanup(func() { cleanup(t) })
	for _, n := range []string{"comfy_a", "comfy_b"} {
		newInstance(t, n)
		sh(t, fmt.Sprintf("install -o comfy -g comfy -m 644 testdata/sleep_node.py %s/%s/custom_nodes/nkg_sleep.py", root, n))
	}
	sh(t, fmt.Sprintf("ln -sfn %s %s/comfy_a/custom_nodes/comfyui_nakagawa", pluginSrc, root))
	sh(t, "rm -rf /home/comfy/stats /home/comfy/config/*/comfyui_nakagawa.yaml")
	writeConfig(t, phase2Config)

	out := mustOK(t, "apply")
	contains(t, out, "> plugin config written: /home/comfy/config/image/comfyui_nakagawa.yaml", "> plugin config written: /home/comfy/config/video/comfyui_nakagawa.yaml")
	pcfg := sh(t, "cat /home/comfy/config/image/comfyui_nakagawa.yaml; stat -c %U /home/comfy/config/image/comfyui_nakagawa.yaml")
	contains(t, pcfg, "# managed by comfynkg", "drain: true", `"8188":`, "enabled: true", "comfy\n")
	if !strings.Contains(mustOK(t, "apply"), "done: 0 restarted, 0 started, 2 unchanged") {
		t.Error("second apply rewrote something")
	}

	// manual drain + status + loopback only
	contains(t, mustOK(t, "drain", "on", "comfy_a"), "(comfy_a) drain on (0 running, 0 pending)")
	if code := queueSleep(t, 8188, 0); code != 503 {
		t.Fatalf("prompt while draining: HTTP %d", code)
	}
	st := mustOK(t, "drain", "status")
	contains(t, st, "comfy_a  yes", "comfy_b  -", "(no plugin)")
	contains(t, mustOK(t, "status"), "draining")
	if code := strings.TrimSpace(sh(t, fmt.Sprintf("curl -s -o /dev/null -w '%%{http_code}' http://%s:8188/nkg/drain", vmIP(t)))); code != "404" {
		t.Fatalf("drain from a non-loopback address: HTTP %s", code)
	}
	if code := strings.TrimSpace(sh(t, "curl -s -o /dev/null -w '%{http_code}' -H 'Host: evil.example' http://127.0.0.1:8188/nkg/drain")); code != "404" {
		t.Fatalf("drain with a non-loopback Host (DNS rebinding): HTTP %s", code)
	}
	contains(t, mustOK(t, "drain", "off", "comfy_a"), "drain off")
	if code := queueSleep(t, 8188, 0); code != 200 {
		t.Fatalf("prompt after drain off: HTTP %d", code)
	}
	if r := nkg(t, "drain", "on", "comfy_b"); r.code != 1 || !strings.Contains(r.out, "no plugin") {
		t.Fatalf("drain on without plugin: exit %d", r.code)
	}

	// restart --wait: three 3 s prompts finish before the restart; new prompts get 503 meanwhile
	time.Sleep(time.Second)
	for i := 0; i < 3; i++ {
		queueSleep(t, 8188, 3)
	}
	_, wait := background(t, "restart", "comfy_a", "--wait")
	time.Sleep(1500 * time.Millisecond)
	if code := queueSleep(t, 8188, 0); code != 503 {
		t.Errorf("prompt during restart --wait: HTTP %d", code)
	}
	r := wait()
	if r.code != 0 {
		t.Fatalf("restart --wait exit %d", r.code)
	}
	contains(t, r.out, "(comfy_a) draining... waiting:", "(comfy_a) draining... queue empty (", "restarting... ok", "done: 1 restarted")
	events := sh(t, "cat /home/comfy/stats/8188/*/events.jsonl")
	if n := strings.Count(events, `"status": "success"`); n < 3 {
		t.Fatalf("only %d successful prompts recorded before the restart:\n%s", n, events)
	}
	if d := drainState(t, 8188); d["draining"] != false {
		t.Fatalf("still draining after restart: %v", d)
	}
	// the new columns: comfy_a has stats, so OK/ERR counts since this start; UPTIME is short
	if st := mustOK(t, "status", "comfy_a"); !regexp.MustCompile(`comfy_a .* 0/0 +\d+/0 +\d+ +\d+s +ok`).MatchString(st) {
		t.Errorf("status columns:\n%s", st)
	}

	// timeout: drain off, skipped, exit 3
	queueSleep(t, 8188, 8)
	queueSleep(t, 8188, 8)
	time.Sleep(500 * time.Millisecond)
	r = nkg(t, "restart", "comfy_a", "comfy_b", "--wait", "--timeout", "3s")
	if r.code != 3 {
		t.Fatalf("timeout exit %d", r.code)
	}
	contains(t, r.out, "(comfy_a) draining... timeout after 3s (1 running, 1 pending), drain off, skipped",
		"(comfy_b) no plugin, waiting for queue without drain... queue empty", "done: 1 restarted, 1 skipped (comfy_a)")
	if d := drainState(t, 8188); d["draining"] != false {
		t.Fatalf("timeout left drain on: %v", d)
	}

	// --force restarts anyway
	r = nkg(t, "restart", "comfy_a", "--wait", "--timeout", "2s", "--force")
	if r.code != 0 {
		t.Fatalf("--force exit %d", r.code)
	}
	contains(t, r.out, "timeout after 2s", "restarting anyway, restarting... ok")

	// Ctrl-C while waiting turns drain off
	queueSleep(t, 8188, 20)
	time.Sleep(500 * time.Millisecond)
	cmd, wait := background(t, "restart", "comfy_a", "--wait")
	time.Sleep(2 * time.Second)
	cmd.Process.Signal(syscall.SIGINT)
	r = wait()
	if r.code == 0 || !strings.Contains(r.out, "interrupted, drain off") {
		t.Fatalf("Ctrl-C: exit %d", r.code)
	}
	if d := drainState(t, 8188); d["draining"] != false {
		t.Fatalf("Ctrl-C left drain on: %v", d)
	}

	// parallel restart of everything
	r = nkg(t, "restart", "all", "--parallel")
	if r.code != 0 {
		t.Fatalf("parallel exit %d", r.code)
	}
	contains(t, r.out, "(comfy_a) restarting... ok", "(comfy_b) restarting... ok", "done: 2 restarted")

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

	// a hand-written plugin file is never overwritten
	os.WriteFile("/home/comfy/config/video/comfyui_nakagawa.yaml", []byte("stats: {enabled: true}\n"), 0o644)
	if r := nkg(t, "apply"); r.code == 0 || !strings.Contains(r.out, "not managed by comfynkg") {
		t.Fatalf("overwrote a hand-written plugin file: exit %d", r.code)
	}
	os.Remove("/home/comfy/config/video/comfyui_nakagawa.yaml")
}
