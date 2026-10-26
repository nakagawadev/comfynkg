package systemd

import (
	"reflect"
	"strings"
	"testing"
)

func TestRender(t *testing.T) {
	u, _ := Render(Unit{Name: "comfy_4", Owner: "comfy", WorkDir: "/home/comfy/instances/comfy_4"})
	for _, want := range []string{Marker + "\n", "User=comfy\n", "ExecStart=/usr/local/bin/comfynkg run comfy_4\n", "WorkingDirectory=/home/comfy/instances/comfy_4\n", "After=network-online.target tailscaled.service\n"} {
		if !strings.Contains(u, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(u, "MemoryMax") {
		t.Error("MemoryMax without memory_max")
	}
	if !IsManaged(u) {
		t.Error("not managed")
	}
	withMem, _ := Render(Unit{Name: "comfy_4", Owner: "comfy", WorkDir: "/x", MemoryMax: "40G"})
	if !strings.Contains(withMem, "MemoryMax=40G\n") {
		t.Error("MemoryMax missing")
	}
	if _, err := Render(Unit{Name: "comfy_4", Owner: "comfy", WorkDir: "/x\nExecStartPre=+/tmp/pwn"}); err == nil {
		t.Error("a line break in a unit value must be refused")
	}
	if got := Diff(u, withMem); !reflect.DeepEqual(got, []string{"-WorkingDirectory=/home/comfy/instances/comfy_4", "+WorkingDirectory=/x", "+MemoryMax=40G"}) {
		t.Errorf("Diff = %q", got)
	}
}
