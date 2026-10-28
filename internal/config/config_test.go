package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSplitArgs(t *testing.T) {
	cases := map[string][]string{
		"":                                    nil,
		"--disable-metadata":                  {"--disable-metadata"},
		"  --a   b\t--c ":                     {"--a", "b", "--c"},
		`--x 'a b' "c \"d\"" e\ f`:            {"--x", "a b", `c "d"`, "e f"},
		`--empty ''`:                          {"--empty", ""},
		"--cache-ram 8 --preview-method auto": {"--cache-ram", "8", "--preview-method", "auto"},
	}
	for in, want := range cases {
		got, err := SplitArgs(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("SplitArgs(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{`'open`, `"open`, `trailing\`} {
		if _, err := SplitArgs(bad); err == nil {
			t.Errorf("SplitArgs(%q) should fail", bad)
		}
	}
}

// fixture writes a config with a real root and models files owned by the test user.
func fixture(t *testing.T, body string) *Config {
	t.Helper()
	dir := t.TempDir()
	for _, d := range []string{"instances/comfy_0", "config/image", "config/video", "models/image"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"config/image/extra_model_paths.yaml", "config/video/extra_model_paths.yaml"} {
		os.WriteFile(filepath.Join(dir, f), []byte("x: {}\n"), 0o644)
	}
	body = strings.ReplaceAll(body, "DIR", dir)
	c, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return c
}

const good = `
host: master
root: DIR/instances
flags: --disable-metadata
memory_max: 48G
models:
  image: DIR/config/image/extra_model_paths.yaml
  video: DIR/config/video/extra_model_paths.yaml

comfy_1:
  type: image
  port: 8189
  gpu: 1
  flags: --gpu-only
  enabled: false

comfy_0:   # first
  type: image
  port: 8188
  gpu: 0
  enabled: true
`

func TestParseAndValidate(t *testing.T) {
	c := fixture(t, good)
	warnings, err := c.Validate()
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings: %v", warnings)
	}
	if c.Instances[0].Name != "comfy_0" || c.Instances[1].Name != "comfy_1" {
		t.Fatalf("instances not sorted: %v, %v", c.Instances[0].Name, c.Instances[1].Name)
	}
	flags, _ := c.ExtraFlags(c.Instance("comfy_1"))
	if !reflect.DeepEqual(flags, []string{"--disable-metadata", "--gpu-only"}) {
		t.Fatalf("flags = %q", flags)
	}
	if c.HostLabel() != "master" || c.MemoryMaxFor(c.Instance("comfy_0")) != "48G" {
		t.Fatal("host or memory_max")
	}
	if c.Owner.Name == "" || c.Owner.UID == 0 && os.Geteuid() != 0 {
		t.Fatalf("owner = %+v", c.Owner)
	}
}

func TestValidateErrors(t *testing.T) {
	cases := []struct{ name, patch, want string }{
		{"dup port", "comfy_1:\n  type: image\n  port: 8188\n  gpu: 1\n", "already used by comfy_0"},
		{"bad type", "comfy_1:\n  type: audio\n  port: 8189\n  gpu: 1\n", "must be image or video"},
		{"owned flag", "comfy_1:\n  type: image\n  port: 8189\n  gpu: 1\n  flags: --port 9000\n", "--port is set by comfynkg"},
		{"owned flag =", "comfy_1:\n  type: image\n  port: 8189\n  gpu: 1\n  flags: --listen=0.0.0.0\n", "--listen is set by comfynkg"},
		{"no gpu", "comfy_1:\n  type: image\n  port: 8189\n", "gpu: required"},
		{"bad memory", "comfy_1:\n  type: image\n  port: 8189\n  gpu: 1\n  memory_max: lots\n", "not a systemd size"},
		{"newline in root", "", "may only contain"},
		{"bad host", "hosts: [pc0, '-oProxyCommand=x']\n", "is not a host name"},
		{"models in sync source", "sync:\n  image:\n    source: DIR/config/\n    to: [comfy@node0:/x/]\n", "inside sync.image.source"},
	}
	base := strings.Split(good, "comfy_1:")[0] + "comfy_0:\n  type: image\n  port: 8188\n  gpu: 0\n  enabled: true\n"
	for _, tc := range cases {
		body := base + tc.patch
		if tc.name == "newline in root" {
			body = strings.Replace(body, "root: DIR/instances", `root: "DIR/instances\nExecStartPre=+/tmp/x"`, 1)
		}
		c := fixture(t, body)
		_, err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for body, want := range map[string]string{
		"comfy_0:\n  type: image\n  prot: 1\n": "field prot not found",
		"Bad-Name:\n  type: image\n":           "not a valid instance name",
		"models:\n  image: a\n  sound: b\n":    "", // unknown model type is a Validate error, not a parse error
	} {
		_, err := Parse([]byte(body))
		if want == "" {
			if err != nil {
				t.Errorf("%q: unexpected %v", body, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", body, err, want)
		}
	}
}

func TestWarnings(t *testing.T) {
	c := fixture(t, `
root: DIR/instances
models:
  image: DIR/config/image/extra_model_paths.yaml
comfy_0:
  type: image
  port: 8188
  gpu: 0
  enabled: true
comfy_9:
  type: image
  port: 8197
  gpu: 0
  enabled: true
`)
	warnings, err := c.Validate()
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(warnings, "\n")
	for _, want := range []string{"comfy_0 has no --disable-metadata", "comfy_9 is enabled but"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing warning %q in:\n%s", want, all)
		}
	}
	if strings.Contains(all, "share gpu") {
		t.Errorf("sharing a GPU is not a warning:\n%s", all)
	}
}

func TestSetKeepsComments(t *testing.T) {
	c := fixture(t, good)
	if err := c.Set("comfy_1", "enabled", "yes"); err == nil {
		t.Fatal("yes should not be a bool")
	}
	for _, kv := range [][3]string{{"comfy_1", "enabled", "true"}, {"comfy_0", "flags", "--cache-ram 8"}, {"global", "memory_max", "40G"}} {
		if err := c.Set(kv[0], kv[1], kv[2]); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Set("comfy_7", "port", "1"); err == nil {
		t.Fatal("unknown instance should fail")
	}
	data, _ := c.Bytes()
	out := string(data)
	for _, want := range []string{"comfy_0: # first", "flags: --cache-ram 8", "memory_max: 40G"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	next, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if !next.Instance("comfy_1").Enabled || next.MemoryMax != "40G" {
		t.Fatal("edits not parsed back")
	}
}

func TestPluginFile(t *testing.T) {
	c := fixture(t, good+`
plugin:
  stats:
    enabled: false
    dir: /x
  instances:
    comfy_0: { stats: { enabled: true }, drain: false }
    comfy_1: { stats: { save_graph: all } }
`)
	if _, err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	b, err := c.PluginFile("image")
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	for _, want := range []string{"# managed by comfynkg", "drain: true", "stats:\n    dir: /x\n    enabled: false", `"8188":` + "\n        drain: false\n        enabled: true", `"8189":` + "\n        save_graph: all"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if !strings.HasSuffix(c.PluginPath("image"), "/config/image/comfyui_nakagawa.yaml") {
		t.Error(c.PluginPath("image"))
	}
	// image and video on one models file: one plugin file with both types' overrides
	shared := fixture(t, strings.Replace(good, "config/video/", "config/image/", 1)+"comfy_v:\n  type: video\n  port: 8190\n  gpu: 2\nplugin:\n  instances:\n    comfy_0: { drain: false }\n    comfy_v: { stats: { enabled: true } }\n")
	img, _ := shared.PluginFile("image")
	vid, _ := shared.PluginFile("video")
	if string(img) != string(vid) || !strings.Contains(string(img), `"8188":`) || !strings.Contains(string(img), `"8190":`) {
		t.Errorf("shared models file:\n%s\n---\n%s", img, vid)
	}
	bad := fixture(t, good+"plugin:\n  instances:\n    comfy_9: {}\n  extra: 1\n")
	if _, err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "no such instance") || !strings.Contains(err.Error(), "plugin.extra") {
		t.Errorf("err = %v", err)
	}
}

func TestDotDirsToCreate(t *testing.T) {
	home := t.TempDir()
	os.Mkdir(filepath.Join(home, ".cache"), 0o700)
	if got := DotDirsToCreate(home, os.Getuid()); got != nil {
		t.Errorf("own home: %v", got)
	}
	// a home someone else owns (root on Hestia): every usual folder not owned by uid is listed
	other := os.Getuid() + 1
	if got := DotDirsToCreate(home, other); len(got) != 5 || got[0] != filepath.Join(home, ".cache") {
		t.Errorf("foreign home: %v", got)
	}
}

func TestRenameInstance(t *testing.T) {
	c := fixture(t, good+"plugin:\n  instances:\n    comfy_1: { drain: false }\n")
	if err := c.RenameInstance("comfy_1", "comfy_x"); err != nil {
		t.Fatal(err)
	}
	b, _ := c.Bytes()
	next, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if next.Instance("comfy_x") == nil || next.Instance("comfy_1") != nil || next.Instance("comfy_x").Port != 8189 {
		t.Fatalf("not renamed:\n%s", b)
	}
	if !strings.Contains(string(b), "comfy_0: # first") || !strings.Contains(string(b), "comfy_x: {drain: false}") {
		t.Errorf("comments or plugin.instances lost:\n%s", b)
	}
	for _, bad := range []string{"comfy_0", "Bad Name", "comfy_x"} {
		if err := c.RenameInstance("comfy_x", bad); err == nil && bad != "comfy_x" {
			t.Errorf("renaming to %q was accepted", bad)
		}
	}
	if err := c.RenameInstance("nope", "comfy_y"); err == nil {
		t.Error("renaming a missing instance was accepted")
	}
}
