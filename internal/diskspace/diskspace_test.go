package diskspace

import (
	"strings"
	"testing"
)

func TestStatfsTempDir(t *testing.T) {
	info, err := Statfs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if info.Total <= 0 || info.Free < 0 || info.MountPoint == "" || info.FSType == "" {
		t.Fatalf("implausible info: %+v", info)
	}
}

func TestShortfall(t *testing.T) {
	if s := Shortfall(100, 1100, 1000); s != "" {
		t.Errorf("full recovery flagged: %q", s)
	}
	if s := Shortfall(100, 150, 0); s != "" {
		t.Errorf("zero expectation flagged: %q", s)
	}
	s := Shortfall(100, 110, 1000)
	if s == "" || !strings.Contains(s, "tmutil listlocalsnapshots") || !strings.Contains(s, "lsof +L1") {
		t.Fatalf("missing guidance: %q", s)
	}
	for _, bad := range []string{"deletelocalsnapshots", "thinlocalsnapshots", "deleteSnapshot"} {
		if strings.Contains(s, bad) {
			t.Errorf("guidance must stay read-only, found %q", bad)
		}
	}
}
