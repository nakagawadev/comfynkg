package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"comfynkg/internal/config"
	"comfynkg/internal/instance"
	"comfynkg/internal/systemd"
	"comfynkg/internal/ui"
)

// cmdRename gives an instance a new name: the key in the file, its folder and its unit. The port
// doesn't change, so clients and the stats (kept by port) don't notice. A running instance is drained
// and stopped first, like `restart --wait`; any failure after the move puts everything back.
func cmdRename(args []string) error {
	fs := flag.NewFlagSet("rename", flag.ContinueOnError)
	timeout := fs.Duration("timeout", 10*time.Minute, "max wait for the queue")
	force := fs.Bool("force", false, "rename even if the queue never empties")
	names, err := parseMixed(fs, args)
	if err != nil || len(names) != 2 {
		return usagef("usage: comfynkg rename <instance> <new-name> [--timeout 10m] [--force]")
	}
	c, err := loadWarn()
	if err != nil {
		return err
	}
	in := c.Instance(names[0])
	if in == nil {
		return usagef("unknown instance %q", names[0])
	}
	orig, err := os.ReadFile(g.config)
	if err != nil {
		return err
	}
	if err := c.RenameInstance(in.Name, names[1]); err != nil {
		return usagef("%v", err)
	}
	from, to := c.Dir(in), filepath.Join(c.Root, names[1])
	if _, err := os.Lstat(to); err == nil {
		return usagef("%s already exists", to)
	}
	p := prefix(c, in)
	if g.dryRun {
		ui.Line(p, "would drain and stop it, move %s to %s, rename it to %s in %s and apply", from, to, names[1], g.config)
		ui.Printf("nothing changed (dry run)\n")
		return nil
	}

	wasActive := isActive(in)
	if wasActive {
		if err := drainAndWait(p, in, *timeout, *force); err != nil {
			return err
		}
	}
	if err := systemd.Ctl("stop", unitOf(in)); err != nil {
		return err
	}
	ui.Line(p, "stopped")
	startOld := func() {
		if wasActive {
			if err := systemd.Ctl("start", unitOf(in)); err != nil {
				ui.Warn("starting %s again failed: %v", unitOf(in), err)
			}
		}
	}
	moveBack := func() {
		if _, err := asOwner(c, "mv", "-T", "--", to, from); err != nil {
			ui.Warn("moving %s back to %s failed: %v", to, from, err)
		}
	}

	if _, err := asOwner(c, "mv", "-T", "--", from, to); err != nil {
		startOld()
		return fmt.Errorf("move %s: %w; nothing changed", from, err)
	}
	next, err := save(c)
	if err != nil {
		moveBack()
		startOld()
		return fmt.Errorf("%w; nothing changed", err)
	}
	ui.Line(p, "moved to %s, renamed to %s in %s", to, names[1], g.config)
	if err := apply(next, nil); err != nil {
		// put back the file, then the folder; apply then drops the new unit and brings the old one back
		ui.Line(p, "%s, undoing the rename", failed(err))
		if werr := config.WriteFile(g.config, orig); werr != nil {
			return fmt.Errorf("rename failed (%v) and %s couldn't be restored: %w", err, g.config, werr)
		}
		moveBack()
		if old, _, lerr := load(); lerr == nil {
			if aerr := apply(old, nil); aerr != nil {
				ui.Warn("bringing %s back failed: %v", in.Name, aerr)
			}
		}
		return fmt.Errorf("rename failed and was undone: %w", err)
	}
	ui.Printf("done: %s is now %s\n", in.Name, names[1])
	return nil
}

// drainAndWait drains a running instance and waits for its queue, like `restart --wait`. A timeout
// (without force) or Ctrl-C turns drain off again and returns an error: nothing else happened yet.
func drainAndWait(p string, in *config.Instance, timeout time.Duration, force bool) error {
	ctx, stop := interruptible()
	defer stop()
	cl := instance.Client{Port: in.Port}
	how, drained, err := drainForRestart(ctx, cl)
	if err != nil {
		ui.Line(p, "%s", failed(err))
		return err
	}
	waited, q, err := waitQueue(ctx, cl, timeout, func(q instance.Queue) {
		ui.Line(p, "%s... waiting: %d running, %d pending", how, q.Running, q.Pending)
	})
	switch {
	case err == nil:
		ui.Line(p, "%s... queue empty (%s)", how, ui.Dur(waited))
	case errors.Is(err, errTimeout) && force:
		ui.Line(p, "%s... timeout after %s (%d running, %d pending), going on anyway", how, ui.Dur(timeout), q.Running, q.Pending)
	case errors.Is(err, errTimeout):
		undrain(cl, drained)
		ui.Line(p, "%s... %s after %s (%d running, %d pending), drain off", how, ui.Yellow("timeout"), ui.Dur(timeout), q.Running, q.Pending)
		return errors.New("the queue never emptied; nothing changed (--force to go on anyway)")
	case errors.Is(err, errDown):
		ui.Line(p, "%s... not responding", how)
	default:
		undrain(cl, drained)
		ui.Line(p, "%s... interrupted, drain off", how)
		return errors.New("interrupted; nothing changed")
	}
	return nil
}
