package main

import (
	"fmt"
	"os"
	"time"

	"comfynkg/internal/config"
	"comfynkg/internal/run"
	nsync "comfynkg/internal/sync"
	"comfynkg/internal/systemd"
	"comfynkg/internal/ui"
)

// syncInterval is the pause between rounds of `sync loop`.
var syncInterval = 60 * time.Second

func cmdSync(args []string) error {
	if len(args) == 0 {
		return usagef("usage: comfynkg sync status [image|video] [-w] | sync now|dry-run [image|video] | sync loop <type>")
	}
	verb, rest := args[0], args[1:]
	watch := false
	if verb == "status" {
		var kept []string
		for _, a := range rest {
			if a == "-w" || a == "--watch" {
				watch = true
			} else {
				kept = append(kept, a)
			}
		}
		rest = kept
	}
	if verb != "loop" && verb != "status" && os.Geteuid() != 0 {
		return usagef("sync %s must run as root", verb)
	}
	c, err := config.Load(g.config)
	if err != nil {
		return err
	}
	if _, err := c.Validate(); err != nil {
		return err
	}
	if len(c.Sync) == 0 {
		return usagef("no sync section in %s: this server doesn't sync", g.config)
	}
	types, err := syncTypes(c, rest, verb == "loop")
	if err != nil {
		return err
	}
	if verb != "status" {
		if err := checkDests(c); err != nil {
			return err
		}
	}
	switch verb {
	case "status":
		if watch {
			return watchEvery(2*time.Second, func() error { return syncStatus(types) })
		}
		return syncStatus(types)
	case "now":
		for _, t := range types {
			s := c.Sync[t]
			o, uid, gid, err := sourceOwner(c, s.Source)
			if err != nil {
				return err
			}
			st := nsync.Round(t, s.Source, s.To, o, uid, gid, func(l string) { ui.Printf("%s\n", l) })
			for _, d := range st.Dests {
				if d.State == "error" {
					return fmt.Errorf("sync %s -> %s: %s", t, d.Dest, d.Error)
				}
			}
		}
		return nil
	case "dry-run":
		return syncDryRun(c, types)
	case "loop":
		t := types[0]
		for {
			// Re-read the file each round so edits apply without a restart.
			if next, err := config.Load(g.config); err == nil {
				if _, err := next.Validate(); err == nil && next.Sync[t].Source != "" {
					c = next
				}
			}
			s := c.Sync[t]
			nsync.Round(t, s.Source, s.To, nil, 0, 0, func(l string) { fmt.Println(l) })
			time.Sleep(syncInterval)
		}
	}
	return usagef("sync: status, now, dry-run or loop")
}

func syncTypes(c *config.Config, args []string, exactlyOne bool) ([]string, error) {
	if len(args) > 1 || exactlyOne && len(args) != 1 {
		return nil, usagef("name one type: image or video")
	}
	if len(args) == 1 {
		if _, ok := c.Sync[args[0]]; !ok {
			return nil, usagef("no sync.%s in %s", args[0], g.config)
		}
		return args, nil
	}
	var out []string
	for _, t := range config.Types {
		if _, ok := c.Sync[t]; ok {
			out = append(out, t)
		}
	}
	return out, nil
}

// checkDests refuses destinations on this machine. Done here, not in Validate,
// because it resolves hostnames.
func checkDests(c *config.Config) error {
	for t, s := range c.Sync {
		for _, d := range s.To {
			local, err := nsync.IsLocal(d)
			if err != nil {
				return usagef("sync.%s.to: %v", t, err)
			}
			if local {
				return usagef("sync.%s.to: %s is this machine", t, d)
			}
		}
	}
	return nil
}

// sourceOwner is who runs rsync: the owner of the source folder (falls back to the instance owner).
func sourceOwner(c *config.Config, source string) (*run.User, int, int, error) {
	o := c.Owner
	if fi, err := os.Stat(source); err == nil {
		if so, err := config.OwnerOf(fi); err == nil {
			o = so
		}
	}
	if o.UID == 0 {
		return nil, 0, 0, fmt.Errorf("%s is owned by root; sync must run as an unprivileged user", source)
	}
	return &run.User{Name: o.Name, Home: o.Home}, o.UID, o.GID, nil
}

