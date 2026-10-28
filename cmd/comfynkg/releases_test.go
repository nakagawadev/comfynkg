package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		latest, have string
		want         bool
	}{
		{"0.44.0", "0.43.0", true},
		{"0.43.0", "0.43.0", false},
		{"0.43.0", "0.44.0", false},
		{"v0.4.0", "v0.3.1", true},
		{"v0.4.0", "v0.3.1-4-g278fb35-dirty", true}, // a dev build counts as its release
		{"v0.3.1", "v0.3.1-4-g278fb35-dirty", false},
		{"1.10.0", "1.9.9", true}, // numbers, not strings
		{"0.44.0", "dev", false},  // unparsable: never "older"
		{"", "0.43.0", false},
	} {
		if got := newer(c.latest, c.have); got != c.want {
			t.Errorf("newer(%q, %q) = %v", c.latest, c.have, got)
		}
	}
}

func TestFetchReleases(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "comfyui"):
			w.Write([]byte(`{"tag_name": "v0.44.0", "prerelease": false}`))
		case strings.Contains(r.URL.Path, "comfynkg"):
			http.Error(w, "rate limited", http.StatusForbidden)
		default:
			w.Write([]byte(`{"info": {"version": "1.24.0"}}`))
		}
	}))
	defer srv.Close()
	defer func(a, b, c string) { comfyUIReleaseURL, comfynkgReleaseURL, comfyCLIReleaseURL = a, b, c }(comfyUIReleaseURL, comfynkgReleaseURL, comfyCLIReleaseURL)
	comfyUIReleaseURL, comfynkgReleaseURL, comfyCLIReleaseURL = srv.URL+"/comfyui", srv.URL+"/comfynkg", srv.URL+"/pypi"

	prev := Releases{Checked: time.Now().Add(-time.Hour), Comfynkg: "v0.3.1"}
	r, err := fetchReleases(context.Background(), prev)
	if err == nil || !strings.Contains(err.Error(), "HTTP 403") {
		t.Errorf("err = %v, want the comfynkg failure reported", err)
	}
	if r.ComfyUI != "0.44.0" || r.ComfyCLI != "1.24.0" || r.Comfynkg != "v0.3.1" || !r.Checked.After(prev.Checked) {
		t.Errorf("releases = %+v (a failed source keeps its previous value)", r)
	}

	defer func(p string) { releasesPath = p }(releasesPath)
	releasesPath = filepath.Join(t.TempDir(), "releases.json")
	if err := saveReleases(r); err != nil {
		t.Fatal(err)
	}
	if back, err := loadReleases(); err != nil || back.ComfyUI != "0.44.0" {
		t.Errorf("load = %+v, %v", back, err)
	}
}

func TestReleaseNotes(t *testing.T) {
	r := Releases{ComfyUI: "0.44.0", Comfynkg: "v0.4.0", ComfyCLI: "1.24.0"}
	got := strings.Join(releaseNotes(r, true, "v0.3.1", "1.23.0"), "\n")
	for _, want := range []string{
		"ComfyUI 0.44.0 is out · run: comfynkg update core all",
		"comfynkg v0.4.0 is out · run: comfynkg self-update",
		"comfy-cli 1.24.0 is out · run: comfynkg update cli",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if notes := releaseNotes(r, false, "v0.4.0", "1.24.0"); len(notes) != 0 {
		t.Errorf("up to date, but: %v", notes)
	}
}
