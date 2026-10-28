package main

import (
	"reflect"
	"strings"
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

func TestLegacyHint(t *testing.T) {
	if got := legacyHint("TypeError: Secondary flag is not valid for non-boolean flag."); !strings.Contains(got, "click<8.2") {
		t.Errorf("legacyHint = %q", got)
	}
	if got := legacyHint("ModuleNotFoundError: No module named 'git'"); got != "ModuleNotFoundError: No module named 'git'" {
		t.Errorf("legacyHint changed an unknown error: %q", got)
	}
}

func TestRunEnv(t *testing.T) {
	if got := runEnv([]string{"A=1"}); !reflect.DeepEqual(got, []string{"A=1", "CUDA_DEVICE_ORDER=PCI_BUS_ID"}) {
		t.Errorf("runEnv = %v", got)
	}
	if got := runEnv([]string{"CUDA_DEVICE_ORDER=FASTEST_FIRST"}); len(got) != 1 {
		t.Errorf("runEnv overrode an explicit order: %v", got)
	}
}

func TestShortGPU(t *testing.T) {
	for name, want := range map[string]string{
		"NVIDIA L4":                          "L4",
		"NVIDIA RTX 4000 SFF Ada Generation": "4000Ada",
		"NVIDIA GeForce RTX 5090":            "5090",
		"NVIDIA A100-SXM4-40GB":              "A100-SXM4-40GB",
		"Tesla T4":                           "T4",
		"NVIDIA RTX A6000":                   "A6000",
	} {
		if got := shortGPU(name); got != want {
			t.Errorf("shortGPU(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestParseImportFailures(t *testing.T) {
	log := `[WARNING] Traceback (most recent call last):
ModuleNotFoundError: No module named 'cv2'
[WARNING] Cannot import /home/comfy/instances/comfy_10/custom_nodes/comfyui-impact-pack module for custom nodes: No module named 'cv2'
[INFO]    0.0 seconds (IMPORT FAILED): /home/comfy/instances/comfy_10/custom_nodes/comfyui-impact-pack
[INFO]    0.0 seconds (IMPORT FAILED): /home/comfy/instances/comfy_10/custom_nodes/comfyui-base64-to-image
[INFO]    0.1 seconds: /home/comfy/instances/comfy_10/custom_nodes/comfyui_nakagawa
`
	got := parseImportFailures(log)
	want := []nodeFailure{{"comfyui-base64-to-image", "see the instance's log"}, {"comfyui-impact-pack", "No module named 'cv2'"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseImportFailures = %v", got)
	}
	if got := parseImportFailures("[INFO]    0.1 seconds: /x/custom_nodes/ok\n"); len(got) != 0 {
		t.Errorf("no failures expected: %v", got)
	}
}

func TestChangesTorch(t *testing.T) {
	safe := "Would install 3 packages\n + opencv-python-headless==5.0.0.93\n + segment-anything==1.0\n + torchmetrics-extra-is-not-torch==1\n"
	if changesTorch(safe) {
		t.Error("flagged a plan that doesn't touch torch")
	}
	for _, plan := range []string{" - torch==2.14.1\n + torch==2.4.0\n", " + torchvision==0.19\n", " ~ torchaudio==2.4\n"} {
		if !changesTorch(plan) {
			t.Errorf("missed %q", plan)
		}
	}
}
