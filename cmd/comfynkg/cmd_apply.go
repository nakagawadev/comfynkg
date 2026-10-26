package main

import (
	"fmt"
	"os"
	"strings"

	"comfynkg/internal/config"
	"comfynkg/internal/run"
	"comfynkg/internal/systemd"
	"comfynkg/internal/ui"
)

func cmdApply(args []string) error {
	if len(args) > 0 {
		return usagef("apply takes no arguments")
	}
	c, err := loadWarn()
	if err != nil {
		return err
	}
	return apply(c, nil)
}

// apply makes systemd match the file. restart lists instances that must
// restart even if their unit didn't change (flags edited with `set`).
func apply(c *config.Config, restart map[string]bool) error {
	var n struct{ restarted, started, stopped, unchanged, removed int }
	changed := map[string]bool{}
	wrote := false

	for _, in := range c.Instances {
		unit := unitOf(in)
		want, err := systemd.Render(systemd.Unit{Name: in.Name, Owner: c.Owner.Name, WorkDir: c.Dir(in), MemoryMax: c.MemoryMaxFor(in)})
		if err != nil {
			return err
		}
		have, err := systemd.Read(unit)
		if err != nil {
			return err
		}
		if have != "" && !systemd.IsManaged(have) {
			return fmt.Errorf("%s exists and is not managed by comfynkg; refusing to overwrite", systemd.Path(unit))
		}
		if want == have {
			continue
		}
		changed[in.Name] = true
		p := prefix(c, in)
		switch {
		case have == "" && g.dryRun:
			ui.Line(p, "would create unit %s", unit)
		case have == "":
			ui.Line(p, "unit created")
		default:
			verb := "unit updated:"
			if g.dryRun {
				verb = "would update unit:"
			}
			ui.Line(p, "%s", verb)
			for _, d := range systemd.Diff(have, want) {
				ui.Printf("    %s\n", d)
			}
		}
		if !g.dryRun {
			if err := systemd.Write(unit, want); err != nil {
				return err
			}
			wrote = true
		}
	}

	managed, err := systemd.Managed()
	if err != nil {
		return err
	}
	for _, name := range managed {
		if c.Instance(name) != nil {
			continue
		}
		unit := systemd.UnitName(name)
		if g.dryRun {
			ui.Printf("> %s would stop, disable and remove %s (not in the file)\n", c.HostLabel(), unit)
			continue
		}
		_ = systemd.Ctl("disable", "--now", unit)
		if err := systemd.Remove(unit); err != nil {
			return err
		}
		ui.Printf("> %s %s removed (not in the file)\n", c.HostLabel(), unit)
		wrote = true
		n.removed++
	}
	if wrote {
		if err := systemd.Ctl("daemon-reload"); err != nil {
			return err
		}
	}

	for _, in := range c.Instances {
		p := prefix(c, in)
		st, err := systemd.StateOf(unitOf(in))
		if err != nil {
			return err
		}
		running := st.Active == "active" || st.Active == "activating" || st.Active == "reloading"
		switch {
		case in.Enabled && (!st.Enabled || !running):
			verb := "starting"
			if !st.Enabled {
				verb = "enabled, starting"
			}
			if g.dryRun {
				ui.Line(p, "would enable and start")
				continue
			}
			d, err := startAndWait(c, in, "enable", "--now")
			if err != nil {
				ui.Line(p, "%s... %s", verb, failed(err))
				return fmt.Errorf("%s: %w", in.Name, err)
			}
			ui.Line(p, "%s... %s", verb, okDur(d))
			n.started++
		case in.Enabled && (changed[in.Name] || restart[in.Name]):
			if g.dryRun {
				ui.Line(p, "would restart")
				continue
			}
			d, err := startAndWait(c, in, "restart")
			if err != nil {
				ui.Line(p, "restarting... %s", failed(err))
				return fmt.Errorf("%s: %w", in.Name, err)
			}
			ui.Line(p, "restarting... %s", okDur(d))
			n.restarted++
		case !in.Enabled && (st.Enabled || running):
			if g.dryRun {
				ui.Line(p, "would disable and stop")
				continue
			}
			if err := systemd.Ctl("disable", "--now", unitOf(in)); err != nil {
				return err
			}
			ui.Line(p, "disabled, stopped")
			n.stopped++
		default:
			state := "enabled"
			if !in.Enabled {
				state = "disabled"
			}
			if !changed[in.Name] && !g.dryRun {
				ui.Line(p, "unit unchanged, %s", state)
			}
			n.unchanged++
		}
	}

	if err := writePluginConfigs(c); err != nil {
		return err
	}
	if err := applySync(c); err != nil {
		return err
	}
	if g.dryRun {
		ui.Printf("nothing changed (dry run)\n")
		return nil
	}
	parts := []string{fmt.Sprintf("%d restarted", n.restarted), fmt.Sprintf("%d started", n.started)}
	if n.stopped > 0 {
		parts = append(parts, fmt.Sprintf("%d stopped", n.stopped))
	}
	if n.removed > 0 {
		parts = append(parts, fmt.Sprintf("%d removed", n.removed))
	}
	parts = append(parts, fmt.Sprintf("%d unchanged", n.unchanged))
	ui.Printf("done: %s\n", strings.Join(parts, ", "))
	return nil
}