func syncStatus(types []string) error {
	rows := [][]string{{"TYPE", "DEST", "STATE", "LAST SYNC", "TOOK", "FILES", "BYTES", "ERROR"}}
	for _, t := range types {
		st, err := nsync.LoadState(t)
		if err != nil {
			rows = append(rows, []string{t, "-", "never run", "-", "-", "-", "-", "-"})
			continue
		}
		for _, d := range st.Dests {
			state := d.State
			switch state {
			case "error":
				state = ui.Red(state)
			case "syncing", "waiting":
				state = ui.Yellow(state)
			}
			last, took, files, size := "-", "-", "-", "-"
			if !d.LastSync.IsZero() {
				last, took = clock(d.LastSync), d.Took
				files, size = fmt.Sprint(d.Files), ui.Bytes(float64(d.Bytes))
			}
			if d.State == "syncing" {
				files = fmt.Sprintf("%d planned", d.Files)
				took = time.Since(d.LastStart).Round(time.Second).String() // this round so far
				size = fmt.Sprintf("%s / %s", ui.Bytes(float64(d.Copied)), ui.Bytes(float64(d.Planned)))
				if d.Speed != "" {
					size += " · " + d.Speed
				}
			}
			errMsg := d.Error
			if errMsg == "" {
				errMsg = "-"
			}
			rows = append(rows, []string{t, d.Dest, state, last, took, files, size, errMsg})
		}
	}
	ui.Table(rows)
	return nil
}

func clock(t time.Time) string {
	if time.Since(t) < 24*time.Hour {
		return t.Format("15:04:05")
	}
	return t.Format("2006-01-02 15:04")
}

func syncDryRun(c *config.Config, types []string) error {
	for _, t := range types {
		s := c.Sync[t]
		o, _, _, err := sourceOwner(c, s.Source)
		if err != nil {
			return err
		}
		for _, dest := range s.To {
			ui.Printf("> %s -> %s\n", t, dest)
			if err := nsync.Guard(s.Source); err != nil {
				ui.Printf("    %s: %v (nothing would run)\n", ui.Red("error"), err)
				continue
			}
			changes, err := nsync.Plan(o, s.Source, dest)
			if err != nil {
				ui.Printf("    %s: %v\n", ui.Red("error"), firstLineOf(err.Error()))
				continue
			}
			if len(changes) == 0 {
				ui.Printf("    up to date\n")
			}
			for _, ch := range changes {
				if ch.Delete {
					ui.Printf("    %s  %s\n", ui.Red("DELETE"), ch.Path)
				} else {
					ui.Printf("    copy    %s (%s)\n", ch.Path, ui.Bytes(float64(ch.Size)))
				}
			}
		}
	}
	ui.Printf("nothing changed (dry run)\n")
	return nil
}

// applySync makes the sync units match the `sync` section.
func applySync(c *config.Config) error {
	if len(c.Sync) > 0 {
		if err := checkDests(c); err != nil {
			return err
		}
	}
	wrote := false
	var start, restart, stop []string
	for _, t := range config.Types {
		unit := systemd.SyncUnitName(t)
		have, err := systemd.Read(unit)
		if err != nil {
			return err
		}
		if have != "" && !systemd.IsManaged(have) {
			return fmt.Errorf("%s exists and is not managed by comfynkg; refusing to overwrite", systemd.Path(unit))
		}
		s, ok := c.Sync[t]
		if !ok {
			if have != "" {
				stop = append(stop, unit)
			}
			continue
		}
		o, _, _, err := sourceOwner(c, s.Source)
		if err != nil {
			return err
		}
		want, err := systemd.RenderSync(t, o.Name)
		if err != nil {
			return err
		}
		switch {
		case have == want:
			if st, _ := systemd.StateOf(unit); st.Active != "active" {
				start = append(start, unit)
			}
			continue
		case have == "":
			start = append(start, unit)
		default:
			restart = append(restart, unit)
		}
		if g.dryRun {
			ui.Printf("> would write %s\n", unit)
			continue
		}
		if err := systemd.Write(unit, want); err != nil {
			return err
		}
		wrote = true
	}
	if g.dryRun {
		for _, u := range stop {
			ui.Printf("> would stop and remove %s\n", u)
		}
		return nil
	}
	for _, u := range stop {
		_ = systemd.Ctl("disable", "--now", u)
		if err := systemd.Remove(u); err != nil {
			return err
		}
		wrote = true
		ui.Printf("> %s removed (no sync section)\n", u)
	}
	if wrote {
		if err := systemd.Ctl("daemon-reload"); err != nil {
			return err
		}
	}
	for _, u := range start {
		if err := systemd.Ctl("enable", "--now", u); err != nil {
			return err
		}
		ui.Printf("> %s started\n", u)
	}
	for _, u := range restart {
		if err := systemd.Ctl("restart", u); err != nil {
			return err
		}
		ui.Printf("> %s restarted\n", u)
	}
	return nil
}

func firstLineOf(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	return s
}
