package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"comfynkg/internal/config"
	"comfynkg/internal/instance"
	"comfynkg/internal/run"
	"comfynkg/internal/systemd"
	"comfynkg/internal/ui"
)

type statusOpts struct {
	names     []string
	local     bool // only this host, even with `hosts:`
	ifPresent bool // skip names this host doesn't have; print nothing when none match (used over ssh)
}

// cmdStatus prints the table once, or with -w every 2 s until Ctrl-C (drawn in one go, no flicker).
// With `hosts:` in the config it also shows each of those hosts, asked over ssh.
func cmdStatus(args []string) error {
	var o statusOpts
	watch := false
	for _, a := range args {
		switch a {
		case "-w", "--watch":
			watch = true
		case "--local":
			o.local = true
		case "--if-present":
			o.ifPresent = true
		default:
			o.names = append(o.names, a)
		}
	}
	if !watch {
		return statusOnce(o)
	}
	for {
		var buf bytes.Buffer
		ui.Out = &buf
		err := statusOnce(o)
		ui.Out = os.Stdout
		if err != nil {
			return err
		}
		fmt.Printf("\x1b[H\x1b[2J%severy 2s · %s · Ctrl-C to exit\n", buf.String(), time.Now().Format("15:04:05"))
		time.Sleep(2 * time.Second)
	}
}

func statusOnce(o statusOpts) error {
	c, warnings, err := load()
	if err != nil {
		return err
	}
	hosts := c.Hosts
	if o.local {
		hosts = nil
	}
	// other hosts first, in the background: ssh is the slow part
	remote := make([]hostStatus, len(hosts))
	var wg sync.WaitGroup
	for i, h := range hosts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			remote[i] = askHost(c, h, o.names)
		}()
	}

	lenient := o.ifPresent || len(hosts) > 0 // a name may live on another host
	all := len(o.names) == 1 && o.names[0] == "all"
	targets := c.Instances
	if len(o.names) > 0 {
		if lenient && !all {
			targets = nil
			for _, n := range o.names {
				if in := c.Instance(n); in != nil {
					targets = append(targets, in)
				}
			}
		} else if targets, err = pick(c, o.names); err != nil {
			return err
		}
	}
	found := len(targets) > 0
	printed := found || len(o.names) == 0 || (all && !o.ifPresent)
	if printed {
		printStatus(c, targets, warnings)
	}

	wg.Wait()
	unreachable := false
	sep := func() {
		if printed {
			ui.Printf("\n")
		}
		printed = true
	}
	for i, r := range remote {
		switch {
		case r.err != nil:
			unreachable = true
			sep()
			ui.Printf("%s%s%s %s\n", ui.Yellow(hosts[i]), ui.Dim(" · "), ui.Red("unreachable"), ui.Dim(r.err.Error()))
		case r.out != "":
			found = true
			sep()
			ui.Printf("%s", r.out)
		}
	}
	if len(o.names) > 0 && !all && !found && !unreachable && !o.ifPresent {
		return usagef("unknown instance %s", strings.Join(o.names, ", "))
	}
	return nil
}

func printStatus(c *config.Config, targets []*config.Instance, warnings []string) {
	dot := ui.Dim(" · ")
	ui.Printf("%s%s%s%s\n\n", ui.Yellow(c.HostLabel()), dot, ui.Cyan("comfynkg "+version),
		ui.Dim(fmt.Sprintf(" · comfy-cli %s · uv %s", toolVersion("comfy"), toolVersion("uv"))))

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
	header := []string{"NAME", "TYPE", "ENABLED", "ACTIVE", "PORT", "GPU", "COMFYUI", "RSS", "VRAM", "QUEUE", "OK/ERR", "RESTARTS", "UPTIME", "HEALTH"}
	for i := range header {
		header[i] = ui.Dim(header[i])
	}
	for _, r := range rows {
		paintRow(r)
	}
	ui.Table(append([][]string{header}, rows...))
	if len(warnings) > 0 {
		ui.Printf("\n")
		for _, w := range warnings {
			ui.Printf("%s %s\n", ui.Yellow("warning:"), w) // on stdout so -w keeps it on screen
		}
	}
}

type hostStatus struct {
	out string
	err error
}

// hostTimeout bounds one host's answer; ConnectTimeout covers a host that's down.
var hostTimeout = 20 * time.Second

// askHost runs `comfynkg status --local` on another host over ssh, as the owner (the same key
// and ~/.ssh/config as model sync). A plain host means <owner>@host.
func askHost(c *config.Config, host string, names []string) hostStatus {
	dest := host
	if !strings.Contains(host, "@") {
		dest = c.Owner.Name + "@" + host
	}
	remote := "/usr/local/bin/comfynkg status --local"
	if len(names) > 0 {
		remote += " --if-present " + shellQuote(names)
	}
	if ui.Colored() {
		remote = "CLICOLOR_FORCE=1 " + remote
	}
	var u *run.User
	if os.Geteuid() == 0 {
		u = owner(c)
	}
	cmd := run.Command(u, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "--", dest, remote)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Start(); err != nil {
		return hostStatus{err: err}
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var err error
	select {
	case err = <-done:
	case <-time.After(hostTimeout):
		cmd.Process.Kill()
		<-done
		err = fmt.Errorf("no answer after %s", hostTimeout)
	}
	if err != nil {
		if msg := lastLine(buf.String()); msg != "" {
			err = errors.New(msg)
		}
		return hostStatus{err: err}
	}
	return hostStatus{out: buf.String()}
}

// shellQuote quotes words for the remote shell ssh runs them in.
func shellQuote(words []string) string {
	q := make([]string, len(words))
	for i, w := range words {
		q[i] = "'" + strings.ReplaceAll(w, "'", `'\''`) + "'"
	}
	return strings.Join(q, " ")
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
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

// paintRow colors a status row: the name cyan, yes/running green, empty values dim (health is colored when set).
func paintRow(r []string) {
	r[0] = ui.Cyan(r[0])
	for i, v := range r[1:] {
		switch v {
		case "yes", "running":
			r[i+1] = ui.Green(v)
		case "-":
			r[i+1] = ui.Dim(v)
		}
	}
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
