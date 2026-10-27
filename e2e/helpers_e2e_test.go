//go:build e2e

// Package e2e walks each phase's story inside the Linux test machine, as root,
// against real systemd and a real ComfyUI. Run with `make e2e`.
package e2e

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"os/exec"
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
	sh(t, `for u in $(ls /etc/systemd/system/comfynkg-*.service 2>/dev/null); do n=$(basename $u); systemctl disable --now $n >/dev/null 2>&1; rm -f $u; done; systemctl daemon-reload; rm -f /etc/comfynkg.yaml`)
}

func vmIP(t *testing.T) string { return strings.TrimSpace(sh(t, "tailscale ip -4")) }
