package sync

import (
	"bufio"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestGuard(t *testing.T) {
	dir := t.TempDir()
	if Guard(filepath.Join(dir, "missing")) == nil {
		t.Error("missing source passed")
	}
	os.MkdirAll(filepath.Join(dir, "a/b"), 0o755) // only folders: an empty mount point looks like this
	if Guard(dir) == nil {
		t.Error("empty source passed")
	}
	os.WriteFile(filepath.Join(dir, "a/b/model.safetensors"), []byte("x"), 0o644)
	if err := Guard(dir); err != nil {
		t.Errorf("non-empty source: %v", err)
	}
}

func TestParsePlan(t *testing.T) {
	out := ">f+++++++++ 12993028096 checkpoints/new-model-fp8.safetensors\n" +
		"cd+++++++++ 4096 loras/\n" +
		"*deleting   0 checkpoints/old model.safetensors\n" +
		">f.st...... 42 vae/ae.safetensors\n" +
		".d..t...... 4096 ./\n"
	want := []Change{
		{Path: "checkpoints/new-model-fp8.safetensors", Size: 12993028096},
		{Delete: true, Path: "checkpoints/old model.safetensors"},
		{Path: "vae/ae.safetensors", Size: 42},
	}
	if got := ParsePlan(out); !reflect.DeepEqual(got, want) {
		t.Errorf("ParsePlan = %+v", got)
	}
}

func TestUnstableAndStats(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "new"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "old"), []byte("x"), 0o644)
	past := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(dir, "old"), past, past)
	got := Unstable(dir, []Change{{Path: "new"}, {Path: "old"}, {Delete: true, Path: "gone"}})
	if !reflect.DeepEqual(got, []string{"new"}) {
		t.Errorf("Unstable = %v", got)
	}
	files, bytes := parseStats("Number of files: 4\nNumber of regular files transferred: 2\nTotal transferred file size: 1,234,567 bytes\n")
	if files != 2 || bytes != 1234567 {
		t.Errorf("parseStats = %d, %d", files, bytes)
	}
}

func TestIsLocal(t *testing.T) {
	name, _ := os.Hostname()
	for dest, want := range map[string]bool{"comfy@localhost:/x/": true, "comfy@127.0.0.1:/x/": true, name + ":/x/": true, "comfy@node0.invalid:/x/": false} {
		got, err := IsLocal(dest)
		if err != nil || got != want {
			t.Errorf("IsLocal(%q) = %v, %v", dest, got, err)
		}
	}
	if _, err := IsLocal("/no/host"); err == nil {
		t.Error("a local path must be refused")
	}
}

func TestParseProgress(t *testing.T) {
	n, speed, ok := parseProgress("      1,048,576  40%    9.17MB/s    0:00:01 (xfr#1, to-chk=1/2)")
	if !ok || n != 1048576 || speed != "9.17MB/s" {
		t.Errorf("parseProgress = %d %q %v", n, speed, ok)
	}
	for _, l := range []string{"", "sending incremental file list", "Number of files: 3 (reg: 2, dir: 1)", "checkpoints/a.safetensors"} {
		if _, _, ok := parseProgress(l); ok {
			t.Errorf("parseProgress(%q) matched", l)
		}
	}
}

func TestSplitLines(t *testing.T) {
	sc := bufio.NewScanner(strings.NewReader("  10  1%  1MB/s\r  20  2%  2MB/s\rdone\nlast"))
	sc.Split(splitLines)
	var got []string
	for sc.Scan() {
		got = append(got, sc.Text())
	}
	if strings.Join(got, "|") != "  10  1%  1MB/s|  20  2%  2MB/s|done|last" {
		t.Errorf("lines = %q", got)
	}
}
