package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"comfynkg/internal/run"
	"comfynkg/internal/systemd"
	"comfynkg/internal/ui"
)

// releasesPath is where `releases --fetch` (run every 6 hours by comfynkg-releases@check.timer)
// keeps the latest stable versions. `status` only reads it: no network, no delay.
var releasesPath = "/var/lib/comfynkg/releases.json"

// releasesMaxAge: older than this, the file is stale and `status` says nothing.
const releasesMaxAge = 7 * 24 * time.Hour

// freshReleases returns the last check, if there is one recent enough to trust.
func freshReleases() (Releases, bool) {
	r, err := loadReleases()
	return r, err == nil && time.Since(r.Checked) < releasesMaxAge
}

// releaseNotes are the lines `status` ends with when something newer is out, each with the command
// that updates it. behind: some instance runs an older ComfyUI than r.ComfyUI.
func releaseNotes(r Releases, behind bool, comfynkgVersion, cliVersion string) []string {
	var out []string
	note := func(what, cmd string) {
		out = append(out, ui.Yellow(what+" is out")+ui.Dim(" · run: ")+ui.Cyan(cmd))
	}
	if behind {
		note("ComfyUI "+r.ComfyUI, "comfynkg update core all")
	}
	if newer(r.Comfynkg, comfynkgVersion) {
		note("comfynkg "+r.Comfynkg, "comfynkg self-update")
	}
	if newer(r.ComfyCLI, cliVersion) {
		note("comfy-cli "+r.ComfyCLI, "comfynkg update cli")
	}
	return out
}

// Release sources; vars so tests can point them at a local server.
var (
	comfyUIReleaseURL  = "https://api.github.com/repos/comfyanonymous/ComfyUI/releases/latest"
	comfynkgReleaseURL = "https://api.github.com/repos/nakagawadev/comfynkg/releases/latest"
	comfyCLIReleaseURL = "https://pypi.org/pypi/comfy-cli/json"
)

type Releases struct {
	Checked  time.Time `json:"checked"`
	ComfyUI  string    `json:"comfyui,omitempty"`   // e.g. 0.44.0
	Comfynkg string    `json:"comfynkg,omitempty"`  // e.g. v0.4.0
	ComfyCLI string    `json:"comfy_cli,omitempty"` // e.g. 1.24.0
}

