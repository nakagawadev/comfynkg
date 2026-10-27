package main

import (
	"reflect"
	"testing"
	"time"
)

func TestSkippedNodes(t *testing.T) {
	out := "x  1/2 [  SKIPPED  ]: comfyui_nakagawa                                   => Not installed\n" +
		"x  2/2 [  SKIPPED  ]: rgthree-comfy => Up to date\n"
	if err := skippedNodes(out); err == nil || err.Error() != "not updated: comfyui_nakagawa (not installed)" {
		t.Errorf("skippedNodes = %v", err)
	}
	if err := skippedNodes("[  UPDATED  ]: a => (1 -> 2)\n[  SKIPPED  ]: b => Up to date"); err != nil {
		t.Errorf("up to date is fine: %v", err)
	}
}

func TestManagerFlags(t *testing.T) {
	if got := managerFlags([]string{"--cpu"}); !reflect.DeepEqual(got, []string{"--cpu", "--enable-manager", "--disable-manager-ui"}) {
		t.Errorf("default: %q", got)
	}
	for _, own := range []string{"--enable-manager", "--enable-manager-legacy-ui", "--disable-manager-ui"} {
		if got := managerFlags([]string{own}); !reflect.DeepEqual(got, []string{own}) {
			t.Errorf("%s: %q", own, got)
		}
	}
}

func TestShortDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		30 * time.Second:                "30s",
		45*time.Minute + 10*time.Second: "45m",
		3*time.Hour + 12*time.Minute:    "3h12m",
		52*time.Hour + 30*time.Minute:   "2d4h",
	} {
		if got := shortDuration(d); got != want {
			t.Errorf("shortDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote([]string{"comfy_0", "x'; rm -rf /"}); got != `'comfy_0' 'x'\''; rm -rf /'` {
		t.Errorf("shellQuote = %s", got)
	}
	if got := lastLine("ssh: connect to host pc1 port 29: Connection refused\n\n"); got != "ssh: connect to host pc1 port 29: Connection refused" {
		t.Errorf("lastLine = %q", got)
	}
}

func TestParseApps(t *testing.T) {
	got := parseApps("1234, 16800\n5678, 6900\n1234, 100\n[N/A], [N/A]\n")
	if got[1234] != 16900*1024*1024 || got[5678] != 6900*1024*1024 || len(got) != 2 {
		t.Errorf("parseApps = %v", got)
	}
}

func TestParseDriver(t *testing.T) {
	banner := "| NVIDIA-SMI 580.196.07             Driver Version: 580.196.07     CUDA Version: 13.0     |\n"
	if d, c := parseDriver(banner); d != "580.196.07" || c != "13.0" {
		t.Errorf("parseDriver = %q %q", d, c)
	}
	if d, _ := parseDriver("NVIDIA-SMI has failed"); d != "" {
		t.Errorf("parseDriver on an error = %q", d)
	}
}
