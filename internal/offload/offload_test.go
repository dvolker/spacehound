package offload

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"spacehound/internal/diskspace"
	"spacehound/internal/fsid"
	"spacehound/internal/units"
	"spacehound/internal/volumes"
)

var day = time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)

func TestDestPath(t *testing.T) {
	cases := []struct {
		src, want string
	}{
		{"/Users/me/Movies/a.mov", "/Volumes/X/SpaceHound Archive/host/2026-05-06/Movies/a.mov"},
		{"/opt/data/big.iso", "/Volumes/X/SpaceHound Archive/host/2026-05-06/_outside_home/opt/data/big.iso"},
	}
	for _, c := range cases {
		got, err := DestPath("/Volumes/X", "host", day, "/Users/me", c.src)
		if err != nil || got != c.want {
			t.Errorf("DestPath(%q) = %q, %v; want %q", c.src, got, err, c.want)
		}
	}
	for _, bad := range []string{"relative/a", "/Users/me/../../etc/passwd", "/Users/me/a/./b", "/Users/me//a", "/Users/me"} {
		if got, err := DestPath("/Volumes/X", "host", day, "/Users/me", bad); err == nil && bad != "/Users/me" {
			t.Errorf("DestPath(%q) accepted: %q", bad, got)
		}
	}
	if _, err := DestPath("/", "host", day, "/Users/me", "/Users/me/a"); err == nil {
		t.Error("root volume accepted")
	}
	// A hostile hostname cannot climb out of the archive.
	got, err := DestPath("/Volumes/X", "../../etc", day, "/Users/me", "/Users/me/a")
	if err != nil || !strings.HasPrefix(got, "/Volumes/X/SpaceHound Archive/") || strings.Contains(got, "/../") {
		t.Errorf("hostile host: %q %v", got, err)
	}
	for in, want := range map[string]string{"..": "Mac", "": "Mac", "a/b:c": "a_b_c", " mac ": "mac"} {
		if got := SanitizeName(in); got != want {
			t.Errorf("SanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

type env struct {
	home, vol, src string
	id             fsid.Identity
	o              *Offloader
	req            Request
}

func setup(t *testing.T, content []byte) *env {
	t.Helper()
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	e := &env{home: filepath.Join(root, "home"), vol: filepath.Join(root, "Volumes", "Ext")}
	os.MkdirAll(filepath.Join(e.home, "Movies"), 0o755)
	os.MkdirAll(e.vol, 0o755)
	e.src = filepath.Join(e.home, "Movies", "film.mov")
	if err := os.WriteFile(e.src, content, 0o644); err != nil {
		t.Fatal(err)
	}
	mt := day.Add(-48 * time.Hour)
	os.Chtimes(e.src, mt, mt)
	var err error
	if e.id, _, err = fsid.Of(e.src); err != nil {
		t.Fatal(err)
	}
	e.o = &Offloader{
		Home: e.home, Host: "testhost", Now: func() time.Time { return day },
		Copy: GoCopy, AllowSameDevice: true, PollInterval: time.Millisecond,
		Statfs: func(p string) (diskspace.Info, error) {
			return diskspace.Info{MountPoint: p, FSType: "apfs", Flags: diskspace.FlagLocal, Free: 10 * units.GiB, Total: 20 * units.GiB}, nil
		},
	}
	vol := volumes.Volume{Path: e.vol, Name: "Ext", Suitable: true, Writable: true, Free: 10 * units.GiB, Total: 20 * units.GiB, Dev: e.id.Dev}
	e.req = Request{Source: e.src, Identity: e.id, Volume: vol}
	return e
}

func data(n int) []byte { return bytes.Repeat([]byte("spacehound-"), n) }

func TestOffloadSuccessKeepsSource(t *testing.T) {
	e := setup(t, data(50000))
	var phases []string
	res, err := e.o.Offload(context.Background(), e.req, func(p string, d, tot int64) { phases = append(phases, p) })
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(e.vol, ArchiveDir, "testhost", "2026-05-06", "Movies", "film.mov")
	if !res.Verified || res.Dest != want || res.SHA256 == "" {
		t.Fatalf("bad result %+v", res)
	}
	got, _ := os.ReadFile(res.Dest)
	if !bytes.Equal(got, data(50000)) {
		t.Fatal("copy content differs")
	}
	if _, err := os.Stat(e.src); err != nil {
		t.Fatal("Offload must never remove the source")
	}
	ents, _ := os.ReadDir(filepath.Dir(res.Dest))
	if len(ents) != 1 {
		t.Errorf("leftover partial files: %v", ents)
	}
	if len(phases) == 0 || phases[len(phases)-1] != "verifying" {
		t.Errorf("progress phases: %v", phases)
	}
	if err := RemoveSource(res); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(e.src); !os.IsNotExist(err) {
		t.Error("source still present after confirmed removal")
	}
}

func TestOffloadRefusesExistingDest(t *testing.T) {
	e := setup(t, data(10))
	dest, _ := e.o.Plan(e.req)
	os.MkdirAll(filepath.Dir(dest), 0o755)
	os.WriteFile(dest, []byte("precious"), 0o644)
	_, err := e.o.Offload(context.Background(), e.req, nil)
	if !errors.Is(err, ErrDestExists) {
		t.Fatalf("want ErrDestExists, got %v", err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "precious" {
		t.Fatal("existing destination was overwritten")
	}
}

func TestOffloadRefusesDestAppearingDuringCopy(t *testing.T) {
	e := setup(t, data(10))
	dest, _ := e.o.Plan(e.req)
	e.o.Copy = func(ctx context.Context, src, dst string) error {
		os.WriteFile(dest, []byte("raced"), 0o644)
		return GoCopy(ctx, src, dst)
	}
	res, err := e.o.Offload(context.Background(), e.req, nil)
	if !errors.Is(err, ErrDestExists) || res.Verified {
		t.Fatalf("want ErrDestExists, got %v", err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "raced" {
		t.Fatal("destination overwritten")
	}
	if res.Partial == "" {
		t.Error("partial copy not reported")
	}
}

func TestOffloadRefusesSymlinkInDestination(t *testing.T) {
	e := setup(t, data(10))
	elsewhere := t.TempDir()
	os.Symlink(elsewhere, filepath.Join(e.vol, ArchiveDir))
	_, err := e.o.Offload(context.Background(), e.req, nil)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("want symlink refusal, got %v", err)
	}
	if ents, _ := os.ReadDir(elsewhere); len(ents) != 0 {
		t.Fatal("wrote through a symlink")
	}
}

func TestOffloadVerificationFailures(t *testing.T) {
	cases := map[string]CopyFunc{
		"corrupt": func(ctx context.Context, src, dst string) error {
			b, _ := os.ReadFile(src)
			b[len(b)/2] ^= 1
			return os.WriteFile(dst, b, 0o644)
		},
		"short": func(ctx context.Context, src, dst string) error {
			b, _ := os.ReadFile(src)
			return os.WriteFile(dst, b[:len(b)-1], 0o644)
		},
		"symlink": func(ctx context.Context, src, dst string) error { return os.Symlink(src, dst) },
		"fails": func(ctx context.Context, src, dst string) error {
			os.WriteFile(dst, []byte("x"), 0o600)
			return errors.New("disk ejected")
		},
	}
	for name, cp := range cases {
		e := setup(t, data(1000))
		e.o.Copy = cp
		res, err := e.o.Offload(context.Background(), e.req, nil)
		if err == nil || res.Verified {
			t.Errorf("%s: accepted bad copy", name)
			continue
		}
		if res.Partial == "" || !strings.Contains(err.Error(), "original was kept") {
			t.Errorf("%s: partial not reported: %v", name, err)
		}
		if _, err := os.Lstat(res.Dest); err == nil {
			t.Errorf("%s: unverified copy published", name)
		}
		if b, _ := os.ReadFile(e.src); !bytes.Equal(b, data(1000)) {
			t.Errorf("%s: source damaged", name)
		}
		if err := RemoveSource(res); err == nil {
			t.Errorf("%s: RemoveSource allowed after failed verification", name)
		}
	}
}

func TestOffloadSourceChanged(t *testing.T) {
	e := setup(t, data(100))
	os.WriteFile(e.src, data(101), 0o644)
	if _, err := e.o.Offload(context.Background(), e.req, nil); !errors.Is(err, fsid.ErrChanged) {
		t.Fatalf("want ErrChanged before copy, got %v", err)
	}

	e = setup(t, data(100))
	e.o.Copy = func(ctx context.Context, src, dst string) error {
		if err := GoCopy(ctx, src, dst); err != nil {
			return err
		}
		later := day.Add(time.Hour)
		return os.Chtimes(src, later, later) // source touched mid-offload
	}
	res, err := e.o.Offload(context.Background(), e.req, nil)
	if err == nil || res.Verified {
		t.Fatal("offload accepted a source modified during copy")
	}
}

func TestOffloadSpaceAndDevice(t *testing.T) {
	e := setup(t, data(100))
	e.o.Statfs = func(p string) (diskspace.Info, error) {
		return diskspace.Info{MountPoint: p, Flags: diskspace.FlagLocal, Free: 50 * units.MiB, Total: units.GiB}, nil
	}
	if _, err := e.o.Offload(context.Background(), e.req, nil); err == nil || !strings.Contains(err.Error(), "headroom") {
		t.Fatalf("want free-space refusal, got %v", err)
	}
	e = setup(t, data(100))
	e.o.AllowSameDevice = false
	if _, err := e.o.Offload(context.Background(), e.req, nil); err == nil || !strings.Contains(err.Error(), "same disk") {
		t.Fatalf("want same-device refusal, got %v", err)
	}
}

func TestRemoveSourceRechecksCopy(t *testing.T) {
	e := setup(t, data(100))
	res, err := e.o.Offload(context.Background(), e.req, nil)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(res.Dest, data(99), 0o644) // copy tampered after verification
	if err := RemoveSource(res); err == nil {
		t.Fatal("removed source although verified copy changed")
	}
	if _, err := os.Stat(e.src); err != nil {
		t.Fatal("source lost")
	}
}

func TestDittoCopy(t *testing.T) {
	if _, err := os.Stat(Ditto); err != nil {
		t.Skip("ditto not available")
	}
	e := setup(t, data(2000))
	e.o.Copy = DittoCopy
	res, err := e.o.Offload(context.Background(), e.req, nil)
	if err != nil || !res.Verified {
		t.Fatalf("ditto offload: %v", err)
	}
	fi, _ := os.Stat(res.Dest)
	if !fi.ModTime().Equal(e.id.ModTime()) {
		t.Errorf("ditto did not preserve mtime: %v vs %v", fi.ModTime(), e.id.ModTime())
	}
}
