package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"comfynkg/internal/config"
	"comfynkg/internal/instance"
	"comfynkg/internal/run"
	"comfynkg/internal/systemd"
	"comfynkg/internal/ui"
)

// cmdTakeover replaces a legacy unit with the comfynkg one on the same port.
func cmdTakeover(args []string) error {
	fs := flag.NewFlagSet("takeover", flag.ContinueOnError)
	old := fs.String("old", "", "legacy unit to replace, e.g. comfy_d.service")
	wait := fs.Bool("wait", false, "drain the old instance and wait for its queue")
	timeout := fs.Duration("timeout", 10*time.Minute, "max wait for the old queue")
	names, err := parseMixed(fs, args)
	if err != nil || len(names) != 1 || *old == "" {
		return usagef("usage: comfynkg takeover <instance> --old <unit> [--wait] [--timeout 10m]")
	}
	oldUnit := *old
	if !strings.HasSuffix(oldUnit, ".service") {
		oldUnit += ".service"
	}
	c, err := loadWarn()
	if err != nil {
		return err
	}
	in := c.Instance(names[0])
	if in == nil {
		return usagef("unknown instance %q", names[0])
	}
	p := prefix(c, in)

	// 1. checks
	if _, err := os.Stat(c.Python(in)); err != nil {
		return usagef("%s is not installed (create it first): %v", in.Name, err)
	}
	if strings.HasPrefix(oldUnit, "comfynkg-") {
		return usagef("%s is a comfynkg unit, not a legacy one", oldUnit)
	}
	oldPort, err := systemd.ExecPort(oldUnit)
	if err != nil {
		return usagef("old unit: %v", err)
	}
	if oldPort != strconv.Itoa(in.Port) {
		return usagef("%s listens on port %q, %s on %d: they must match", oldUnit, oldPort, in.Name, in.Port)
	}
	if g.dryRun {
		ui.Line(p, "would drain %s, disable it and start %s", oldUnit, unitOf(in))
		return nil
	}

	// 2. drain and wait on the old instance
	cl := instance.Client{Port: in.Port}
	ctx, stop := interruptible()
	defer stop()
	if *wait {
		how, drained, err := drainForRestart(ctx, cl)
		if err != nil {
			return err
		}
		waited, q, err := waitQueue(ctx, cl, *timeout, func(q instance.Queue) {
			ui.Line(p, "old unit %s: %s... waiting: %d running, %d pending", oldUnit, how, q.Running, q.Pending)
		})
		switch {
		case err == nil:
			ui.Line(p, "old unit %s: %s... queue empty (%s)", oldUnit, how, ui.Dur(waited))
		case errors.Is(err, errDown):
			ui.Line(p, "old unit %s: not responding", oldUnit)
		default:
			undrain(cl, drained)
			ui.Line(p, "old unit %s: %s... %v (%d running, %d pending), drain off, nothing changed", oldUnit, how, err, q.Running, q.Pending)
			return err
		}
	}

	// 3. stop the old unit
	if err := systemd.Ctl("disable", "--now", oldUnit); err != nil {
		return err
	}
	ui.Line(p, "stopping and disabling %s... %s", oldUnit, ui.Green("ok"))

	// 4. enable the new one
	if err := c.Set(in.Name, "enabled", "true"); err != nil {
		return err
	}
	next, err := save(c)
	if err == nil {
		err = apply(next, nil)
	}
	if err == nil {
		ui.Printf("done: %s replaced; remove it when you're ready: rm %s\n", oldUnit, legacyPath(oldUnit))
		return nil
	}

	// 5. roll back to the old unit
	ui.Line(p, "rolling back to %s...", oldUnit)
	_ = systemd.Ctl("disable", "--now", unitOf(in))
	if back, lerr := config.Load(g.config); lerr == nil && back.Set(in.Name, "enabled", "false") == nil {
		_, _ = save(back)
	}
	if rerr := systemd.Ctl("enable", "--now", oldUnit); rerr != nil {
		return fmt.Errorf("takeover failed (%v) and %s did not start again: %w", err, oldUnit, rerr)
	}
	if herr := cl.WaitHealthy(context.Background(), healthTimeout, crashed(oldUnit)); herr != nil {
		return fmt.Errorf("takeover failed (%v) and %s is not healthy: %w", err, oldUnit, herr)
	}
	ui.Line(p, "%s is back... %s", oldUnit, ui.Green("ok"))
	return fmt.Errorf("takeover of %s failed: %w", in.Name, err)
}

// legacyPath is the file of a legacy unit as systemd loaded it, falling back to /etc/systemd/system.
func legacyPath(unit string) string {
	if out, err := run.Output(run.Command(nil, "systemctl", "show", "-p", "FragmentPath", "--value", unit)); err == nil && strings.TrimSpace(out) != "" {
		return strings.TrimSpace(out)
	}
	return filepath.Join(systemd.Dir, unit)
}

// cmdSelfUpdate replaces /usr/local/bin/comfynkg with a checked binary.
func cmdSelfUpdate(args []string) error {
	if len(args) > 1 {
		return usagef("usage: comfynkg self-update [vX.Y.Z | path to new binary]")
	}
	src, cleanup, err := selfUpdateSource(args)
	if err != nil {
		return err
	}
	defer cleanup()
	out, err := run.Output(run.Command(nil, src, "version"))
	if err != nil {
		return fmt.Errorf("%s is not a working comfynkg for this machine: %w", src, err)
	}
	if !strings.HasPrefix(out, "comfynkg ") {
		return fmt.Errorf("%s is not a working comfynkg for this machine: `version` printed %q", src, firstLineOf(out))
	}
	newVer := strings.Fields(out)[1]
	if newVer == version {
		ui.Printf("comfynkg %s is already installed\n", version)
		return nil
	}
	if g.dryRun {
		ui.Printf("would replace %s: %s -> %s\n", systemd.Binary, version, newVer)
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := systemd.Binary + ".new"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, in); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, systemd.Binary); err != nil {
		os.Remove(tmp)
		return err
	}
	ui.Printf("comfynkg %s -> %s (running instances keep going; new starts use it)\n", version, newVer)
	return nil
}
