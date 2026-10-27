package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"comfynkg/internal/comfycli"
	"comfynkg/internal/config"
	"comfynkg/internal/run"
	"comfynkg/internal/systemd"
	"comfynkg/internal/ui"
)

// Where comfy-cli lives. The same env is used to upgrade it.
var uvToolEnv = []string{"UV_TOOL_DIR=/opt/comfynkg/tools", "UV_TOOL_BIN_DIR=/usr/local/bin", "UV_PYTHON_INSTALL_DIR=/opt/comfynkg/python"}

func workspace(c *config.Config, in *config.Instance) comfycli.Workspace {
	return comfycli.Workspace{Path: c.Dir(in), Owner: owner(c), Prefix: prefix(c, in)}
}

// asOwner runs a command as the owner and returns its output.
func asOwner(c *config.Config, name string, args ...string) (string, error) {
	return run.Output(run.Command(owner(c), name, args...))
}

// save re-parses and validates an edited config, then writes it (unless --dry-run).
func save(c *config.Config) (*config.Config, error) {
	data, err := c.Bytes()
	if err != nil {
		return nil, err
	}
	next, err := config.Parse(data)
	if err != nil {
		return nil, err
	}
	next.Path = g.config
	warnings, err := next.Validate()
	for _, w := range warnings {
		ui.Warn("%s", w)
	}
	if err != nil {
		return nil, err
	}
	if !g.dryRun {
		if err := config.WriteFile(g.config, data); err != nil {
			return nil, err
		}
	}
	return next, nil
}

func isActive(in *config.Instance) bool {
	st, err := systemd.StateOf(unitOf(in))
	return err == nil && st.Active == "active"
}

// --- create / remove ---------------------------------------------------------

func cmdCreate(args []string) error {
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	typ := fs.String("type", "", "image or video")
	port := fs.Int("port", 0, "port")
	gpu := fs.Int("gpu", -1, "cuda device")
	from := fs.String("nodes-from", "", "copy custom nodes from this instance or ComfyUI path")
	fromVenv := fs.String("nodes-venv", "", "venv of --nodes-from when it isn't <path>/.venv")
	ver := fs.String("version", "", "ComfyUI version: latest, nightly or a number")
	names, err := parseMixed(fs, args)
	if err != nil || len(names) != 1 || *typ == "" || *port == 0 || *gpu < 0 {
		return usagef("usage: comfynkg create <instance> --type image|video --port N --gpu N [--nodes-from <path|instance>] [--nodes-venv <venv>] [--version X]")
	}
	name := names[0]
	c, err := loadWarn()
	if err != nil {
		return err
	}
	if err := c.AddInstance(name, *typ, *port, *gpu); err != nil {
		return usagef("%v", err)
	}
	data, _ := c.Bytes()
	next, err := config.Parse(data)
	if err != nil {
		return usagef("%v", err)
	}
	if _, err := next.Validate(); err != nil {
		return err
	}
	in := next.Instance(name)
	p := prefix(next, in)
	dir := next.Dir(in)
	if _, err := os.Stat(dir); err == nil {
		return usagef("%s already exists", dir)
	}
	src := ""
	if *from != "" {
		src = *from
		if other := c.Instance(*from); other != nil {
			src = c.Dir(other)
		}
		if _, err := os.Stat(src); err != nil {
			return usagef("--nodes-from: %v", err)
		}
	}
	if g.dryRun {
		ui.Line(p, "would install ComfyUI in %s", dir)
		if src != "" {
			ui.Line(p, "would copy custom nodes from %s", src)
		}
		ui.Printf("nothing changed (dry run)\n")
		return nil
	}

	// Snapshot the source before installing, so a source comfynkg can't read fails in seconds.
	snap := ""
	if src != "" {
		var cleanup func()
		if snap, cleanup, err = saveNodes(next, in, src, *fromVenv); err != nil {
			return fmt.Errorf("%s: copy nodes from %s: %w", name, src, err)
		}
		defer cleanup()
	}

	w := workspace(next, in)
	installArgs, _ := config.SplitArgs(envOr("COMFYNKG_INSTALL_ARGS", "--nvidia"))
	if *ver != "" {
		installArgs = append(installArgs, "--version", *ver)
	}
	ui.Line(p, "installing ComfyUI in %s...", dir)
	start := time.Now()
	if err := w.Run(ui.Out, append([]string{"install"}, installArgs...)...); err != nil {
		return fmt.Errorf("%s: install: %w (remove %s before retrying)", name, err, dir)
	}
	ui.Line(p, "installed (%s)", ui.Dur(time.Since(start)))
	if snap != "" {
		if err := restoreNodes(next, in, src, snap); err != nil {
			return fmt.Errorf("%s: copy nodes: %w (remove %s before retrying)", name, err, dir)
		}
	}
	if err := config.WriteFile(g.config, data); err != nil {
		return err
	}
	if err := apply(next, nil); err != nil {
		return err
	}
	ui.Printf("done: %s created (disabled); start it with: comfynkg enable %s\n", name, name)
	return nil
}

