// Package ui prints comfynkg's messages: the `> host:port (name)` prefix, tables and colors.
package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"
)

var (
	Out   io.Writer = os.Stdout
	color           = isTTY(os.Stdout)
)

func isTTY(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0 && os.Getenv("NO_COLOR") == ""
}

func paint(code, s string) string {
	if !color {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func Green(s string) string  { return paint("32", s) }
func Red(s string) string    { return paint("31", s) }
func Yellow(s string) string { return paint("33", s) }

// Prefix is `> host:port (name)`.
func Prefix(host string, port int, name string) string {
	return fmt.Sprintf("> %s:%d (%s)", host, port, name)
}

// Line prints one message about an instance; prefix is "> host:port (name)".
func Line(prefix, format string, a ...any) {
	fmt.Fprintf(Out, "%s %s\n", prefix, fmt.Sprintf(format, a...))
}

func Printf(format string, a ...any) { fmt.Fprintf(Out, format, a...) }

// Warn prints a warning to stderr; the command goes on.
func Warn(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "%s %s\n", Yellow("warning:"), fmt.Sprintf(format, a...))
}

// Table prints rows aligned in columns; the first row is the header.
func Table(rows [][]string) {
	tw := tabwriter.NewWriter(Out, 0, 0, 2, ' ', 0)
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	tw.Flush()
}

// Dur formats a duration like 18s, 2m41s or 1h2m.
func Dur(d time.Duration) string {
	if d < time.Second {
		return d.Round(time.Millisecond).String()
	}
	return d.Round(time.Second).String()
}

// Bytes formats sizes like 6.1G.
func Bytes(n float64) string {
	units := []string{"B", "K", "M", "G", "T"}
	i := 0
	for n >= 1024 && i < len(units)-1 {
		n /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%.0fB", n)
	}
	return fmt.Sprintf("%.1f%s", n, units[i])
}
