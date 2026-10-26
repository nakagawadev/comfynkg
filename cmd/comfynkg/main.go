// Command comfynkg manages the ComfyUI instances of a server.
package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"comfynkg/internal/config"
	"comfynkg/internal/run"
)

var version = "dev" // set with -ldflags "-X main.version=..."

// Exit codes.
const (
	exitOK      = 0
	exitFailed  = 1
	exitUsage   = 2
	exitPartial = 3
)

type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usagef(format string, a ...any) error { return usageError{fmt.Sprintf(format, a...)} }

// partialError: some instances failed, others succeeded.
type partialError struct{ msg string }

func (e partialError) Error() string { return e.msg }

type global struct {
	config string
	dryRun bool
}

var g = global{config: config.DefaultPath}

type command struct {
	run      func(args []string) error
	needRoot bool
}

var commands = map[string]command{
	"status":      {cmdStatus, true},
	"apply":       {cmdApply, true},
	"start":       {cmdStart, true},
	"stop":        {cmdStop, true},
	"enable":      {cmdEnable, true},
	"disable":     {cmdDisable, true},
	"set":         {cmdSet, true},
	"logs":        {cmdLogs, true},
	"restart":     {cmdRestart, true},
	"drain":       {cmdDrain, true},
	"create":      {cmdCreate, true},
	"remove":      {cmdRemove, true},
	"node":        {cmdNode, true},
	"update":      {cmdUpdate, true},
	"snapshot":    {cmdSnapshot, true},
	"rollback":    {cmdRollback, true},
	"comfy":       {cmdComfy, true},
	"takeover":    {cmdTakeover, true},
	"sync":        {cmdSync, false},
	"self-update": {cmdSelfUpdate, true},
	"run":         {cmdRun, false},
	"version":     {cmdVersion, false},
}

func main() { os.Exit(realMain(os.Args[1:])) }

func realMain(argv []string) int {
	if v := os.Getenv("COMFYNKG_CONFIG"); v != "" {
		g.config = v
	}
	args, err := globalFlags(argv)
	if err == nil && len(args) == 0 {
		err = usagef("usage: comfynkg [--config path] [--dry-run] [--verbose] <command> [args]\ncommands: %s", commandList())
	}
	if err != nil {
		return report(err)
	}
	cmd, ok := commands[args[0]]
	if !ok {
		return report(usagef("unknown command %q (commands: %s)", args[0], commandList()))
	}
	if cmd.needRoot && os.Geteuid() != 0 {
		return report(usagef("%s must run as root", args[0]))
	}
	if args[0] != "run" {
		if _, err := lookPath("uv"); err != nil {
			return report(errors.New("uv is required: install it in /usr/local/bin"))
		}
	}
	return report(cmd.run(args[1:]))
}

// globalFlags removes --config, --dry-run and --verbose from anywhere before "--".
func globalFlags(argv []string) ([]string, error) {
	var out []string
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "--":
			return append(out, argv[i:]...), nil
		case a == "--dry-run":
			g.dryRun = true
		case a == "--verbose":
			run.Verbose = true
		case a == "--config":
			if i+1 >= len(argv) {
				return nil, usagef("--config needs a path")
			}
			i++
			g.config = argv[i]
		case strings.HasPrefix(a, "--config="):
			g.config = strings.TrimPrefix(a, "--config=")
		default:
			out = append(out, a)
		}
	}
	return out, nil
}

func report(err error) int {
	if err == nil {
		return exitOK
	}
	fmt.Fprintln(os.Stderr, "error:", err)
	var ue usageError
	var ve *config.ValidationError
	var pe partialError
	switch {
	case errors.As(err, &ue), errors.As(err, &ve):
		return exitUsage
	case errors.As(err, &pe):
		return exitPartial
	}
	return exitFailed
}

func commandList() string {
	names := []string{"status", "apply", "start", "stop", "restart", "drain", "create", "remove", "node", "update", "snapshot", "rollback", "comfy", "takeover", "sync", "self-update", "enable", "disable", "set", "logs", "run", "version"}
	return strings.Join(names, ", ")
}
