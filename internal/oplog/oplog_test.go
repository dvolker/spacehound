package oplog

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLogAppendsJSONLines(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Logs", "SpaceHound")
	l := New(dir)
	if err := l.Log(Entry{Action: "trash", Path: "/x/a", Size: 3, OK: true}); err != nil {
		t.Fatal(err)
	}
	if err := l.Log(Entry{Action: "offload-copy", Path: "/x/b", Dest: "/Volumes/D/b", Error: "boom"}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(l.Path())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("log mode %v, want 0600", fi.Mode().Perm())
	}
	f, _ := os.Open(l.Path())
	defer f.Close()
	sc := bufio.NewScanner(f)
	var n int
	for sc.Scan() {
		var e Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		if e.Time.IsZero() {
			t.Error("missing timestamp")
		}
		n++
	}
	if n != 2 {
		t.Fatalf("got %d lines", n)
	}
	var nilLogger *Logger
	if err := nilLogger.Log(Entry{}); err != nil {
		t.Error("nil logger should discard")
	}
}

func TestDefaultDir(t *testing.T) {
	if got := DefaultDir("/Users/me"); got != "/Users/me/Library/Logs/SpaceHound" {
		t.Error(got)
	}
}