// legacyManagerDirs are the usual folder names of a classic ComfyUI-Manager clone.
var legacyManagerDirs = []string{"ComfyUI-Manager", "comfyui-manager"}

// saveNodes snapshots the custom nodes of src as its own owner, into a temp dir that cleanup removes.
// comfy-cli needs the pip comfyui_manager package; older installs only have a Manager clone in
// custom_nodes, so its cm-cli.py is tried next.
func saveNodes(c *config.Config, in *config.Instance, src, venv string) (snap string, cleanup func(), err error) {
	p := prefix(c, in)
	fi, err := os.Stat(src)
	if err != nil {
		return "", nil, err
	}
	srcOwner, err := config.OwnerOf(fi)
	if err != nil {
		return "", nil, err
	}
	tmp, err := os.MkdirTemp("", "comfynkg-nodes-")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { os.RemoveAll(tmp) }
	if err := os.Chmod(tmp, 0o755); err != nil {
		cleanup()
		return "", nil, err
	}
	if err := os.Chown(tmp, srcOwner.UID, srcOwner.GID); err != nil {
		cleanup()
		return "", nil, err
	}
	snap = filepath.Join(tmp, "snapshot.json")
	ui.Line(p, "saving a snapshot of the custom nodes in %s...", src)
	srcWS := comfycli.Workspace{Path: src, Owner: &run.User{Name: srcOwner.Name, Home: srcOwner.Home}, Venv: venv, Prefix: p}
	out, err := run.Output(srcWS.Cmd("node", "save-snapshot", "--output", snap))
	if err == nil {
		return snap, cleanup, nil
	}
	for _, dir := range legacyManagerDirs {
		if _, lerr := run.Output(srcWS.LegacySnapshotCmd(dir, snap)); lerr == nil {
			ui.Line(p, "snapshot saved with the classic ComfyUI-Manager (custom_nodes/%s)", dir)
			return snap, cleanup, nil
		}
	}
	cleanup()
	return "", nil, fmt.Errorf("no ComfyUI-Manager can snapshot it (comfy-cli: %w; no custom_nodes/{%s}/cm-cli.py that runs)\n%s",
		err, strings.Join(legacyManagerDirs, ","), out)
}

// restoreNodes restores a snapshot from saveNodes into the new instance, then its single-file nodes.
func restoreNodes(c *config.Config, in *config.Instance, src, snap string) error {
	ui.Line(prefix(c, in), "copying custom nodes from %s...", src)
	if err := workspace(c, in).Run(ui.Out, "node", "restore-snapshot", snap); err != nil {
		return err
	}
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	srcOwner, err := config.OwnerOf(fi)
	if err != nil {
		return err
	}
	return copyFileNodes(c, in, src, snap, &run.User{Name: srcOwner.Name, Home: srcOwner.Home})
}

