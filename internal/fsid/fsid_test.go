package fsid

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func mustWrite(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRevalidate(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.bin")
	mustWrite(t, p, "hello")
	old := time.Date(2023, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	id, _, err := Of(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := Revalidate(p, id); err != nil {
		t.Fatalf("unchanged file failed revalidation: %v", err)
	}

	t.Run("mtime", func(t *testing.T) {
		newer := old.Add(time.Hour)
		if err := os.Chtimes(p, newer, newer); err != nil {
			t.Fatal(err)
		}
		defer os.Chtimes(p, old, old)
		if err := Revalidate(p, id); !errors.Is(err, ErrChanged) {
			t.Fatalf("want ErrChanged, got %v", err)
		}
	})

	t.Run("size", func(t *testing.T) {
		mustWrite(t, p, "hello world")
		os.Chtimes(p, old, old)
		if err := Revalidate(p, id); !errors.Is(err, ErrChanged) {
			t.Fatalf("want ErrChanged, got %v", err)
		}
		mustWrite(t, p, "hello")
		os.Chtimes(p, old, old)
	})

	t.Run("replaced", func(t *testing.T) {
		tmp := filepath.Join(dir, "other")
		mustWrite(t, tmp, "hello")
		os.Chtimes(tmp, old, old)
		if err := os.Rename(tmp, p); err != nil {
			t.Fatal(err)
		}
		if err := Revalidate(p, id); !errors.Is(err, ErrChanged) {
			t.Fatalf("want ErrChanged for replaced inode, got %v", err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		id2, _, _ := Of(p)
		link := filepath.Join(dir, "link")
		if err := os.Symlink(p, link); err != nil {
			t.Fatal(err)
		}
		if err := Revalidate(link, id2); !errors.Is(err, ErrChanged) {
			t.Fatalf("want ErrChanged for symlink, got %v", err)
		}
	})

	t.Run("missing", func(t *testing.T) {
		if err := Revalidate(filepath.Join(dir, "nope"), id); !errors.Is(err, ErrChanged) {
			t.Fatalf("want ErrChanged for missing file, got %v", err)
		}
	})
}