// writePluginConfigs writes comfyui_nakagawa.yaml next to each models file in use.
// Files not written by comfynkg are never overwritten.
func writePluginConfigs(c *config.Config) error {
	if !c.HasPlugin {
		return nil
	}
	for _, typ := range config.Types {
		if c.Models[typ] == "" {
			continue
		}
		want, err := c.PluginFile(typ)
		if err != nil {
			return err
		}
		path := c.PluginPath(typ)
		// The folder belongs to the owner: check with Lstat (never follows links), read and write as the owner.
		var have []byte
		if fi, err := os.Lstat(path); err == nil {
			if !fi.Mode().IsRegular() {
				return fmt.Errorf("%s is not a regular file (a symlink?); refusing to touch it", path)
			}
			if have, err = run.ReadFile(owner(c), path); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		if string(have) == string(want) {
			continue
		}
		if len(have) > 0 && !strings.HasPrefix(string(have), "# managed by comfynkg") {
			return fmt.Errorf("%s exists and is not managed by comfynkg; move it away or delete it", path)
		}
		if g.dryRun {
			ui.Printf("> would write plugin config: %s\n", path)
			continue
		}
		if err := run.WriteFile(owner(c), path, want); err != nil {
			return err
		}
		ui.Printf("> plugin config written: %s\n", path)
	}
	return nil
}

func cmdEnable(args []string) error  { return setAndApply(args, "enabled", "true") }
func cmdDisable(args []string) error { return setAndApply(args, "enabled", "false") }

func setAndApply(args []string, key, value string) error {
	if len(args) != 1 {
		return usagef("usage: comfynkg %s <instance>", map[string]string{"true": "enable", "false": "disable"}[value])
	}
	return edit(args[0], key, value)
}

func cmdSet(args []string) error {
	if len(args) != 3 {
		return usagef("usage: comfynkg set <instance|global> <key> <value>")
	}
	return edit(args[0], args[1], args[2])
}

// edit changes one key, re-validates the whole file, saves it and applies.
// Keys that `run` reads at start (flags, type, port, gpu) restart the affected running instances.
func edit(name, key, value string) error {
	c, err := config.Load(g.config)
	if err != nil {
		return err
	}
	if err := c.Set(name, key, value); err != nil {
		return usagef("%v", err)
	}
	next, err := save(c)
	if err != nil {
		return err
	}
	restart := map[string]bool{}
	if key == "flags" || key == "type" || key == "port" || key == "gpu" {
		for _, in := range next.Instances {
			if name == "global" || in.Name == name {
				restart[in.Name] = true
			}
		}
	}
	verb := "set"
	if g.dryRun {
		verb = "would set"
	}
	ui.Printf("%s %s.%s = %s\n", verb, name, key, value)
	return apply(next, restart)
}
