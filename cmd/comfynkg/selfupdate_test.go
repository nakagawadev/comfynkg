package main

import "testing"

func TestSelfUpdateHelpers(t *testing.T) {
	sums := "aaaa  other\n" + "ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef0123456789  comfynkg-linux-arm64\n"
	if got, err := sumFor(sums, "comfynkg-linux-arm64"); err != nil || got != "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789" {
		t.Errorf("sumFor = %q, %v", got, err)
	}
	if _, err := sumFor(sums, "comfynkg-linux-amd64"); err == nil {
		t.Error("missing entry must fail")
	}
	for arg, isTag := range map[string]bool{"v0.1.0": true, "v1.2.3-rc.1": true, "/tmp/comfynkg": false, "./v0.1.0": false, "v0.1": false} {
		if tagRe.MatchString(arg) != isTag {
			t.Errorf("%q: tag = %v", arg, !isTag)
		}
	}
	if u := releaseURL("", "SHA256SUMS"); u != releases+"/latest/download/SHA256SUMS" {
		t.Errorf("latest url %s", u)
	}
}
