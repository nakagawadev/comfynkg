package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"comfynkg/internal/systemd"
	"comfynkg/internal/ui"
)

const releases = "https://github.com/nakagawadev/comfynkg/releases"

var tagRe = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)

// releaseURL is where a release asset lives: the latest release, or a given tag.
func releaseURL(tag, asset string) string {
	if tag == "" {
		return releases + "/latest/download/" + asset
	}
	return releases + "/download/" + tag + "/" + asset
}

// selfUpdateSource resolves the self-update argument to a local binary: a path is used as is;
// nothing (latest) or a tag is downloaded next to the installed binary and checked against the
// release's SHA256SUMS. cleanup removes a downloaded file.
func selfUpdateSource(args []string) (string, func(), error) {
	noop := func() {}
	tag := ""
	if len(args) == 1 {
		if !tagRe.MatchString(args[0]) {
			return args[0], noop, nil
		}
		tag = args[0]
	}
	asset := "comfynkg-linux-" + runtime.GOARCH
	ui.Printf("downloading %s\n", releaseURL(tag, asset))
	sums, err := fetch(releaseURL(tag, "SHA256SUMS"), 1<<16)
	if err != nil {
		return "", noop, err
	}
	want, err := sumFor(string(sums), asset)
	if err != nil {
		return "", noop, err
	}
	bin, err := fetch(releaseURL(tag, asset), 256<<20)
	if err != nil {
		return "", noop, err
	}
	if got := sha256.Sum256(bin); hex.EncodeToString(got[:]) != want {
		return "", noop, fmt.Errorf("%s: checksum mismatch, not installed", asset)
	}
	// Next to the installed binary, not /tmp: /tmp may be mounted noexec.
	f, err := os.CreateTemp(filepath.Dir(systemd.Binary), ".comfynkg-download-*")
	if err != nil {
		return "", noop, err
	}
	cleanup := func() { os.Remove(f.Name()) }
	_, err = f.Write(bin)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(f.Name(), 0o755)
	}
	if err != nil {
		cleanup()
		return "", noop, err
	}
	return f.Name(), cleanup, nil
}

func fetch(url string, max int64) ([]byte, error) {
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err == nil && int64(len(body)) > max {
		err = fmt.Errorf("GET %s: larger than %d bytes", url, max)
	}
	return body, err
}

// sumFor finds the hex SHA-256 of name in a sha256sum-style listing.
func sumFor(sums, name string) (string, error) {
	sc := bufio.NewScanner(strings.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name && len(f[0]) == 64 {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("SHA256SUMS has no entry for %s", name)
}
