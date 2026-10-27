// Package systemd renders comfynkg units and wraps systemctl.
package systemd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"comfynkg/internal/run"
)

const Marker = "# managed by comfynkg - edit /etc/comfynkg.yaml, then run: comfynkg apply"

var (
	Dir    = "/etc/systemd/system"
	Binary = "/usr/local/bin/comfynkg"
)

type Unit struct {
	Name      string // instance name
	Owner     string
	WorkDir   string
	MemoryMax string
}

// UnitName is the unit of an instance: comfynkg-<instance>.service.
func UnitName(instance string) string { return "comfynkg-" + instance + ".service" }

// Render returns the unit file for an instance. Values are validated by config; a line break
// here would inject unit directives, so Render refuses it as a second guard.
func Render(u Unit) (string, error) {
	for _, v := range []string{u.Name, u.Owner, u.WorkDir, u.MemoryMax} {
		if strings.ContainsAny(v, "\n\r") {
			return "", fmt.Errorf("unit value %q contains a line break", v)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, `%s
[Unit]
Description=ComfyUI %s (comfynkg)
After=network-online.target tailscaled.service
Wants=network-online.target tailscaled.service

[Service]
User=%s
WorkingDirectory=%s
ExecStart=%s run %s
SyslogIdentifier=%s
Environment=PYTHONUNBUFFERED=1
Restart=on-failure
RestartSec=10
TimeoutStopSec=60
`, Marker, u.Name, u.Owner, u.WorkDir, Binary, u.Name, u.Name)
	if u.MemoryMax != "" {
		fmt.Fprintf(&b, "MemoryMax=%s\n", u.MemoryMax)
	}
	b.WriteString("\n[Install]\nWantedBy=multi-user.target\n")
	return b.String(), nil
}

// Path is where a unit file lives.
func Path(unit string) string { return filepath.Join(Dir, unit) }

// Read returns the unit file content, "" if missing.
func Read(unit string) (string, error) {
	b, err := os.ReadFile(Path(unit))
	if os.IsNotExist(err) {
		return "", nil
	}
	return string(b), err
}

// IsManaged reports whether a unit file was written by comfynkg (it starts with Marker).
func IsManaged(content string) bool { return strings.HasPrefix(content, Marker) }

// Managed lists instance names that have a comfynkg unit on disk.
func Managed() ([]string, error) {
	paths, err := filepath.Glob(filepath.Join(Dir, "comfynkg-*.service"))
	if err != nil {
		return nil, err
	}
	var names []string
	for _, p := range paths {
		base := filepath.Base(p)
		if strings.Contains(base, "@") {
			continue // sync units (comfynkg-sync@<type>)
		}
		b, err := os.ReadFile(p)
		if err == nil && IsManaged(string(b)) {
			names = append(names, strings.TrimSuffix(strings.TrimPrefix(base, "comfynkg-"), ".service"))
		}
	}
	sort.Strings(names)
	return names, nil
}

// Write saves a unit file; callers run `systemctl daemon-reload` afterwards.
func Write(unit, content string) error {
	return os.WriteFile(Path(unit), []byte(content), 0o644)
}

// Remove deletes a unit file.
func Remove(unit string) error { return os.Remove(Path(unit)) }

// Diff lists removed (-) and added (+) lines between two unit files.
func Diff(old, new string) []string {
	oldSet, newSet := lineSet(old), lineSet(new)
	var out []string
	for _, l := range strings.Split(old, "\n") {
		if l != "" && !newSet[l] {
			out = append(out, "-"+l)
		}
	}
	for _, l := range strings.Split(new, "\n") {
		if l != "" && !oldSet[l] {
			out = append(out, "+"+l)
		}
	}
	return out
}

func lineSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, l := range strings.Split(s, "\n") {
		m[l] = true
	}
	return m
}

// Ctl runs systemctl with args.
func Ctl(args ...string) error {
	_, err := run.Output(run.Command(nil, "systemctl", args...))
	return err
}

// Props reads unit properties with `systemctl show`.
func Props(unit string, names ...string) (map[string]string, error) {
	out, err := run.Output(run.Command(nil, "systemctl", "show", unit, "-p", strings.Join(names, ",")))
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			m[k] = v
		}
	}
	return m, nil
}

type State struct {
	Enabled  bool   // UnitFileState == enabled
	Active   string // ActiveState: active, inactive, failed, activating...
	Restarts string
	Exists   bool
	Uptime   time.Duration // since it became active; 0 when not active or unknown
	PID      int           // MainPID: ComfyUI itself, since `comfynkg run` execs it
}

// StateOf reports whether a unit exists, is enabled, is active, how often it restarted and for how long it's up.
func StateOf(unit string) (State, error) {
	p, err := Props(unit, "UnitFileState", "ActiveState", "NRestarts", "LoadState", "ActiveEnterTimestampMonotonic", "MainPID")
	if err != nil {
		return State{}, err
	}
	st := State{
		Enabled:  p["UnitFileState"] == "enabled",
		Active:   p["ActiveState"],
		Restarts: p["NRestarts"],
		Exists:   p["LoadState"] == "loaded",
	}
	if st.Active == "active" {
		st.Uptime = uptime(p["ActiveEnterTimestampMonotonic"])
		st.PID, _ = strconv.Atoi(p["MainPID"])
	}
	return st, nil
}

// uptime turns a monotonic timestamp (µs since boot) into time since then, using /proc/uptime.
// ponytail: /proc/uptime also counts suspended time; servers don't suspend.
func uptime(enteredUS string) time.Duration {
	us, err := strconv.ParseInt(enteredUS, 10, 64)
	b, rerr := os.ReadFile("/proc/uptime")
	if err != nil || rerr != nil || us == 0 {
		return 0
	}
	secs, err := strconv.ParseFloat(strings.Fields(string(b))[0], 64)
	if err != nil {
		return 0
	}
	return max(0, time.Duration(secs*float64(time.Second))-time.Duration(us)*time.Microsecond)
}

// ExecPort reads `--port N` from a legacy unit's ExecStart (used by takeover).
func ExecPort(unit string) (string, error) {
	out, err := run.Output(run.Command(nil, "systemctl", "cat", unit))
	if err != nil {
		return "", err
	}
	port := ""
	for _, l := range strings.Split(out, "\n") {
		if !strings.HasPrefix(l, "ExecStart=") {
			continue
		}
		f := strings.Fields(strings.NewReplacer("'", " ", "\"", " ").Replace(l))
		for i, w := range f {
			if w == "--port" && i+1 < len(f) {
				port = f[i+1]
			} else if strings.HasPrefix(w, "--port=") {
				port = strings.TrimPrefix(w, "--port=")
			}
		}
	}
	return port, nil
}

// SyncUnitName is the unit of one model type's sync loop.
func SyncUnitName(typ string) string { return "comfynkg-sync@" + typ + ".service" }

// RenderSync returns the unit that runs `comfynkg sync loop <type>` as the source owner.
func RenderSync(typ, owner string) (string, error) {
	if strings.ContainsAny(typ+owner, "\n\r") {
		return "", fmt.Errorf("unit value contains a line break")
	}
	return fmt.Sprintf(`%s
[Unit]
Description=comfynkg model sync (%s)
After=network-online.target tailscaled.service
Wants=network-online.target

[Service]
User=%s
ExecStart=%s sync loop %s
SyslogIdentifier=comfynkg-sync-%s
StateDirectory=comfynkg-sync-%s
Restart=always
RestartSec=30

[Install]
WantedBy=multi-user.target
`, Marker, typ, owner, Binary, typ, typ, typ), nil
}
