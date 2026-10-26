package sync

import (
	"os"
	"path/filepath"
	"reflect"
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
