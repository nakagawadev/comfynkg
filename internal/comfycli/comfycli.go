// Package comfycli calls comfy-cli for one workspace, always as the owner.
package comfycli

import (
	"io"
	"os/exec"
	"path/filepath"
	"strings"

	"comfynkg/internal/run"
)

type Workspace struct {
	Path   string
	Owner  *run.User
	Venv   string // optional: a venv outside the workspace (legacy installs)
	Prefix string // printed before every output line
}

// Cmd builds `comfy --skip-prompt --workspace <path> <args>` as the owner.
func (w Workspace) Cmd(args ...string) *exec.Cmd {
	cmd := run.Command(w.Owner, "comfy", append([]string{"--skip-prompt", "--workspace", w.Path}, args...)...)
	if w.Venv != "" {
		cmd.Env = append(cmd.Env, "VIRTUAL_ENV="+w.Venv)
	}
	return cmd
}

// LegacySnapshotCmd saves a node snapshot with a classic ComfyUI-Manager clone
// (custom_nodes/<managerDir>/cm-cli.py), for installs older than the pip comfyui_manager
// package that comfy-cli needs. The snapshot format is the same.
func (w Workspace) LegacySnapshotCmd(managerDir, output string) *exec.Cmd {
	venv := w.Venv
	if venv == "" {
		venv = filepath.Join(w.Path, ".venv")
	}
	cmd := run.Command(w.Owner, filepath.Join(venv, "bin", "python"),
		filepath.Join(w.Path, "custom_nodes", managerDir, "cm-cli.py"), "save-snapshot", "--output", output)
	cmd.Env = append(cmd.Env, "COMFYUI_PATH="+w.Path, "VIRTUAL_ENV="+venv)
	return cmd
}

// Run streams comfy-cli output, each line prefixed.
func (w Workspace) Run(out io.Writer, args ...string) error {
	return run.Stream(w.Cmd(args...), out, w.Prefix)
}

// refused are passthrough arguments that would break comfynkg's control of the workspace.
var refusedFlags = map[string]string{
	"--workspace": "--workspace is set by comfynkg",
	"--recent":    "--recent is set by comfynkg",
	"--here":      "--here is set by comfynkg",
}

var refusedCommands = map[string]string{
	"set-default": "set-default changes the owner's default workspace",
	"launch":      "launch starts ComfyUI outside systemd; use comfynkg start",
	"stop":        "stop works outside systemd; use comfynkg stop",
	"install":     "use comfynkg create",
}

// Guard returns why passthrough args are refused, or "" when they are fine.
func Guard(args []string) string {
	command := ""
	for i, a := range args {
		flag, _, _ := strings.Cut(a, "=")
		if why, ok := refusedFlags[flag]; ok {
			return why
		}
		if command == "" && !strings.HasPrefix(a, "-") {
			command = a
			if why, ok := refusedCommands[a]; ok {
				return why
			}
			if a == "update" && i+1 < len(args) && args[i+1] == "cli" {
				return "use comfynkg update cli"
			}
		}
	}
	if command == "" {
		return "no comfy-cli command given"
	}
	return ""
}
