//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

// Self-update story: a binary that isn't comfynkg is refused, the same version is a no-op, a newer
// one replaces it.
func TestSelfUpdate(t *testing.T) {
	sh(t, "cp /usr/local/bin/comfynkg /tmp/comfynkg-new && printf '#!/bin/sh\\necho nope\\n' > /tmp/not-comfynkg && chmod +x /tmp/not-comfynkg")
	if r := nkg(t, "self-update", "/tmp/not-comfynkg"); r.code != 1 || !strings.Contains(r.out, "is not a working comfynkg") {
		t.Fatalf("bad binary: exit %d", r.code)
	}
	contains(t, mustOK(t, "self-update", "/tmp/comfynkg-new"), "is already installed")
	// a "newer" build: a wrapper that reports another version and runs the real binary otherwise
	sh(t, `printf '#!/bin/sh\n[ "$1" = version ] && echo "comfynkg v9.9.9" && exit 0\nexec /tmp/comfynkg-new "$@"\n' > /tmp/comfynkg-v9 && chmod +x /tmp/comfynkg-v9`)
	contains(t, mustOK(t, "self-update", "/tmp/comfynkg-v9"), "-> v9.9.9", "running instances keep going")
	contains(t, mustOK(t, "version"), "comfynkg v9.9.9")
	sh(t, "install -m 755 /tmp/comfynkg-new /usr/local/bin/comfynkg") // the wrapper runs the real binary, which sees its own version
	if strings.Contains(mustOK(t, "version"), "v9.9.9") {
		t.Fatal("not restored")
	}
}
