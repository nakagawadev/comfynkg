package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"comfynkg/internal/config"
	"comfynkg/internal/run"
	"comfynkg/internal/ui"
)

// importTimeout bounds one import check; loading every custom node usually takes seconds.
var importTimeout = 5 * time.Minute

type nodeFailure struct{ Node, Reason string }

// cmdCheck loads every custom node of the given instances in a throwaway ComfyUI process and lists
// the ones that fail to import, with the reason. The running instances aren't touched.
func cmdCheck(args []string) error {
	if len(args) == 0 {
		return usagef("usage: comfynkg check <instance...|all>")
	}
	c, err := loadWarn()
	if err != nil {
		return err
	}
	targets, err := pick(c, args)
	if err != nil {
		return err
	}
	var bad []string
	for _, in := range targets {
		p := prefix(c, in)
		start := time.Now()
		failures, err := importCheck(c, in)
		switch {
		case err != nil:
			ui.Line(p, "custom nodes... %s", failed(err))
			bad = append(bad, in.Name)
		case len(failures) == 0:
			ui.Line(p, "custom nodes... %s", okDur(time.Since(start)))
		default:
			ui.Line(p, "custom nodes... %s", ui.Red(fmt.Sprintf("%d failed to import", len(failures))))
			for _, f := range failures {
				ui.Printf("    %s: %s\n", ui.Cyan(f.Node), f.Reason)
			}
			bad = append(bad, in.Name)
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("custom nodes don't import in %s", strings.Join(bad, ", "))
	}
	return nil
}

// importCheck starts the instance's ComfyUI with --quick-test-for-ci, which loads every custom node
// and exits before serving. It runs as the owner on the CPU, with a throwaway user folder and an
// in-memory database, so it never touches the GPU, the queue or the running instance's files.
func importCheck(c *config.Config, in *config.Instance) ([]nodeFailure, error) {
	tmp, err := os.MkdirTemp("", "comfynkg-check-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	if err := os.Chown(tmp, c.Owner.UID, c.Owner.GID); err != nil {
		return nil, err
	}
	argv := []string{"main.py", "--quick-test-for-ci", "--cpu", "--disable-metadata",
		"--user-directory", tmp, "--database-url", "sqlite:///:memory:"}
	if m := c.Models[in.Type]; m != "" {
		argv = append(argv, "--extra-model-paths-config", m)
	}
	cmd := run.Command(owner(c), c.Python(in), argv...)
	cmd.Dir = c.Dir(in)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	timer := time.AfterFunc(importTimeout, func() { cmd.Process.Kill() })
	err = cmd.Wait()
	timer.Stop()
	if err != nil {
		return nil, fmt.Errorf("ComfyUI didn't start: %w: %s", err, lastLine(out.String()))
	}
	return parseImportFailures(out.String()), nil
}

var (
	importFailedRe = regexp.MustCompile(`\(IMPORT FAILED\): (.+)$`)
	cannotImportRe = regexp.MustCompile(`Cannot import (.+) module for custom nodes: (.+)$`)
)

// parseImportFailures reads ComfyUI's startup log: "(IMPORT FAILED): <path>" names the node,
// "Cannot import <path> module for custom nodes: <error>" says why.
func parseImportFailures(log string) []nodeFailure {
	reasons, failed := map[string]string{}, map[string]bool{}
	for _, l := range strings.Split(log, "\n") {
		l = strings.TrimSpace(l)
		if m := cannotImportRe.FindStringSubmatch(l); m != nil {
			reasons[filepath.Base(m[1])] = m[2]
		}
		if m := importFailedRe.FindStringSubmatch(l); m != nil {
			failed[filepath.Base(strings.TrimSpace(m[1]))] = true
		}
	}
	var out []nodeFailure
	for n := range failed {
		r := reasons[n]
		if r == "" {
			r = "see the instance's log"
		}
		out = append(out, nodeFailure{n, r})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Node < out[j].Node })
	return out
}
