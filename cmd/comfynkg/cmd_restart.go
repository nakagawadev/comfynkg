package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"comfynkg/internal/config"
	"comfynkg/internal/instance"
	"comfynkg/internal/ui"
)

// pollEvery is how often /queue is checked while waiting.
var pollEvery = 2 * time.Second

type restartOpts struct {
	wait, force, parallel bool
	timeout               time.Duration
}

func cmdRestart(args []string) error {
	fs := flag.NewFlagSet("restart", flag.ContinueOnError)
	var o restartOpts
	fs.BoolVar(&o.wait, "wait", false, "drain and wait for the queue to empty")
	fs.BoolVar(&o.force, "force", false, "restart even if the queue never empties")
	fs.BoolVar(&o.parallel, "parallel", false, "restart every instance at once")
	fs.DurationVar(&o.timeout, "timeout", 10*time.Minute, "max wait for the queue")
	names, err := parseMixed(fs, args)
	if err != nil {
		return usagef("usage: comfynkg restart <instance...|all> [--wait] [--timeout 10m] [--force] [--parallel]")
	}
	if o.parallel && o.wait {
		return usagef("--parallel and --wait don't mix")
	}
	c, err := loadWarn()
	if err != nil {
		return err
	}
	targets, err := pick(c, names)
	if err != nil {
		return err
	}
	ctx, stop := interruptible()
	defer stop()
	if o.parallel {
		return restartParallel(c, targets)
	}

	var restarted int
	var skipped, failedNames []string
	for _, in := range targets {
		err := restartOne(ctx, c, in, o)
		switch {
		case errors.Is(err, errSkipped):
			skipped = append(skipped, in.Name)
		case err != nil:
			failedNames = append(failedNames, in.Name)
			if ctx.Err() != nil {
				return err
			}
		default:
			restarted++
		}
	}
	return summary(restarted, skipped, failedNames)
}

var errSkipped = errors.New("skipped")

// summary prints the "done:" line and picks the result: nil, an error when nothing restarted,
// or a partialError (exit 3) when some did.
func summary(restarted int, skipped, failedNames []string) error {
	msg := fmt.Sprintf("%d restarted", restarted)
	if len(skipped) > 0 {
		msg += fmt.Sprintf(", %d skipped (%s)", len(skipped), strings.Join(skipped, ", "))
	}
	if len(failedNames) > 0 {
		msg += fmt.Sprintf(", %d failed (%s)", len(failedNames), strings.Join(failedNames, ", "))
	}
	ui.Printf("done: %s\n", msg)
	switch {
	case len(skipped)+len(failedNames) == 0:
		return nil
	case restarted == 0 && len(skipped) == 0:
		return errors.New("every restart failed")
	}
	return partialError{"some instances were not restarted"}
}

// restartOne drains, waits for the queue and restarts one instance. The line is printed as it progresses.
func restartOne(ctx context.Context, c *config.Config, in *config.Instance, o restartOpts) error {
	p := prefix(c, in)
	if g.dryRun {
		ui.Line(p, "would restart")
		return nil
	}
	if !o.wait {
		d, err := startAndWait(c, in, "restart")
		if err != nil {
			ui.Line(p, "restarting... %s", failed(err))
			return err
		}
		ui.Line(p, "restarting... %s", okDur(d))
		return nil
	}

	cl := instance.Client{Port: in.Port}
	how, drained, err := drainForRestart(ctx, cl)
	if err != nil {
		ui.Line(p, "%s", failed(err))
		return err
	}
	waited, q, err := waitQueue(ctx, cl, o.timeout, func(q instance.Queue) {
		ui.Line(p, "%s... waiting: %d running, %d pending", how, q.Running, q.Pending)
	})
	switch {
	case err == nil:
		how = fmt.Sprintf("%s... queue empty (%s)", how, ui.Dur(waited))
	case errors.Is(err, errTimeout) && o.force:
		how = fmt.Sprintf("%s... timeout after %s (%d running, %d pending), restarting anyway", how, ui.Dur(o.timeout), q.Running, q.Pending)
	case errors.Is(err, errTimeout):
		undrain(cl, drained)
		ui.Line(p, "%s... %s after %s (%d running, %d pending), drain off, skipped", how, ui.Yellow("timeout"), ui.Dur(o.timeout), q.Running, q.Pending)
		return errSkipped
	case errors.Is(err, errDown):
		how += "... not responding"
	default: // interrupted
		undrain(cl, drained)
		ui.Line(p, "%s... interrupted, drain off", how)
		return errors.New("interrupted")
	}
	d, err := startAndWait(c, in, "restart")
	if err != nil {
		ui.Line(p, "%s, restarting... %s", how, failed(err))
		return err
	}
	ui.Line(p, "%s, restarting... %s", how, okDur(d))
	return nil
}

// drainForRestart turns drain on. Without the plugin it only waits for the queue.
func drainForRestart(ctx context.Context, cl instance.Client) (how string, drained bool, err error) {
	_, err = cl.DrainOn(ctx)
	switch {
	case err == nil:
		return "draining", true, nil
	case errors.Is(err, instance.ErrNoPlugin):
		return "no plugin, waiting for queue without drain", false, nil
	}
	if _, statsErr := cl.Stats(ctx); statsErr != nil {
		return "not responding", false, nil // nothing to wait for
	}
	return "", false, fmt.Errorf("drain on: %w", err)
}