// copyFileNodes copies single-file custom nodes, which Manager lists in a snapshot but doesn't restore.
// Both sides are user folders: files are read as the source owner and written as the new owner,
// never by root, so a planted symlink can't read or write root-only files.
func copyFileNodes(c *config.Config, in *config.Instance, src, snap string, srcOwner *run.User) error {
	data, err := run.ReadFile(srcOwner, snap)
	if err != nil {
		return err
	}
	var s struct {
		Files []struct {
			Filename string `json:"filename"`
			Disabled bool   `json:"disabled"`
		} `json:"file_custom_nodes"`
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	for _, f := range s.Files {
		if f.Disabled || f.Filename == "" || f.Filename != filepath.Base(f.Filename) || f.Filename == ".." {
			continue
		}
		p := prefix(c, in)
		data, err := run.ReadFile(srcOwner, filepath.Join(src, "custom_nodes", f.Filename))
		if err != nil {
			ui.Line(p, "%s %s: not readable by its owner, skipped", ui.Yellow("warning:"), f.Filename)
			continue
		}
		wrote, err := run.CreateFile(owner(c), filepath.Join(c.Dir(in), "custom_nodes", f.Filename), data)
		if err != nil {
			return err
		}
		if wrote {
			ui.Line(p, "copied %s", f.Filename)
		}
	}
	return nil
}

func cmdRemove(args []string) error {
	fs := flag.NewFlagSet("remove", flag.ContinueOnError)
	purge := fs.Bool("purge", false, "also delete the instance folder")
	names, err := parseMixed(fs, args)
	if err != nil || len(names) != 1 {
		return usagef("usage: comfynkg remove <instance> [--purge]")
	}
	c, err := loadWarn()
	if err != nil {
		return err
	}
	in := c.Instance(names[0])
	if in == nil {
		return usagef("unknown instance %q", names[0])
	}
	p, dir, unit := prefix(c, in), c.Dir(in), unitOf(in)
	if g.dryRun {
		ui.Line(p, "would stop, disable and remove %s and its entry in %s", unit, g.config)
		if *purge {
			ui.Line(p, "would delete %s", dir)
		}
		ui.Printf("nothing changed (dry run)\n")
		return nil
	}
	if have, _ := systemd.Read(unit); systemd.IsManaged(have) {
		_ = systemd.Ctl("disable", "--now", unit)
		if err := systemd.Remove(unit); err != nil {
			return err
		}
		if err := systemd.Ctl("daemon-reload"); err != nil {
			return err
		}
	}
	if err := c.RemoveInstance(in.Name); err != nil {
		return err
	}
	if _, err := save(c); err != nil {
		return err
	}
	ui.Line(p, "removed from %s and systemd", g.config)
	if *purge {
		if filepath.Dir(dir) != filepath.Clean(c.Root) {
			return fmt.Errorf("refusing to delete %s: not directly under root", dir)
		}
		if _, err := asOwner(c, "rm", "-rf", "--", dir); err != nil {
			return err
		}
		ui.Line(p, "deleted %s", dir)
	}
	return nil
}

// --- snapshots ---------------------------------------------------------------

func snapshotDir(c *config.Config, in *config.Instance) string {
	return filepath.Join(c.Root, ".snapshots", in.Name)
}

func takeSnapshot(c *config.Config, in *config.Instance) (string, error) {
	dir := snapshotDir(c, in)
	if _, err := asOwner(c, "mkdir", "-p", dir); err != nil {
		return "", err
	}
	path := filepath.Join(dir, time.Now().Format("2006-01-02_150405")+".json")
	if out, err := run.Output(workspace(c, in).Cmd("node", "save-snapshot", "--output", path)); err != nil {
		return "", fmt.Errorf("%w\n%s", err, out)
	}
	return path, nil
}

func latestSnapshot(c *config.Config, in *config.Instance) (string, error) {
	files, _ := filepath.Glob(filepath.Join(snapshotDir(c, in), "*.json"))
	if len(files) == 0 {
		return "", fmt.Errorf("%s: no snapshots in %s", in.Name, snapshotDir(c, in))
	}
	sort.Strings(files)
	return files[len(files)-1], nil
}

func restoreSnapshot(c *config.Config, in *config.Instance, path string) error {
	if out, err := run.Output(workspace(c, in).Cmd("node", "restore-snapshot", path)); err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}

func cmdSnapshot(args []string) error {
	c, err := loadWarn()
	if err != nil {
		return err
	}
	targets, err := pick(c, args)
	if err != nil {
		return err
	}
	for _, in := range targets {
		path, err := takeSnapshot(c, in)
		if err != nil {
			return err
		}
		ui.Line(prefix(c, in), "snapshot saved: %s", path)
	}
	return nil
}

func cmdRollback(args []string) error {
	if len(args) < 1 || len(args) > 2 {
		return usagef("usage: comfynkg rollback <instance> [snapshot-file]")
	}
	c, err := loadWarn()
	if err != nil {
		return err
	}
	in := c.Instance(args[0])
	if in == nil {
		return usagef("unknown instance %q", args[0])
	}
	file := ""
	if len(args) == 2 {
		file = args[1]
	} else if file, err = latestSnapshot(c, in); err != nil {
		return err
	}
	p := prefix(c, in)
	if g.dryRun {
		ui.Line(p, "would restore %s", file)
		return nil
	}
	ui.Line(p, "restoring %s...", filepath.Base(file))
	if err := restoreSnapshot(c, in, file); err != nil {
		return err
	}
	if isActive(in) {
		ctx, stop := interruptible()
		defer stop()
		return restartOne(ctx, c, in, restartOpts{wait: true, timeout: 10 * time.Minute})
	}
	return nil
}

// --- canary pipeline -----------------------------------------------------------

// change mutates one instance and returns how to undo the parts a node snapshot can't
// (core version, a git checkout); nil means the snapshot is enough.
type change func(in *config.Instance) (undo func() error, err error)

// canary snapshots every target, then changes them one at a time; the first one is the canary.
// A failed change or restart rolls that instance back and stops.
func canary(c *config.Config, targets []*config.Instance, label string, do change) error {
	if g.dryRun {
		for _, in := range targets {
			ui.Line(prefix(c, in), "would %s", label)
		}
		ui.Printf("nothing changed (dry run)\n")
		return nil
	}
	snaps := map[string]string{}
	for _, in := range targets {
		snap, err := takeSnapshot(c, in)
		if err != nil {
			return fmt.Errorf("%s: snapshot: %w", in.Name, err)
		}
		snaps[in.Name] = snap
		ui.Line(prefix(c, in), "snapshot saved: %s", filepath.Base(snap))
	}
	ctx, stop := interruptible()
	defer stop()
	updated := 0
	for i, in := range targets {
		p := prefix(c, in)
		tag := ""
		if i == 0 && len(targets) > 1 {
			tag = "canary: "
		}
		ui.Line(p, "%s%s...", tag, label)
		undo, err := do(in)
		if err == nil && isActive(in) {
			err = restartOne(ctx, c, in, restartOpts{wait: true, timeout: 10 * time.Minute})
			if errors.Is(err, errSkipped) {
				ui.Printf("stopped: %s is changed on disk but was not restarted (queue busy); %d updated\n", in.Name, updated)
				return partialError{in.Name + " not restarted"}
			}
		}
		if err == nil {
			updated++
			continue
		}
		ui.Line(p, "rolling back to %s...", filepath.Base(snaps[in.Name]))
		if undo != nil {
			if uerr := undo(); uerr != nil {
				ui.Line(p, "%s", failed(fmt.Errorf("undo: %w", uerr)))
			}
		}
		if rerr := restoreSnapshot(c, in, snaps[in.Name]); rerr != nil {
			ui.Line(p, "%s", failed(fmt.Errorf("restore: %w", rerr)))
		}
		if st, _ := systemd.StateOf(unitOf(in)); in.Enabled || st.Active != "inactive" {
			if d, rerr := startAndWait(c, in, "restart"); rerr != nil {
				ui.Line(p, "restarting... %s", failed(rerr))
			} else {
				ui.Line(p, "restarting... %s", okDur(d))
			}
		}
		ui.Printf("stopped: %s failed and was rolled back; %d updated\n", in.Name, updated)
		if updated > 0 {
			return partialError{in.Name + " failed"}
		}
		return fmt.Errorf("%s: %w", in.Name, err)
	}
	ui.Printf("done: %d updated\n", updated)
	return nil
}

// --- node / update -------------------------------------------------------------

func cmdNode(args []string) error {
	fs := flag.NewFlagSet("node", flag.ContinueOnError)
	ref := fs.String("ref", "", "install from git at this branch or tag")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) != 3 {
		return usagef("usage: comfynkg node install|uninstall|enable|disable <instance|all> <node> [--ref branch|tag]")
	}
	verb, target, node := pos[0], pos[1], pos[2]
	switch verb {
	case "install", "uninstall", "enable", "disable":
	default:
		return usagef("node: install, uninstall, enable or disable")
	}
	if *ref != "" && verb != "install" {
		return usagef("--ref only works with install")
	}
	c, err := loadWarn()
	if err != nil {
		return err
	}
	targets, err := pick(c, []string{target})
	if err != nil {
		return err
	}
	if *ref != "" {
		return canary(c, targets, fmt.Sprintf("installing %s@%s", node, *ref), func(in *config.Instance) (func() error, error) {
			return gitInstall(c, in, node, *ref)
		})
	}
	label := map[string]string{"install": "installing", "uninstall": "uninstalling", "enable": "enabling", "disable": "disabling"}[verb] + " " + node
	return canary(c, targets, label, func(in *config.Instance) (func() error, error) {
		a := []string{"node", verb, node}
		if verb == "install" {
			a = append(a, "--exit-on-fail")
		}
		return nil, workspace(c, in).Run(ui.Out, a...)
	})
}

