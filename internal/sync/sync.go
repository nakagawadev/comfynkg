// Package sync copies models from this server to workers with rsync over SSH.
package sync

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"comfynkg/internal/run"
)

// StableFor: files modified more recently than this are still being written.
var StableFor = 60 * time.Second

// StateDir is where each type's state file lives (a systemd StateDirectory owned by the source owner).
func StateDir(typ string) string { return "/var/lib/comfynkg-sync-" + typ }

func statePath(typ string) string { return filepath.Join(StateDir(typ), "state.json") }

// Dest is one destination's state, as shown by `sync status`.
type Dest struct {
	Dest      string    `json:"dest"`
	State     string    `json:"state"` // idle, syncing, waiting, error
	LastStart time.Time `json:"last_start"`
	LastEnd   time.Time `json:"last_end"`
	LastSync  time.Time `json:"last_sync"` // last successful transfer
	Took      string    `json:"took"`
	Files     int       `json:"files"`
	Bytes     int64     `json:"bytes"`
	Error     string    `json:"error"`
	// While syncing: bytes the plan will copy, copied so far and rsync's current speed.
	Planned int64  `json:"planned,omitempty"`
	Copied  int64  `json:"copied,omitempty"`
	Speed   string `json:"speed,omitempty"`
}

type State struct {
	Type  string `json:"type"`
	Dests []Dest `json:"dests"`
}

// LoadState reads the last sync state of a model type. A state file that isn't a regular file is refused.
func LoadState(typ string) (State, error) {
	var s State
	if fi, err := os.Lstat(statePath(typ)); err == nil && !fi.Mode().IsRegular() {
		return State{Type: typ}, fmt.Errorf("%s is not a regular file", statePath(typ))
	}
	b, err := os.ReadFile(statePath(typ))
	if err != nil {
		return State{Type: typ}, err
	}
	return s, json.Unmarshal(b, &s)
}

// saveState writes the state file. As root it writes as the owner, so a symlink planted in the
// owner's state dir can't redirect the write.
func saveState(s State, owner *run.User, uid, gid int) error {
	b, _ := json.MarshalIndent(s, "", "  ")
	if os.Geteuid() == 0 && owner != nil {
		// The state dir belongs to the owner: as root, only create it (its parent is root's) and write as the owner.
		if err := os.Mkdir(StateDir(s.Type), 0o755); err == nil {
			if err := os.Lchown(StateDir(s.Type), uid, gid); err != nil {
				return err
			}
		} else if !os.IsExist(err) {
			return err
		}
		return run.WriteFile(owner, statePath(s.Type), b)
	}
	if err := os.MkdirAll(StateDir(s.Type), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(StateDir(s.Type), "state-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), statePath(s.Type))
}

// Guard refuses a source that is missing or empty, so `--delete` never wipes a worker
// because a disk wasn't mounted.
func Guard(source string) error {
	fi, err := os.Stat(source)
	if err != nil || !fi.IsDir() {
		return errors.New("source missing or empty")
	}
	found := errors.New("found")
	err = filepath.WalkDir(source, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			return found
		}
		return nil
	})
	if err != found {
		return errors.New("source missing or empty")
	}
	return nil
}

// Change is one line of an rsync plan.
type Change struct {
	Delete bool
	Path   string
	Size   int64
}

const outFormat = "--out-format=%i %l %n"

func rsyncArgs(extra ...string) []string {
	return append([]string{"-a", "--delete", "--partial-dir=.rsync-partial", "--delay-updates",
		"-e", "ssh -o BatchMode=yes -o ConnectTimeout=10"}, extra...)
}

// ParsePlan reads `rsync --dry-run --out-format='%i %l %n'` output: files to copy and paths to delete.
func ParsePlan(out string) []Change {
	var changes []Change
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(line, "*deleting"); ok { // "*deleting   0 path"
			size, path, _ := strings.Cut(strings.TrimLeft(rest, " "), " ")
			if _, err := strconv.Atoi(size); err != nil {
				path = strings.TrimLeft(rest, " ")
			}
			changes = append(changes, Change{Delete: true, Path: path})
			continue
		}
		f := strings.SplitN(line, " ", 3)
		if len(f) < 3 {
			continue
		}
		switch {
		case strings.HasPrefix(f[0], ">f") || strings.HasPrefix(f[0], "<f"):
			size, _ := strconv.ParseInt(f[1], 10, 64)
			changes = append(changes, Change{Path: f[2], Size: size})
		}
	}
	return changes
}

// Plan asks rsync what it would do for one destination.
func Plan(owner *run.User, source, dest string) ([]Change, error) {
	out, err := run.Output(run.Command(owner, "rsync", rsyncArgs("--dry-run", outFormat, slash(source), dest)...))
	if err != nil {
		return nil, err
	}
	return ParsePlan(out), nil
}

func slash(p string) string { return strings.TrimRight(p, "/") + "/" }

// Unstable lists planned files changed within StableFor (only those, not the whole tree).
func Unstable(source string, changes []Change) []string {
	var busy []string
	for _, c := range changes {
		if c.Delete {
			continue
		}
		fi, err := os.Stat(filepath.Join(source, c.Path))
		if err == nil && time.Since(fi.ModTime()) < StableFor {
			busy = append(busy, c.Path)
		}
	}
	return busy
}

