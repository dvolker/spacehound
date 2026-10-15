// Package oplog appends a JSON-lines record of every mutating operation to
// ~/Library/Logs/SpaceHound/operations.log. It records paths, sizes,
// hashes and outcomes, never file contents.
package oplog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// FileName is the log file inside the log directory.
const FileName = "operations.log"

// DefaultDir returns ~/Library/Logs/SpaceHound.
func DefaultDir(home string) string { return filepath.Join(home, "Library", "Logs", "SpaceHound") }

// Entry is one logged operation.
type Entry struct {
	Time       time.Time `json:"time"`
	Action     string    `json:"action"` // trash, offload-copy, offload-remove-source, offload-trash-source
	Path       string    `json:"path"`
	Dest       string    `json:"dest,omitempty"`
	Size       int64     `json:"size,omitempty"`
	SHA256     string    `json:"sha256,omitempty"`
	OK         bool      `json:"ok"`
	Error      string    `json:"error,omitempty"`
	FreeBefore int64     `json:"internal_free_before,omitempty"`
	FreeAfter  int64     `json:"internal_free_after,omitempty"`
}

// Logger writes entries. A nil *Logger discards everything.
type Logger struct {
	Dir string
	Now func() time.Time
	mu  sync.Mutex
}

// New returns a logger writing into dir. The directory is created lazily
// on the first write.
func New(dir string) *Logger { return &Logger{Dir: dir} }

// Path returns the log file path.
func (l *Logger) Path() string { return filepath.Join(l.Dir, FileName) }

// Log appends e. The log is private to the user (0600).
func (l *Logger) Log(e Entry) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if e.Time.IsZero() {
		if l.Now != nil {
			e.Time = l.Now()
		} else {
			e.Time = time.Now()
		}
	}
	if err := os.MkdirAll(l.Dir, 0o700); err != nil {
		return fmt.Errorf("operation log: %w", err)
	}
	f, err := os.OpenFile(l.Path(), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("operation log: %w", err)
	}
	defer f.Close()
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return err
}
