// Package oplog appends a JSON-lines record of every mutating operation to
// ~/Library/Logs/SpaceHound/operations.log. It records paths, sizes,
// hashes and outcomes, never file contents.
package oplog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"

	"spacehound/internal/macosfs"
)

// FileName is the log file inside the log directory.
const FileName = "operations.log"

const maxRecordBytes = 1024 * 1024

// DefaultDir returns ~/Library/Logs/SpaceHound.
func DefaultDir(home string) string { return filepath.Join(home, "Library", "Logs", "SpaceHound") }

// Entry is one logged operation.
type Entry struct {
	ID           string    `json:"operation_id,omitempty"`
	Phase        string    `json:"phase,omitempty"` // intent, captured, complete, recovery
	RecoveryPath string    `json:"recovery_path,omitempty"`
	Time         time.Time `json:"time"`
	Action       string    `json:"action"` // trash, offload-copy, offload-remove-source, offload-trash-source
	Path         string    `json:"path"`
	Dest         string    `json:"dest,omitempty"`
	Size         int64     `json:"size,omitempty"`
	SHA256       string    `json:"sha256,omitempty"`
	OK           bool      `json:"ok"`
	Error        string    `json:"error,omitempty"`
	FreeBefore   int64     `json:"internal_free_before,omitempty"`
	FreeAfter    int64     `json:"internal_free_after,omitempty"`
}

// Logger writes entries. Log on a nil *Logger discards entries; Check and
// Pending require a logger.
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

// Log appends and syncs e. The log is private to the user (0600).
func (l *Logger) Log(e Entry) (err error) {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	defer wrapError(&err)
	if err := validateEntry(e); err != nil {
		return err
	}
	if e.Time.IsZero() {
		if l.Now != nil {
			e.Time = l.Now()
		} else {
			e.Time = time.Now()
		}
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if len(b)+1 > maxRecordBytes {
		return errors.New("record exceeds 1 MiB limit")
	}
	dir, f, err := l.open(true)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, closeFiles(f, dir)) }()
	if _, err := readPending(f); err != nil {
		return err
	}
	b = append(b, '\n')
	if n, err := f.Write(b); err != nil {
		return err
	} else if n != len(b) {
		return io.ErrShortWrite
	}
	return syncFiles(f, dir)
}

// Check prepares the journal and validates it before a destructive operation.
// Unlike Log, Check rejects a nil logger: a live mutation needs a journal.
func (l *Logger) Check() (err error) {
	if l == nil {
		return errors.New("operation log: logger is required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	defer wrapError(&err)
	dir, f, err := l.open(true)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, closeFiles(f, dir)) }()
	if _, err := readPending(f); err != nil {
		return err
	}
	return syncFiles(f, dir)
}

// Pending returns each operation whose latest phase is not complete. It does
// not create or alter the journal. Invalid or truncated records are errors so
// an uncertain recovery state is never silently presented as an empty journal.
func (l *Logger) Pending() (pending []Entry, err error) {
	if l == nil {
		return nil, errors.New("operation log: logger is required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	defer wrapError(&err)
	dir, f, err := l.open(false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { err = errors.Join(err, closeFiles(f, dir)) }()
	return readPending(f)
}

func readPending(f *os.File) ([]Entry, error) {
	byID := make(map[string]Entry)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), maxRecordBytes)
	scanner.Split(journalLines)
	line := 0
	for scanner.Scan() {
		line++
		var e *Entry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("record %d: %w", line, err)
		}
		if e == nil {
			return nil, fmt.Errorf("record %d: expected an operation object", line)
		}
		if err := validateEntry(*e); err != nil {
			return nil, fmt.Errorf("record %d: %w", line, err)
		}
		if e.Phase == "" {
			continue // Entries from versions without recovery journaling.
		}
		if e.Phase == "complete" {
			delete(byID, e.ID)
		} else {
			byID[e.ID] = *e
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("record %d: %w", line+1, err)
	}
	var pending []Entry
	for _, e := range byID {
		pending = append(pending, e)
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].ID < pending[j].ID })
	return pending, nil
}

func (l *Logger) open(writable bool) (*os.File, *os.File, error) {
	if l.Dir == "" {
		return nil, nil, errors.New("empty log directory")
	}
	path, err := filepath.Abs(l.Dir)
	if err != nil {
		return nil, nil, err
	}
	dir, err := macosfs.OpenDir(path, writable, 0o700)
	if err != nil {
		return nil, nil, err
	}
	fail := func(err error) (*os.File, *os.File, error) {
		return nil, nil, errors.Join(err, dir.Close())
	}
	if err := validateFile(dir, false); err != nil {
		return fail(err)
	}
	if writable {
		if err := macosfs.MakePrivate(dir, 0o700); err != nil {
			return fail(err)
		}
	}
	flags := os.O_RDONLY | syscall.O_NONBLOCK
	if writable {
		flags = os.O_RDWR | os.O_APPEND | os.O_CREATE | syscall.O_NONBLOCK
	}
	f, err := macosfs.OpenAt(dir, FileName, flags, 0o600)
	if err != nil {
		return fail(err)
	}
	if err := validateFile(f, true); err != nil {
		return nil, nil, errors.Join(err, closeFiles(f, dir))
	}
	if writable {
		if err := macosfs.MakePrivate(f, 0o600); err != nil {
			return nil, nil, errors.Join(err, closeFiles(f, dir))
		}
	}
	return dir, f, nil
}

func validateFile(f *os.File, regular bool) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot validate %s ownership", f.Name())
	}
	if st.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("%s is owned by another user", f.Name())
	}
	if regular {
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", f.Name())
		}
		if st.Nlink != 1 {
			return fmt.Errorf("%s has %d hard links; require exactly one", f.Name(), st.Nlink)
		}
	} else if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", f.Name())
	}
	return nil
}

func validateEntry(e Entry) error {
	if e.Phase == "" {
		return nil
	}
	if e.ID == "" || e.Path == "" {
		return errors.New("phased record needs an operation ID and source path")
	}
	switch e.Phase {
	case "intent", "captured", "recovery":
		if e.RecoveryPath == "" {
			return fmt.Errorf("%s record needs a recovery path", e.Phase)
		}
	case "complete":
	default:
		return fmt.Errorf("unknown phase %q", e.Phase)
	}
	return nil
}

func journalLines(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF && len(data) > 0 {
		return 0, nil, errors.New("truncated journal record (missing newline)")
	}
	return 0, nil, nil
}

func syncFiles(files ...*os.File) error {
	for _, f := range files {
		if err := f.Sync(); err != nil {
			return err
		}
	}
	return nil
}

func closeFiles(files ...*os.File) error {
	var err error
	for _, f := range files {
		err = errors.Join(err, f.Close())
	}
	return err
}

func wrapError(err *error) {
	if *err != nil {
		*err = fmt.Errorf("operation log: %w", *err)
	}
}