// gitInstall clones (or moves) a custom node to a git ref, then installs its requirements with uv.
// ponytail: runs requirements.txt only, not a node's install.py; add it if a node needs it.
func gitInstall(c *config.Config, in *config.Instance, url, ref string) (func() error, error) {
	name := strings.TrimSuffix(filepath.Base(strings.TrimRight(url, "/")), ".git")
	if name == "" || name == "." || strings.ContainsAny(name, " /") {
		return nil, fmt.Errorf("cannot get a folder name from %q", url)
	}
	dest := filepath.Join(c.Dir(in), "custom_nodes", name)
	p := prefix(c, in)
	var undo func() error
	if _, err := os.Stat(dest); err == nil {
		if _, err := os.Stat(filepath.Join(dest, ".git")); err != nil {
			return nil, fmt.Errorf("%s exists and is not a git checkout; uninstall it first", dest)
		}
		prev, err := asOwner(c, "git", "-C", dest, "rev-parse", "HEAD")
		if err != nil {
			return nil, err
		}
		prev = strings.TrimSpace(prev)
		undo = func() error { _, err := asOwner(c, "git", "-C", dest, "checkout", "--detach", prev); return err }
		if _, err := asOwner(c, "git", "-C", dest, "fetch", "--tags", "origin"); err != nil {
			return undo, err
		}
		if _, err := asOwner(c, "git", "-C", dest, "checkout", ref); err != nil {
			return undo, err
		}
		if _, err := asOwner(c, "git", "-C", dest, "symbolic-ref", "-q", "HEAD"); err == nil { // a branch: fast-forward it
			if _, err := asOwner(c, "git", "-C", dest, "merge", "--ff-only", "origin/"+ref); err != nil {
				return undo, err
			}
		}
	} else {
		undo = func() error { _, err := asOwner(c, "rm", "-rf", "--", dest); return err }
		if err := run.Stream(run.Command(owner(c), "git", "clone", "--branch", ref, url, dest), ui.Out, p); err != nil {
			return undo, err
		}
	}
	if req := filepath.Join(dest, "requirements.txt"); fileExists(req) {
		if err := run.Stream(run.Command(owner(c), "uv", "pip", "install", "--python", c.Python(in), "-r", req), ui.Out, p); err != nil {
			return undo, err
		}
	}
	return undo, nil
}

