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
