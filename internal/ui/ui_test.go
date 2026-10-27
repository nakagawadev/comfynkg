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
	Table([][]string{{Dim("NAME"), Dim("HEALTH")}, {Cyan("comfy_a"), Green("ok")}, {"b", Red("down")}})
	want := "\x1b[2mNAME\x1b[0m     \x1b[2mHEALTH\x1b[0m\n" +
		"\x1b[36mcomfy_a\x1b[0m  \x1b[32mok\x1b[0m\n" +
		"b        \x1b[31mdown\x1b[0m\n"
	if got := buf.String(); got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
}
