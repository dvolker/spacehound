package trash

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"spacehound/internal/fsid"
)

var stamp = time.Date(2026, 3, 4, 5, 6, 7, 0, time.Local)

func setup(t *testing.T) (src, trashDir string, tr *Trasher) {
	root := t.TempDir()
	trashDir = filepath.Join(root, "Trash")
	os.Mkdir(trashDir, 0o700)
	src = filepath.Join(root, "work", "report.pdf")
	os.MkdirAll(filepath.Dir(src), 0o755)
	if err := os.WriteFile(src, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	tr = &Trasher{
		DirFor: func(string, uint64) (string, error) { return trashDir, nil },
		Now:    func() time.Time { return stamp },
	}
	return
}

func TestCandidateName(t *testing.T) {
	cases := []struct {
		base string
		n    int
		want string
	}{
		{"a.txt", 0, "a.txt"},
		{"a.txt", 1, "a (SpaceHound 2026-03-04 05.06.07).txt"},
		{"a.txt", 3, "a (SpaceHound 2026-03-04 05.06.07 3).txt"},
		{"archive.tar.gz", 1, "archive.tar (SpaceHound 2026-03-04 05.06.07).gz"},
		{".env", 1, ".env (SpaceHound 2026-03-04 05.06.07)"},
		{"noext", 2, "noext (SpaceHound 2026-03-04 05.06.07 2)"},
	}
	for _, c := range cases {
		if got := CandidateName(c.base, stamp, c.n); got != c.want {
			t.Errorf("CandidateName(%q,%d) = %q, want %q", c.base, c.n, got, c.want)
		}
	}
}

func TestTrashCollisionSafe(t *testing.T) {
	src, trashDir, tr := setup(t)
	// Existing items with the first two candidate names must not be overwritten.
	os.WriteFile(filepath.Join(trashDir, "report.pdf"), []byte("older 1"), 0o644)
	os.WriteFile(filepath.Join(trashDir, CandidateName("report.pdf", stamp, 1)), []byte("older 2"), 0o644)

	id, _, err := fsid.Of(src)
	if err != nil {
		t.Fatal(err)
	}
	res, err := tr.Trash(src, id)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(trashDir, CandidateName("report.pdf", stamp, 2))
	if res.Dest != want {
		t.Errorf("dest = %s, want %s", res.Dest, want)
	}
	if _, err := os.Lstat(src); !os.IsNotExist(err) {
		t.Errorf("source still exists")
	}
	for name, content := range map[string]string{
		"report.pdf":                          "older 1",
		CandidateName("report.pdf", stamp, 1): "older 2",
		CandidateName("report.pdf", stamp, 2): "original",
	} {
		b, err := os.ReadFile(filepath.Join(trashDir, name))
		if err != nil || string(b) != content {
			t.Errorf("%s = %q (%v), want %q", name, b, err, content)
		}
	}
}

func TestTrashRefusesChangedFile(t *testing.T) {
	src, trashDir, tr := setup(t)
	id, _, _ := fsid.Of(src)
	os.WriteFile(src, []byte("modified contents"), 0o644)
	if _, err := tr.Trash(src, id); !errors.Is(err, fsid.ErrChanged) {
		t.Fatalf("want ErrChanged, got %v", err)
	}
	if _, err := os.Lstat(src); err != nil {
		t.Errorf("source must remain: %v", err)
	}
	entries, _ := os.ReadDir(trashDir)
	if len(entries) != 0 {
		t.Errorf("nothing should be in Trash, found %d", len(entries))
	}
}

func TestTrashUnavailableKeepsFile(t *testing.T) {
	src, _, tr := setup(t)
	tr.DirFor = func(string, uint64) (string, error) { return "", ErrNoTrash }
	id, _, _ := fsid.Of(src)
	if _, err := tr.Trash(src, id); !errors.Is(err, ErrNoTrash) {
		t.Fatalf("want ErrNoTrash, got %v", err)
	}
	if _, err := os.Lstat(src); err != nil {
		t.Errorf("file must never be deleted when Trash is unavailable: %v", err)
	}
}

func TestTrashRejectsRelativePath(t *testing.T) {
	_, _, tr := setup(t)
	if _, err := tr.Trash("relative/file", fsid.Identity{}); err == nil {
		t.Fatal("expected error")
	}
}