// cmdReleases prints the latest stable versions comfynkg knows of; --fetch asks GitHub and PyPI
// first and saves the answer (what the timer runs).
func cmdReleases(args []string) error {
	fetch := len(args) == 1 && args[0] == "--fetch"
	if len(args) > 0 && !fetch {
		return usagef("usage: comfynkg releases [--fetch]")
	}
	if fetch {
		if os.Geteuid() != 0 {
			return usagef("releases --fetch must run as root")
		}
		prev, _ := loadReleases()
		r, err := fetchReleases(context.Background(), prev)
		if r.Checked.After(prev.Checked) {
			if werr := saveReleases(r); werr != nil {
				return werr
			}
		}
		if err != nil {
			return err // what could be fetched was saved
		}
	}
	r, err := loadReleases()
	if err != nil {
		return fmt.Errorf("no release information yet (%v); run: comfynkg releases --fetch", err)
	}
	ui.Table([][]string{
		{"NAME", "LATEST", "HERE"},
		{"ComfyUI", or(r.ComfyUI, "-"), "see status"},
		{"comfynkg", or(r.Comfynkg, "-"), version},
		{"comfy-cli", or(r.ComfyCLI, "-"), toolVersion("comfy")},
	})
	ui.Printf("\nchecked %s\n", clock(r.Checked))
	return nil
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// fetchReleases asks each source for its latest stable release. A source that fails keeps its
// previous value; Checked moves only if at least one answered.
func fetchReleases(ctx context.Context, prev Releases) (Releases, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	r, answered := prev, false
	var errs []string
	get := func(url string, into func([]byte) (string, error), field *string) {
		code, body, err := run.HTTP(ctx, "GET", url)
		if err == nil && code != http.StatusOK {
			err = fmt.Errorf("HTTP %d", code)
		}
		var v string
		if err == nil {
			v, err = into(body)
		}
		if err != nil || v == "" {
			errs = append(errs, fmt.Sprintf("%s: %v", url, err))
			return
		}
		*field, answered = v, true
	}
	get(comfyUIReleaseURL, githubTag, &r.ComfyUI)
	get(comfynkgReleaseURL, githubTag, &r.Comfynkg)
	get(comfyCLIReleaseURL, pypiVersion, &r.ComfyCLI)
	r.ComfyUI = strings.TrimPrefix(r.ComfyUI, "v") // ComfyUI reports itself without the v
	if answered {
		r.Checked = time.Now()
	}
	if len(errs) > 0 {
		return r, fmt.Errorf("release check: %s", strings.Join(errs, "; "))
	}
	return r, nil
}

// githubTag reads tag_name from GitHub's releases/latest (which skips drafts and pre-releases).
func githubTag(body []byte) (string, error) {
	var v struct {
		Tag string `json:"tag_name"`
	}
	return v.Tag, json.Unmarshal(body, &v)
}

// pypiVersion reads info.version from PyPI's JSON API (the latest non-pre-release).
func pypiVersion(body []byte) (string, error) {
	var v struct {
		Info struct {
			Version string `json:"version"`
		} `json:"info"`
	}
	return v.Info.Version, json.Unmarshal(body, &v)
}

func loadReleases() (Releases, error) {
	var r Releases
	b, err := os.ReadFile(releasesPath)
	if err != nil {
		return r, err
	}
	return r, json.Unmarshal(b, &r)
}

// saveReleases writes the file atomically, readable by everyone (status runs as any user).
func saveReleases(r Releases) error {
	b, _ := json.MarshalIndent(r, "", "  ")
	if err := os.MkdirAll(filepath.Dir(releasesPath), 0o755); err != nil {
		return err
	}
	tmp := releasesPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, releasesPath)
}

// newer reports whether version have is older than latest, comparing major.minor.patch.
// A leading v and anything after - or + (a dev build) are ignored; an unparsable one is never older.
func newer(latest, have string) bool {
	l, ok1 := semver(latest)
	h, ok2 := semver(have)
	if !ok1 || !ok2 {
		return false
	}
	for i := range l {
		if l[i] != h[i] {
			return l[i] > h[i]
		}
	}
	return false
}

func semver(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// applyReleases installs the release-check service and timer and makes sure the timer runs.
func applyReleases() error {
	service, timer := systemd.RenderReleases()
	changed := false
	for _, u := range []struct{ name, want string }{{systemd.ReleasesService, service}, {systemd.ReleasesTimer, timer}} {
		have, err := systemd.Read(u.name)
		if err != nil {
			return err
		}
		if have != "" && !systemd.IsManaged(have) {
			return fmt.Errorf("%s exists and is not managed by comfynkg; refusing to overwrite", systemd.Path(u.name))
		}
		if have == u.want {
			continue
		}
		changed = true
		if g.dryRun {
			ui.Printf("> would write %s\n", u.name)
			continue
		}
		if err := systemd.Write(u.name, u.want); err != nil {
			return err
		}
	}
	if g.dryRun {
		return nil
	}
	if changed {
		if err := systemd.Ctl("daemon-reload"); err != nil {
			return err
		}
	}
	if st, _ := systemd.StateOf(systemd.ReleasesTimer); changed || st.Active != "active" {
		if err := systemd.Ctl("enable", "--now", systemd.ReleasesTimer); err != nil {
			return err
		}
		ui.Printf("> %s started (checks for new releases every 6 hours)\n", systemd.ReleasesTimer)
	}
	return nil
}
