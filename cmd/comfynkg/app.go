package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"comfynkg/internal/config"
	"comfynkg/internal/instance"
	"comfynkg/internal/run"
	"comfynkg/internal/systemd"
	"comfynkg/internal/ui"
)

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

var (
	lookPath      = exec.LookPath
	healthTimeout = 180 * time.Second
)

// load reads and validates the config. Warnings are returned for the caller to print.
func load() (*config.Config, []string, error) {
	c, err := config.Load(g.config)
	if err != nil {
		return nil, nil, err
	}
	warnings, err := c.Validate()
	return c, warnings, err
}

// loadWarn is load + print warnings first.
func loadWarn() (*config.Config, error) {
	c, warnings, err := load()
	for _, w := range warnings {
		ui.Warn("%s", w)
	}
	return c, err
}

// pick resolves instance arguments; "all" means every enabled instance.
func pick(c *config.Config, names []string) ([]*config.Instance, error) {
	if len(names) == 0 {
		return nil, usagef("name an instance or use all")
	}
	if len(names) == 1 && names[0] == "all" {
		var out []*config.Instance
		for _, in := range c.Instances {
			if in.Enabled {
				out = append(out, in)
			}
		}
		return out, nil
	}
	var out []*config.Instance
	for _, n := range names {
		in := c.Instance(n)
		if in == nil {
			return nil, usagef("unknown instance %q", n)
		}
		out = append(out, in)
	}
	return out, nil
}

func prefix(c *config.Config, in *config.Instance) string {
	return ui.Prefix(c.HostLabel(), in.Port, in.Name)
}

func unitOf(in *config.Instance) string { return systemd.UnitName(in.Name) }

// startAndWait runs a systemctl verb that starts the instance, then waits for health.
func startAndWait(c *config.Config, in *config.Instance, verb ...string) (time.Duration, error) {
	start := time.Now()
	if err := systemd.Ctl(append(verb, unitOf(in))...); err != nil {
		return 0, err
	}
	if err := (instance.Client{Port: in.Port}).WaitHealthy(context.Background(), healthTimeout, crashed(unitOf(in))); err != nil {
		printJournal(in, 30)
		return 0, err
	}
	return time.Since(start), nil
}

// crashed reports an instance whose process exited while we wait for health
// (systemd shows it as failed or waiting to auto-restart).
func crashed(unit string) func() error {
	return func() error {
		p, err := systemd.Props(unit, "ActiveState", "SubState", "ExecMainStatus")
		if err != nil {
			return nil
		}
		if p["ActiveState"] == "failed" || p["SubState"] == "auto-restart" {
			return fmt.Errorf("crashed on start (exit status %s)", p["ExecMainStatus"])
		}
		return nil
	}
}

func owner(c *config.Config) *run.User {
	return &run.User{Name: c.Owner.Name, Home: c.Owner.Home}
}

func printJournal(in *config.Instance, n int) {
	out, _ := run.Output(run.Command(nil, "journalctl", "-u", unitOf(in), "-n", fmt.Sprint(n), "--no-pager", "-o", "cat"))
	for _, l := range strings.Split(strings.TrimSpace(ansi.ReplaceAllString(out, "")), "\n") {
		if l != "" {
			ui.Printf("    %s\n", l)
		}
	}
}

func okDur(d time.Duration) string { return fmt.Sprintf("%s (%s)", ui.Green("ok"), ui.Dur(d)) }

func failed(err error) string { return ui.Red("FAILED: ") + err.Error() }

// toolVersion runs `<tool> --version` and returns a short version string.
func toolVersion(tool string) string {
	out, err := run.Output(run.Command(nil, tool, "--version"))
	if err != nil {
		return "missing"
	}
	out = strings.TrimSpace(out)
	if strings.HasPrefix(out, "{") { // comfy-cli prints a JSON envelope when not on a TTY
		var v struct {
			Version string `json:"version"`
		}
		if json.Unmarshal([]byte(out), &v) == nil && v.Version != "" {
			return v.Version
		}
	}
	f := strings.Fields(out) // "uv 0.9.4 (arch...)"
	if len(f) < 2 {
		return "?"
	}
	return f[1]
}
