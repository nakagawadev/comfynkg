package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"comfynkg/internal/config"
	"comfynkg/internal/run"
)

// cmdRun is the unit's ExecStart: resolve flags and Tailscale IP, then exec ComfyUI.
func cmdRun(args []string) error {
	printOnly := false
	var name string
	for _, a := range args {
		if a == "--print" {
			printOnly = true
		} else if name == "" {
			name = a
		} else {
			return usagef("usage: comfynkg run <instance> [--print]")
		}
	}
	if name == "" {
		return usagef("usage: comfynkg run <instance> [--print]")
	}
	c, err := config.Load(g.config)
	if err != nil {
		return err
	}
	if _, err := c.Validate(); err != nil {
		return err
	}
	in := c.Instance(name)
	if in == nil {
		return usagef("unknown instance %q", name)
	}

	wait := 60 * time.Second
	if printOnly {
		wait = 0
	}
	ip, err := tailscaleIP(wait)
	if err != nil {
		if !printOnly {
			return err
		}
		ip = "<tailscale-ip>"
	}
	argv, err := comfyArgv(c, in, ip)
	if err != nil {
		return err
	}
	line := strings.Join(argv, " ")
	if printOnly {
		fmt.Println(line)
		return nil
	}
	fmt.Fprintf(os.Stderr, "comfynkg run %s: %s\n", name, line)
	if err := os.Chdir(c.Dir(in)); err != nil {
		return err
	}
	return syscall.Exec(argv[0], argv, os.Environ())
}

// comfyArgv builds the ComfyUI command line.
func comfyArgv(c *config.Config, in *config.Instance, ip string) ([]string, error) {
	extra, err := c.ExtraFlags(in)
	if err != nil {
		return nil, err
	}
	argv := []string{
		c.Python(in), "main.py",
		"--listen", "127.0.0.1," + ip,
		"--port", strconv.Itoa(in.Port),
		"--cuda-device", strconv.Itoa(*in.GPU),
		"--extra-model-paths-config", c.Models[in.Type],
	}
	return append(argv, extra...), nil
}

// tailscaleIP returns the Tailscale IPv4, retrying every 2 s up to wait.
// COMFYNKG_TAILSCALE_IP replaces it in the dev machine.
func tailscaleIP(wait time.Duration) (string, error) {
	if ip := os.Getenv("COMFYNKG_TAILSCALE_IP"); ip != "" {
		return ip, nil
	}
	deadline := time.Now().Add(wait)
	for {
		out, err := run.Output(run.Command(nil, "tailscale", "ip", "-4"))
		if ip := strings.TrimSpace(strings.Split(out, "\n")[0]); err == nil && ip != "" {
			return ip, nil
		}
		if time.Now().After(deadline) {
			return "", errors.New("no Tailscale IPv4 (tailscale ip -4)")
		}
		time.Sleep(2 * time.Second)
	}
}
