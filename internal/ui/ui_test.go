package ui

import (
	"bytes"
	"testing"
)

func TestTableAlignsColoredCells(t *testing.T) {
	color = true
	defer func() { color = false }()
	var buf bytes.Buffer
	Out = &buf
	Table([][]string{{"NAME", "HEALTH"}, {Cyan("comfy_a"), Green("ok")}, {"b", Red("down")}})
	want := "\x1b[2mNAME\x1b[0m     \x1b[2mHEALTH\x1b[0m\n" +
		"\x1b[36mcomfy_a\x1b[0m  \x1b[32mok\x1b[0m\n" +
		"b        \x1b[31mdown\x1b[0m\n"
	if got := buf.String(); got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
}

func TestPrefixColors(t *testing.T) {
	color = true
	defer func() { color = false }()
	if got := Prefix("master", 7859, "comfy_4"); got != "> \x1b[33mmaster\x1b[0m\x1b[2m:7859\x1b[0m (\x1b[36mcomfy_4\x1b[0m)" {
		t.Errorf("Prefix = %q", got)
	}
	color = false
	if got := Prefix("master", 7859, "comfy_4"); got != "> master:7859 (comfy_4)" {
		t.Errorf("plain Prefix = %q", got)
	}
}
