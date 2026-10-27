// Package instance talks to a running ComfyUI over HTTP and drives its lifecycle.
package instance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"comfynkg/internal/run"
)

// Addr is where comfynkg reaches instances; always loopback.
var Addr = "127.0.0.1"

type Client struct{ Port int }

func (c Client) url(path string) string { return fmt.Sprintf("http://%s:%d%s", Addr, c.Port, path) }

func (c Client) getJSON(ctx context.Context, method, path string, out any) (int, error) {
	code, body, err := run.HTTP(ctx, method, c.url(path))
	if err != nil {
		return 0, err
	}
	if code != http.StatusOK {
		return code, fmt.Errorf("%s %s: HTTP %d", method, path, code)
	}
	return code, json.Unmarshal(body, out)
}

type Stats struct{ Version string }

// Stats calls /system_stats; any valid answer means the instance is healthy.
func (c Client) Stats(ctx context.Context) (Stats, error) {
	var v struct {
		System struct {
			Version string `json:"comfyui_version"`
		} `json:"system"`
	}
	_, err := c.getJSON(ctx, "GET", "/system_stats", &v)
	return Stats{Version: v.System.Version}, err
}

type Queue struct{ Running, Pending int }

func (q Queue) Empty() bool { return q.Running+q.Pending == 0 }

// Queue counts running and pending prompts.
func (c Client) Queue(ctx context.Context) (Queue, error) {
	var v struct {
		Running []json.RawMessage `json:"queue_running"`
		Pending []json.RawMessage `json:"queue_pending"`
	}
	_, err := c.getJSON(ctx, "GET", "/queue", &v)
	return Queue{len(v.Running), len(v.Pending)}, err
}

// Metrics holds what status shows from the plugin's /nkg/metrics; the prompt counters start at 0 on each ComfyUI start.
type Metrics struct{ RSS, VRAM, OK, Err float64 }

// Metrics reads RSS, VRAM and the success/error prompt counters from the plugin; ok is false when the plugin isn't there.
func (c Client) Metrics(ctx context.Context) (m Metrics, ok bool, err error) {
	code, body, err := run.HTTP(ctx, "GET", c.url("/nkg/metrics"))
	if err != nil || code != http.StatusOK {
		return m, false, err
	}
	for _, line := range strings.Split(string(body), "\n") {
		name, val, found := strings.Cut(line, " ")
		if !found {
			continue
		}
		f, _ := strconv.ParseFloat(strings.TrimSpace(val), 64)
		switch name {
		case "process_resident_memory_bytes":
			m.RSS = f
		case "comfyui_torch_vram_reserved_bytes":
			m.VRAM = f
		case `comfyui_prompts_total{status="success"}`:
			m.OK = f
		case `comfyui_prompts_total{status="error"}`:
			m.Err = f
		}
	}
	return m, true, nil
}

// ErrNoPlugin means the drain endpoint isn't there (plugin missing or drain disabled).
var ErrNoPlugin = errors.New("no plugin")

type Drain struct {
	Draining bool     `json:"draining"`
	Since    *float64 `json:"since"`
	Running  int      `json:"running"`
	Pending  int      `json:"pending"`
}

// drain calls /nkg/drain. 404 and 405 mean the plugin is missing or has drain off: ErrNoPlugin.
func (c Client) drain(ctx context.Context, method string) (Drain, error) {
	var d Drain
	code, err := c.getJSON(ctx, method, "/nkg/drain", &d)
	if code == http.StatusNotFound || code == http.StatusMethodNotAllowed { // no route: plugin missing or drain off
		return d, ErrNoPlugin
	}
	return d, err
}

// DrainStatus, DrainOn and DrainOff read, start and stop drain; each returns the current state.
func (c Client) DrainStatus(ctx context.Context) (Drain, error) { return c.drain(ctx, "GET") }
func (c Client) DrainOn(ctx context.Context) (Drain, error)     { return c.drain(ctx, "POST") }
func (c Client) DrainOff(ctx context.Context) (Drain, error)    { return c.drain(ctx, "DELETE") }

// WaitHealthy polls /system_stats every 2 s until it answers or timeout passes.
// alive, when set, is checked on every round so a crash fails fast instead of waiting the full timeout.
func (c Client) WaitHealthy(ctx context.Context, timeout time.Duration, alive func() error) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := c.Stats(ctx); err == nil {
			return nil
		}
		if alive != nil {
			if err := alive(); err != nil {
				return err
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("not healthy after %s", timeout.Round(time.Second))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}