// Round runs one sync of a type to every destination and saves the state.
// log gets one line per destination.
func Round(typ, source string, dests []string, owner *run.User, uid, gid int, log func(string)) State {
	prev, _ := LoadState(typ)
	byDest := map[string]Dest{}
	for _, d := range prev.Dests {
		byDest[d.Dest] = d
	}
	s := State{Type: typ}
	for _, dest := range dests {
		d := byDest[dest]
		d.Dest = dest
		d.LastStart, d.Error = time.Now(), ""
		set := func(state, msg string) {
			d.State, d.LastEnd = state, time.Now()
			d.Planned, d.Copied, d.Speed = 0, 0, ""
			if state == "error" {
				d.Error = msg
			}
			log(fmt.Sprintf("> %s -> %s %s", typ, dest, strings.TrimSpace(state+" "+msg)))
		}
		if err := Guard(source); err != nil {
			set("error", err.Error())
			s.Dests = append(s.Dests, d)
			continue
		}
		changes, err := Plan(owner, source, dest)
		switch {
		case err != nil:
			set("error", firstLine(err.Error()))
		case len(changes) == 0:
			set("idle", "")
		default:
			if busy := Unstable(source, changes); len(busy) > 0 {
				set("waiting", fmt.Sprintf("(%d files still changing)", len(busy)))
				break
			}
			d.State, d.Files, d.Planned = "syncing", 0, 0
			for _, c := range changes {
				if !c.Delete {
					d.Files++
					d.Planned += c.Size
				}
			}
			saveMid := func() {
				s.Dests = append(s.Dests, d)
				_ = saveState(withDests(s, prev, dests), owner, uid, gid)
				s.Dests = s.Dests[:len(s.Dests)-1]
			}
			saveMid()
			start, saved := time.Now(), time.Now()
			out, err := transfer(owner, source, dest, func(copied int64, speed string) {
				d.Copied, d.Speed = copied, speed
				if time.Since(saved) >= ProgressEvery {
					saveMid()
					saved = time.Now()
				}
			})
			if err != nil {
				set("error", firstLine(err.Error()))
				break
			}
			d.Files, d.Bytes = parseStats(out)
			d.LastSync, d.Took = time.Now(), time.Since(start).Round(time.Second).String()
			set("idle", fmt.Sprintf("(%d files, %d bytes in %s)", d.Files, d.Bytes, d.Took))
		}
		s.Dests = append(s.Dests, d)
	}
	_ = saveState(s, owner, uid, gid)
	return s
}

// ProgressEvery is how often a running transfer writes its progress to the state file.
var ProgressEvery = 5 * time.Second

// transfer runs the real rsync with --stats and --info=progress2, calling progress with the bytes
// copied so far and rsync's speed as they come (progress lines end in \r). It returns the whole
// output, for parseStats.
func transfer(owner *run.User, source, dest string, progress func(int64, string)) (string, error) {
	cmd := run.Command(owner, "rsync", rsyncArgs("--stats", "--info=progress2", slash(source), dest)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	var all strings.Builder
	sc := bufio.NewScanner(pipe)
	sc.Split(splitLines)
	for sc.Scan() {
		line := sc.Text()
		all.WriteString(line + "\n")
		if n, speed, ok := parseProgress(line); ok {
			progress(n, speed)
		}
	}
	if err := cmd.Wait(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return all.String(), fmt.Errorf("rsync: %w: %s", err, msg)
		}
		return all.String(), fmt.Errorf("rsync: %w", err)
	}
	return all.String(), nil
}

// splitLines is bufio.ScanLines that also ends a line at \r, the way rsync redraws its progress.
func splitLines(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// parseProgress reads an --info=progress2 line: "  1,048,576  40%    9.17MB/s    0:00:01 (xfr#1, to-chk=1/2)".
func parseProgress(line string) (copied int64, speed string, ok bool) {
	f := strings.Fields(line)
	if len(f) < 3 || !strings.HasSuffix(f[1], "%") || !strings.HasSuffix(f[2], "/s") {
		return 0, "", false
	}
	n, err := strconv.ParseInt(strings.ReplaceAll(f[0], ",", ""), 10, 64)
	if err != nil {
		return 0, "", false
	}
	return n, f[2], true
}

// withDests keeps previous entries for destinations not processed yet, for a consistent status mid-round.
func withDests(cur, prev State, dests []string) State {
	out := State{Type: cur.Type, Dests: append([]Dest{}, cur.Dests...)}
	done := map[string]bool{}
	for _, d := range cur.Dests {
		done[d.Dest] = true
	}
	for _, d := range prev.Dests {
		if !done[d.Dest] {
			out.Dests = append(out.Dests, d)
		}
	}
	return out
}

// parseStats reads the files and bytes transferred from `rsync --stats` output.
func parseStats(out string) (files int, bytes int64) {
	for _, l := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		n := strings.ReplaceAll(strings.Fields(strings.TrimSpace(v) + " 0")[0], ",", "")
		switch strings.TrimSpace(k) {
		case "Number of regular files transferred":
			files, _ = strconv.Atoi(n)
		case "Total transferred file size":
			bytes, _ = strconv.ParseInt(n, 10, 64)
		}
	}
	return files, bytes
}

func firstLine(s string) string { l, _, _ := strings.Cut(s, "\n"); return l }

// IsLocal reports whether a destination `user@host:path` points at this machine.
func IsLocal(dest string) (bool, error) {
	hostPart, _, ok := strings.Cut(dest, ":")
	if !ok {
		return false, fmt.Errorf("%q is not user@host:/path", dest)
	}
	host := hostPart
	if _, h, found := strings.Cut(hostPart, "@"); found {
		host = h
	}
	name, _ := os.Hostname()
	short, _, _ := strings.Cut(name, ".")
	if host == "localhost" || host == name || host == short {
		return true, nil
	}
	ips, err := net.LookupHost(host)
	if err != nil {
		return false, nil // can't resolve: rsync will report it
	}
	addrs, _ := net.InterfaceAddrs()
	for _, ip := range ips {
		if net.ParseIP(ip).IsLoopback() {
			return true, nil
		}
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.Equal(net.ParseIP(ip)) {
				return true, nil
			}
		}
	}
	return false, nil
}
