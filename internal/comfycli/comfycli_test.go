package comfycli

import "testing"

func TestGuard(t *testing.T) {
	ok := [][]string{{"node", "show", "installed"}, {"node", "install", "x"}, {"update", "comfy"}, {"--json", "node", "list"}}
	for _, a := range ok {
		if why := Guard(a); why != "" {
			t.Errorf("Guard(%q) refused: %s", a, why)
		}
	}
	bad := map[string][]string{
		"--workspace is set by comfynkg": {"--workspace", "/tmp", "node", "list"},
		"--recent is set by comfynkg":    {"node", "list", "--recent"},
		"--here is set by comfynkg":      {"--here=true", "node"},
		"set-default changes":            {"set-default", "/x"},
		"launch starts ComfyUI":          {"launch", "--background"},
		"use comfynkg create":            {"install", "--cpu"},
		"use comfynkg update cli":        {"update", "cli"},
		"stop works outside systemd":     {"stop"},
		"no comfy-cli command":           {"--json"},
	}
	for want, a := range bad {
		if why := Guard(a); why == "" || len(why) < len(want) || why[:len(want)] != want {
			t.Errorf("Guard(%q) = %q, want %q...", a, why, want)
		}
	}
}
