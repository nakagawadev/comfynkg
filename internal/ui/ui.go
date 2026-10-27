// Package ui prints comfynkg's messages: the `> host:port (name)` prefix, tables and colors.
package ui

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	Out   io.Writer = os.Stdout
	color           = isTTY(os.Stdout)
)

// isTTY says whether to color: a terminal, or CLICOLOR_FORCE (set by `status` when it asks other hosts over ssh).
func isTTY(f *os.File) bool {
	fi, err := f.Stat()
	tty := err == nil && fi.Mode()&os.ModeCharDevice != 0
	return (tty || os.Getenv("CLICOLOR_FORCE") != "") && os.Getenv("NO_COLOR") == ""
}

// Colored reports whether output is colored.
func Colored() bool { return color }

func paint(code, s string) string {
	if !color {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func Green(s string) string  { return paint("32", s) }
func Red(s string) string    { return paint("31", s) }
func Yellow(s string) string { return paint("33", s) }
func Cyan(s string) string   { return paint("36", s) }
func Dim(s string) string    { return paint("2", s) }

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

var ansi = regexp.MustCompile("\x1b\\[[0-9;]*m")

// width is what a cell takes on screen: color codes don't count.
func width(s string) int { return utf8.RuneCountInString(ansi.ReplaceAllString(s, "")) }

// Table prints rows aligned in columns, two spaces apart; the first row is the header.
// Cells may be colored: alignment uses the visible width.
func Table(rows [][]string) {
	var widths []int
	for _, r := range rows {
		for i, c := range r {
			if i == len(widths) {
				widths = append(widths, 0)
			}
			widths[i] = max(widths[i], width(c))
		}
	}
	for _, r := range rows {
		var b strings.Builder
		for i, c := range r {
			b.WriteString(c)
			if i < len(r)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-width(c)+2))
			}
		}
		fmt.Fprintln(Out, b.String())
	}
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
