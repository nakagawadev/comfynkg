package main

import (
	"reflect"
	"testing"
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