// undrain turns drain off again if this run turned it on, so a failed or skipped restart never
// leaves an instance refusing prompts.
func undrain(cl instance.Client, drained bool) {
	if drained {
		if _, err := cl.DrainOff(context.Background()); err != nil {
			ui.Warn("drain off failed: %v (run: comfynkg drain off ...)", err)
		}
	}
}

var (
	errTimeout = errors.New("timeout")
	errDown    = errors.New("not responding")
)

// waitQueue polls /queue until it's empty. It reports progress at start and every minute.
func waitQueue(ctx context.Context, cl instance.Client, timeout time.Duration, progress func(instance.Queue)) (time.Duration, instance.Queue, error) {
	start := time.Now()
	var last time.Time
	for {
		q, err := cl.Queue(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return time.Since(start), q, ctx.Err()
			}
			return time.Since(start), q, errDown
		}
		if q.Empty() {
			return time.Since(start), q, nil
		}
		if time.Since(start) >= timeout {
			return time.Since(start), q, errTimeout
		}
		if time.Since(last) >= time.Minute {
			progress(q)
			last = time.Now()
		}
		select {
		case <-ctx.Done():
			return time.Since(start), q, ctx.Err()
		case <-time.After(pollEvery):
		}
	}
}

// restartParallel restarts every target at once, without draining, and waits for all health checks.
func restartParallel(c *config.Config, targets []*config.Instance) error {
	type res struct {
		d   time.Duration
		err error
	}
	results := make([]res, len(targets))
	var wg sync.WaitGroup
	for i, in := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if g.dryRun {
				return
			}
			d, err := startAndWait(c, in, "restart")
			results[i] = res{d, err}
		}()
	}
	wg.Wait()
	var restarted int
	var failedNames []string
	for i, in := range targets {
		p := prefix(c, in)
		switch {
		case g.dryRun:
			ui.Line(p, "would restart")
		case results[i].err != nil:
			ui.Line(p, "restarting... %s", failed(results[i].err))
			failedNames = append(failedNames, in.Name)
		default:
			ui.Line(p, "restarting... %s", okDur(results[i].d))
			restarted++
		}
	}
	return summary(restarted, nil, failedNames)
}

func cmdDrain(args []string) error {
	if len(args) == 0 {
		return usagef("usage: comfynkg drain on|off <instance...|all> | drain status [instance...]")
	}
	c, err := loadWarn()
	if err != nil {
		return err
	}
	verb, names := args[0], args[1:]
	if verb == "status" {
		targets := c.Instances
		if len(names) > 0 {
			if targets, err = pick(c, names); err != nil {
				return err
			}
		}
		return drainStatus(targets)
	}
	if verb != "on" && verb != "off" {
		return usagef("drain: on, off or status")
	}
	if os.Geteuid() != 0 {
		return usagef("drain %s must run as root", verb)
	}
	targets, err := pick(c, names)
	if err != nil {
		return err
	}
	var bad int
	for _, in := range targets {
		p := prefix(c, in)
		if g.dryRun {
			ui.Line(p, "would turn drain %s", verb)
			continue
		}
		cl := instance.Client{Port: in.Port}
		var d instance.Drain
		if verb == "on" {
			d, err = cl.DrainOn(context.Background())
		} else {
			d, err = cl.DrainOff(context.Background())
		}
		switch {
		case errors.Is(err, instance.ErrNoPlugin):
			ui.Line(p, "%s", failed(errors.New("no plugin, or drain disabled in comfyui_nakagawa.yaml")))
			bad++
		case err != nil:
			ui.Line(p, "%s", failed(err))
			bad++
		default:
			ui.Line(p, "drain %s (%d running, %d pending)", verb, d.Running, d.Pending)
		}
	}
	if bad > 0 && bad < len(targets) {
		return partialError{"drain failed on some instances"}
	} else if bad > 0 {
		return errors.New("drain failed")
	}
	return nil
}

func drainStatus(targets []*config.Instance) error {
	rows := [][]string{{"NAME", "DRAINING", "SINCE", "RUNNING", "PENDING", ""}}
	for _, in := range targets {
		cl := instance.Client{Port: in.Port}
		d, err := cl.DrainStatus(context.Background())
		switch {
		case errors.Is(err, instance.ErrNoPlugin):
			q, qerr := cl.Queue(context.Background())
			if qerr != nil {
				rows = append(rows, []string{in.Name, "-", "-", "-", "-", "(down)"})
				continue
			}
			rows = append(rows, []string{in.Name, "-", "-", fmt.Sprint(q.Running), fmt.Sprint(q.Pending), "(no plugin)"})
		case err != nil:
			rows = append(rows, []string{in.Name, "-", "-", "-", "-", "(down)"})
		default:
			since, draining := "-", "no"
			if d.Draining {
				draining = ui.Yellow("yes")
				if d.Since != nil {
					since = time.Unix(int64(*d.Since), 0).Format("15:04:05")
				}
			}
			rows = append(rows, []string{in.Name, draining, since, fmt.Sprint(d.Running), fmt.Sprint(d.Pending), ""})
		}
	}
	ui.Table(rows)
	return nil
}

// parseMixed lets flags and instance names come in any order.
func parseMixed(fs *flag.FlagSet, args []string) ([]string, error) {
	var names []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return names, nil
		}
		names = append(names, args[0])
		args = args[1:]
	}
}
