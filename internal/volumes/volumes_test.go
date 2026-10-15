package volumes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spacehound/internal/diskspace"
	"spacehound/internal/units"
)

func good() Candidate {
	return Candidate{
		Path:  "/Volumes/Archive",
		IsDir: true,
		Dev:   42,
		Info: diskspace.Info{MountPoint: "/Volumes/Archive", FSType: "apfs", Flags: diskspace.FlagLocal,
			Free: 500 * units.GiB, Total: 1000 * units.GiB},
		Writable: true,
	}
}

func TestAssess(t *testing.T) {
	internal := map[uint64]bool{1: true}
	cases := []struct {
		name   string
		mutate func(*Candidate)
		reason string // substring expected in reasons; "" = suitable
	}{
		{"ok", func(*Candidate) {}, ""},
		{"symlink", func(c *Candidate) { c.IsSymlink, c.IsDir = true, false }, "symlink"},
		{"plain dir", func(c *Candidate) { c.Info.MountPoint = "/System/Volumes/Data"; c.Dev = 1 }, "not a mount point"},
		{"internal", func(c *Candidate) { c.Dev = 1 }, "internal disk"},
		{"rootfs", func(c *Candidate) { c.Info.Flags |= diskspace.FlagRootFS }, "internal disk"},
		{"read-only", func(c *Candidate) { c.Info.Flags |= diskspace.FlagReadOnly }, "read-only"},
		{"no write perm", func(c *Candidate) { c.Writable = false }, "not writable"},
		{"hidden", func(c *Candidate) { c.Info.Flags |= diskspace.FlagDontBrowse }, "hidden"},
		{"network", func(c *Candidate) { c.Info.FSType = "smbfs"; c.Info.Flags &^= diskspace.FlagLocal }, "network"},
		{"optical", func(c *Candidate) { c.Info.FSType = "cd9660" }, "optical"},
		{"tm hfs", func(c *Candidate) { c.RootNames = []string{"Backups.backupdb"} }, "Time Machine"},
		{"tm apfs", func(c *Candidate) { c.RootNames = []string{"2026-01-02-030405.previous"} }, "Time Machine"},
		{"tm marker", func(c *Candidate) { c.RootNames = []string{".com.apple.timemachine.donotpresent"} }, "Time Machine"},
	}
	for _, tc := range cases {
		c := good()
		tc.mutate(&c)
		v := Assess(c, internal)
		if tc.reason == "" {
			if !v.Suitable {
				t.Errorf("%s: unexpectedly unsuitable: %v", tc.name, v.Reasons)
			}
			continue
		}
		if v.Suitable || !strings.Contains(strings.Join(v.Reasons, ";"), tc.reason) {
			t.Errorf("%s: suitable=%v reasons=%v, want %q", tc.name, v.Suitable, v.Reasons, tc.reason)
		}
	}
}

func TestCanHold(t *testing.T) {
	v := Assess(good(), nil)
	if err := v.CanHold(10 * units.GiB); err != nil {
		t.Errorf("should fit: %v", err)
	}
	v.Free = 10*units.GiB + units.MiB // less than 1% headroom of 1000 GiB
	if err := v.CanHold(10 * units.GiB); err == nil || !strings.Contains(err.Error(), "headroom") {
		t.Errorf("headroom not enforced: %v", err)
	}
	fat := good()
	fat.Info.FSType = "msdos"
	fv := Assess(fat, nil)
	if err := fv.CanHold(5 * units.GiB); err == nil {
		t.Error("FAT32 accepted a 5 GiB file")
	}
	bad := Assess(func() Candidate { c := good(); c.Info.Flags |= diskspace.FlagReadOnly; return c }(), nil)
	if err := bad.CanHold(1); err == nil {
		t.Error("unsuitable volume accepted")
	}
}

func TestListerFakeRoot(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "Drive"), 0o755)
	os.Mkdir(filepath.Join(root, "Backup"), 0o755)
	os.Mkdir(filepath.Join(root, "Backup", "Backups.backupdb"), 0o755)
	os.Mkdir(filepath.Join(root, ".hidden"), 0o755)
	os.Symlink("/", filepath.Join(root, "Macintosh HD"))
	l := &Lister{
		Root: root,
		Statfs: func(p string) (diskspace.Info, error) {
			return diskspace.Info{MountPoint: p, FSType: "apfs", Flags: diskspace.FlagLocal, Free: 100 * units.GiB, Total: 200 * units.GiB}, nil
		},
		Writable: func(string) bool { return true },
	}
	vols, err := l.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(vols) != 3 {
		t.Fatalf("got %d volumes: %+v", len(vols), vols)
	}
	if vols[0].Name != "Drive" || !vols[0].Suitable {
		t.Errorf("first volume should be suitable Drive: %+v", vols[0])
	}
	for _, v := range vols[1:] {
		if v.Suitable {
			t.Errorf("%s should be unsuitable", v.Name)
		}
	}
}