func cmdUpdate(args []string) error {
	if len(args) == 0 {
		return usagef("usage: comfynkg update core|nodes|cli ...")
	}
	switch args[0] {
	case "cli":
		return updateCLI()
	case "core":
		fs := flag.NewFlagSet("update core", flag.ContinueOnError)
		ver := fs.String("version", "latest", "latest, nightly or a version number")
		names, err := parseMixed(fs, args[1:])
		if err != nil || len(names) != 1 {
			return usagef("usage: comfynkg update core <instance|all> [--version X]")
		}
		c, err := loadWarn()
		if err != nil {
			return err
		}
		targets, err := pick(c, names)
		if err != nil {
			return err
		}
		return canary(c, targets, "updating ComfyUI to "+*ver, func(in *config.Instance) (func() error, error) {
			return updateCore(c, in, *ver)
		})
	case "nodes":
		if len(args) < 2 {
			return usagef("usage: comfynkg update nodes <instance|all> [node...]")
		}
		c, err := loadWarn()
		if err != nil {
			return err
		}
		targets, err := pick(c, args[1:2])
		if err != nil {
			return err
		}
		nodes := args[2:]
		if len(nodes) == 0 {
			nodes = []string{"all"}
		}
		return canary(c, targets, "updating nodes", func(in *config.Instance) (func() error, error) {
			return nil, workspace(c, in).Run(ui.Out, append([]string{"node", "update"}, nodes...)...)
		})
	}
	return usagef("update: core, nodes or cli")
}

