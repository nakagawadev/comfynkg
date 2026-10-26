// Package run is the single place where comfynkg calls other programs or HTTP endpoints,
// so --verbose can show every call and tests can swap it.
package run

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

var (
	Verbose bool
	Log     io.Writer = os.Stderr
)

// User runs commands as this login (via runuser) when set; empty means the current user.
type User struct {
	Name string
	Home string
}

// Command builds a command, wrapped in runuser when u is set.
// Inherited VIRTUAL_ENV/CONDA_PREFIX are always dropped so tools pick the workspace venv.
func Command(u *User, name string, args ...string) *exec.Cmd {
	env := []string{}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "VIRTUAL_ENV=") || strings.HasPrefix(kv, "CONDA_PREFIX=") {
			continue
		}
		if u != nil && (strings.HasPrefix(kv, "HOME=") || strings.HasPrefix(kv, "USER=") || strings.HasPrefix(kv, "LOGNAME=")) {
			continue
		}
		env = append(env, kv)
	}
	var cmd *exec.Cmd
	if u != nil {
		env = append(env, "HOME="+u.Home, "USER="+u.Name, "LOGNAME="+u.Name)
		cmd = exec.Command("runuser", append([]string{"-u", u.Name, "--", name}, args...)...)
	} else {
		cmd = exec.Command(name, args...)
	}
	cmd.Env = env
	return cmd
}

func show(cmd *exec.Cmd) string { return strings.Join(cmd.Args, " ") }

func logf(format string, a ...any) {
	if Verbose {
		fmt.Fprintf(Log, "  $ "+format+"\n", a...)
	}
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return -1
}

// Output runs cmd and returns stdout. stderr is included in the error.
func Output(cmd *exec.Cmd) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	start := time.Now()
	err := cmd.Run()
	logf("%s -> %d (%s)", show(cmd), exitCode(err), dur(start))
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		return stdout.String(), fmt.Errorf("%s: %w: %s", show(cmd), err, lastLines(msg, 5))
	}
	return stdout.String(), nil
}

// Stream runs cmd and writes each output line (stdout and stderr) prefixed.
func Stream(cmd *exec.Cmd, w io.Writer, prefix string) error {
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s: %w", show(cmd), err)
	}
	done := make(chan struct{})
	var tail []string
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			fmt.Fprintf(w, "%s  %s\n", prefix, line)
			tail = append(tail, line)
			if len(tail) > 5 {
				tail = tail[1:]
			}
		}
		close(done)
	}()
	err := cmd.Wait()
	pw.Close()
	<-done
	logf("%s -> %d (%s)", show(cmd), exitCode(err), dur(start))
	if err != nil {
		return fmt.Errorf("%s: %w", show(cmd), err)
	}
	return nil
}

// Interactive runs cmd attached to the terminal.
func Interactive(cmd *exec.Cmd) error {
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	logf("%s", show(cmd))
	return cmd.Run()
}

var client = &http.Client{Timeout: 5 * time.Second}

// HTTP does one request and returns status and body. Errors are transport errors only.
func HTTP(ctx context.Context, method, url string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return 0, nil, err
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		logf("%s %s -> error: %v (%s)", method, url, err, dur(start))
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	logf("%s %s -> %d (%s)", method, url, resp.StatusCode, dur(start))
	return resp.StatusCode, body, err
}

func dur(start time.Time) string { return time.Since(start).Round(10 * time.Millisecond).String() }

func lastLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// WriteFile writes data to path as u (atomically: a temp file next to it, then mv).
// Root must never write inside a user's folders itself: a planted symlink would
// redirect the write. As u, a symlink can only reach what u can write.
func WriteFile(u *User, path string, data []byte) error {
	cmd := Command(u, "sh", "-c", `set -e; t="$1.comfynkg-tmp"; cat > "$t"; mv -f "$t" "$1"`, "sh", path)
	cmd.Stdin = bytes.NewReader(data)
	_, err := Output(cmd)
	return err
}

// CreateFile is WriteFile that leaves an existing path alone; it reports whether it wrote.
func CreateFile(u *User, path string, data []byte) (bool, error) {
	cmd := Command(u, "sh", "-c", `set -e; if [ -e "$1" ] || [ -L "$1" ]; then echo exists; exit 0; fi; cat > "$1"`, "sh", path)
	cmd.Stdin = bytes.NewReader(data)
	out, err := Output(cmd)
	return err == nil && strings.TrimSpace(out) != "exists", err
}

// ReadFile reads path as u, so a symlink can't expose files u can't read.
func ReadFile(u *User, path string) ([]byte, error) {
	out, err := Output(Command(u, "cat", "--", path))
	return []byte(out), err
}
