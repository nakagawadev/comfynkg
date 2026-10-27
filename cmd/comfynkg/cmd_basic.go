package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"comfynkg/internal/config"
	"comfynkg/internal/instance"
	"comfynkg/internal/run"
	"comfynkg/internal/systemd"
	"comfynkg/internal/ui"
)

// cmdStatus prints the table once, or with -w every 2 s until Ctrl-C (drawn in one go, no flicker).
func cmdStatus(args []string) error {
	var names []string
	watch := false
	for _, a := range args {
		if a == "-w" || a == "--watch" {
			watch = true
		} else {
			names = append(names, a)
		}
	}
	if !watch {
		return statusOnce(names)
	}
	for {
		var buf bytes.Buffer
		ui.Out = &buf
		err := statusOnce(names)
		ui.Out = os.Stdout
		if err != nil {
			return err
		}
		fmt.Printf("\x1b[H\x1b[2J%severy 2s · %s · Ctrl-C to exit\n", buf.String(), time.Now().Format("15:04:05"))
		time.Sleep(2 * time.Second)
	}
}

func statusOnce(args []string) error {
	c, warnings, err := load()
	if err != nil {
		return err
	}
	targets := c.Instances
	if len(args) > 0 {
		if targets, err = pick(c, args); err != nil {
			return err
		}
	}
	ui.Printf("%s · comfynkg %s · comfy-cli %s · uv %s\n\n", c.HostLabel(), version, toolVersion("comfy"), toolVersion("uv"))

	rows := make([][]string, len(targets))
	var wg sync.WaitGroup
	for i, in := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows[i] = statusRow(in)
		}()
	}
	wg.Wait()
	ui.Table(append([][]string{{"NAME", "TYPE", "ENABLED", "ACTIVE", "PORT", "GPU", "COMFYUI", "RSS", "VRAM", "QUEUE", "OK/ERR", "RESTARTS", "UPTIME", "HEALTH"}}, rows...))
	if len(warnings) > 0 {
		ui.Printf("\n")
		for _, w := range warnings {
			ui.Printf("%s %s\n", ui.Yellow("warning:"), w) // on stdout so -w keeps it on screen
		}
	}
	return nil
}

func statusRow(in *config.Instance) []string {
	ctx := context.Background()
	yes := map[bool]string{true: "yes", false: "no"}
	row := []string{in.Name, in.Type, yes[in.Enabled], "no unit", strconv.Itoa(in.Port), strconv.Itoa(*in.GPU), "-", "-", "-", "-", "-", "-", "-", "-"}
	st, err := systemd.StateOf(unitOf(in))
	if err != nil || !st.Exists {
		return row
	}
	row[3] = map[string]string{"active": "running", "inactive": "stopped", "activating": "starting", "deactivating": "stopping"}[st.Active]
	if row[3] == "" {
		row[3] = st.Active
	}
	row[11] = st.Restarts
	if st.Active != "active" {
		if st.Active == "failed" {
			row[13] = ui.Red("down")
		}
		return row
	}
	if st.Uptime > 0 {
		row[12] = shortDuration(st.Uptime)
	}
	cl := instance.Client{Port: in.Port}
	stats, err := cl.Stats(ctx)
	if err != nil {
		row[13] = ui.Red("down")
		return row
	}
	row[6], row[13] = stats.Version, ui.Green("ok")
	if q, err := cl.Queue(ctx); err == nil {
		row[9] = fmt.Sprintf("%d/%d", q.Running, q.Pending)
	}
	if m, ok, _ := cl.Metrics(ctx); ok {
		row[7], row[8], row[10] = ui.Bytes(m.RSS), ui.Bytes(m.VRAM), fmt.Sprintf("%.0f/%.0f", m.OK, m.Err)
	}
	if d, err := cl.DrainStatus(ctx); err == nil && d.Draining {
		row[13] += "  " + ui.Yellow("draining")
	}
	return row
}

// shortDuration prints the two largest units: 2d4h, 3h12m, 45m, 30s.
func shortDuration(d time.Duration) string {
	s := int(d.Seconds())
	switch {
	case s >= 86400:
		return fmt.Sprintf("%dd%dh", s/86400, s%86400/3600)
	case s >= 3600:
		return fmt.Sprintf("%dh%dm", s/3600, s%3600/60)
	case s >= 60:
		return fmt.Sprintf("%dm", s/60)
	}
	return fmt.Sprintf("%ds", s)
}

func cmdStart(args []string) error {
	c, err := loadWarn()
	if err != nil {
		return err
	}
	targets, err := pick(c, args)
	if err != nil {
		return err
	}
	for _, in := range targets {
		p := prefix(c, in)
		if g.dryRun {
			ui.Line(p, "would start")
			continue
		}
		d, err := startAndWait(c, in, "start")
		if err != nil {
			ui.Line(p, "starting... %s", failed(err))
			return fmt.Errorf("%s: %w", in.Name, err)
		}
		ui.Line(p, "starting... %s", okDur(d))
	}
	return nil
}

func cmdStop(args []string) error {
	c, err := loadWarn()
	if err != nil {
		return err
	}
	targets, err := pick(c, args)
	if err != nil {
		return err
	}
	for _, in := range targets {
		p := prefix(c, in)
		if g.dryRun {
			ui.Line(p, "would stop")
			continue
		}
		if err := systemd.Ctl("stop", unitOf(in)); err != nil {
			ui.Line(p, "stopping... %s", failed(err))
			return fmt.Errorf("%s: %w", in.Name, err)
		}
		ui.Line(p, "stopping... %s", ui.Green("ok"))
	}
	return nil
}

func cmdLogs(args []string) error {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	follow := fs.Bool("f", false, "follow")
	lines := fs.Int("n", 200, "lines")
	name, rest := splitName(args)
	if err := fs.Parse(rest); err != nil || name == "" {
		return usagef("usage: comfynkg logs <instance> [-f] [-n 200]")
	}
	c, err := loadWarn()
	if err != nil {
		return err
	}
	in := c.Instance(name)
	if in == nil {
		return usagef("unknown instance %q", name)
	}
	jargs := []string{"-u", unitOf(in), "-n", strconv.Itoa(*lines), "--no-pager"}
	if *follow {
		jargs = append(jargs, "-f")
	}
	return run.Interactive(run.Command(nil, "journalctl", jargs...))
}

// splitName takes the first non-flag argument as the instance name; the rest go to flag parsing.
func splitName(args []string) (string, []string) {
	for i, a := range args {
		if len(a) > 0 && a[0] != '-' && (i == 0 || args[i-1] != "-n") {
			return a, append(append([]string{}, args[:i]...), args[i+1:]...)
		}
	}
	return "", args
}

func cmdVersion(args []string) error {
	ui.Printf("comfynkg %s\ncomfy-cli %s\nuv %s\n", version, toolVersion("comfy"), toolVersion("uv"))
	return nil
}