// updateCore moves ComfyUI with comfy-cli; undo checks out the previous commit and reinstalls its requirements.
func updateCore(c *config.Config, in *config.Instance, ver string) (func() error, error) {
	dir := c.Dir(in)
	prev, err := asOwner(c, "git", "-C", dir, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	prev = strings.TrimSpace(prev)
	undo := func() error {
		if _, err := asOwner(c, "git", "-C", dir, "checkout", "--detach", prev); err != nil {
			return err
		}
		_, err := asOwner(c, "uv", "pip", "install", "--python", c.Python(in), "-r", filepath.Join(dir, "requirements.txt"))
		return err
	}
	return undo, workspace(c, in).Run(ui.Out, "update", "comfy", "--version", ver)
}

func updateCLI() error {
	before := toolVersion("comfy")
	if g.dryRun {
		ui.Printf("would run: uv tool upgrade comfy-cli (now %s)\n", before)
		return nil
	}
	cmd := run.Command(nil, "uv", "tool", "upgrade", "comfy-cli")
	cmd.Env = append(cmd.Env, uvToolEnv...)
	if err := run.Stream(cmd, ui.Out, ">"); err != nil {
		return err
	}
	if out, err := run.Output(run.Command(nil, "chmod", "-R", "a+rX", "/opt/comfynkg")); err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	ui.Printf("comfy-cli %s -> %s\n", before, toolVersion("comfy"))
	return nil
}

// --- passthrough ---------------------------------------------------------------

func cmdComfy(args []string) error {
	if len(args) < 3 || args[1] != "--" {
		return usagef("usage: comfynkg comfy <instance> -- <comfy-cli args>")
	}
	c, err := loadWarn()
	if err != nil {
		return err
	}
	in := c.Instance(args[0])
	if in == nil {
		return usagef("unknown instance %q", args[0])
	}
	rest := args[2:]
	if why := comfycli.Guard(rest); why != "" {
		ui.Line(prefix(c, in), "refused: %s", why)
		return usageError{"refused"}
	}
	if g.dryRun {
		ui.Line(prefix(c, in), "would run: comfy --workspace %s %s", c.Dir(in), strings.Join(rest, " "))
		return nil
	}
	return workspace(c, in).Run(ui.Out, rest...)
}

// --- helpers -------------------------------------------------------------------

// interruptible cancels on Ctrl-C/SIGTERM so waits can undo drain before exiting.
func interruptible() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
