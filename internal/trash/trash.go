// Package trash moves files to the macOS Trash. It never unlinks a file
// permanently: if no Trash can be used it returns an explanatory error.
package trash

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"spacehound/internal/fsid"
)

// ErrNoTrash means the file's volume has no usable Trash.
var ErrNoTrash = errors.New("no usable Trash for this file")

// SystemTool is the trash(8) command shipped with macOS 15 and later. It
// uses the same mechanism as Finder (including "Put Back").
const SystemTool = "/usr/bin/trash"

// Trasher moves files to the Trash.
type Trasher struct {
	Home string
	// Tool is the path of the system trash command; empty disables it.
	Tool string
	// DirFor overrides Trash folder discovery (used by tests and demo mode).
	DirFor func(path string, dev uint64) (string, error)
	Now    func() time.Time
}

// New returns a Trasher that prefers the system tool when present.
func New(home string) *Trasher {
	t := &Trasher{Home: home}
	if fi, err := os.Stat(SystemTool); err == nil && fi.Mode()&0o111 != 0 {
		t.Tool = SystemTool
	}
	return t
}

// Result describes a completed move.
type Result struct {
	Path   string `json:"path"`
	Dest   string `json:"dest,omitempty"`
	Method string `json:"method"`
}

// Trash re-validates path against the scan identity and moves it to the
// Trash. It refuses if the file changed.
func (t *Trasher) Trash(path string, want fsid.Identity) (Result, error) {
	if !filepath.IsAbs(path) {
		return Result{}, fmt.Errorf("refusing relative path %q", path)
	}
	if err := fsid.Revalidate(path, want); err != nil {
		return Result{}, err
	}
	if t.Tool != "" && t.DirFor == nil {
		res, err := t.viaTool(path, want)
		if err == nil {
			return res, nil
		}
		// The tool failed; the file is untouched if it still matches.
		if verr := fsid.Revalidate(path, want); verr != nil {
			return Result{}, fmt.Errorf("trash tool failed (%v) and file state is uncertain: %w", err, verr)
		}
	}
	return t.native(path, want)
}

func (t *Trasher) viaTool(path string, want fsid.Identity) (Result, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, t.Tool, "-s", path)
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return Result{}, fmt.Errorf("%s: %v %s", t.Tool, err, strings.TrimSpace(out.String()))
	}
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		return Result{}, fmt.Errorf("%s reported success but %s still exists", t.Tool, path)
	}
	return Result{Path: path, Method: "macOS Trash (trash tool; Put Back available in Finder)"}, nil
}

func (t *Trasher) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

// dirFor finds the Trash folder on the same volume as the file.
func (t *Trasher) dirFor(path string, dev uint64) (string, error) {
	if t.DirFor != nil {
		return t.DirFor(path, dev)
	}
	home := filepath.Join(t.Home, ".Trash")
	if id, _, err := fsid.Of(home); err == nil && id.Dev == dev {
		return home, nil
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return "", fmt.Errorf("%w: cannot identify volume: %v", ErrNoTrash, err)
	}
	mnt := cString(st.Mntonname[:])
	if mnt == "" || mnt == "/" {
		return "", fmt.Errorf("%w: file is on the system volume outside your home Trash", ErrNoTrash)
	}
	trashes := filepath.Join(mnt, ".Trashes")
	if _, err := os.Lstat(trashes); err != nil {
		return "", fmt.Errorf("%w: volume %s has no .Trashes folder (it may be read-only or not support Trash)", ErrNoTrash, mnt)
	}
	dir := filepath.Join(trashes, fmt.Sprint(os.Getuid()))
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", fmt.Errorf("%w: cannot create %s: %v", ErrNoTrash, dir, err)
	}
	return dir, nil
}

func cString(b []int8) string {
	n := 0
	for n < len(b) && b[n] != 0 {
		n++
	}
	out := make([]byte, n)
	for i := 0; i < n; i++ {
		out[i] = byte(b[i])
	}
	return string(out)
}

// CandidateName returns the n-th collision-safe name for base: n=0 is the
// original name, then "<stem> (SpaceHound <stamp>)<ext>", then the same with
// " 2", " 3"...
func CandidateName(base string, stamp time.Time, n int) string {
	if n == 0 {
		return base
	}
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if stem == "" { // dotfile such as ".env"
		stem, ext = base, ""
	}
	tag := "SpaceHound " + stamp.Format("2006-01-02 15.04.05")
	if n > 1 {
		tag += fmt.Sprintf(" %d", n)
	}
	return fmt.Sprintf("%s (%s)%s", stem, tag, ext)
}

func (t *Trasher) native(path string, want fsid.Identity) (Result, error) {
	dir, err := t.dirFor(path, want.Dev)
	if err != nil {
		return Result{}, err
	}
	did, fi, err := fsid.Of(dir)
	if err != nil {
		return Result{}, explain(dir, err)
	}
	if !fi.IsDir() {
		return Result{}, fmt.Errorf("%w: %s is not a directory", ErrNoTrash, dir)
	}
	if did.Dev != want.Dev {
		return Result{}, fmt.Errorf("%w: %s is on a different volume; moving would require copying", ErrNoTrash, dir)
	}
	stamp := t.now()
	base := filepath.Base(path)
	for n := 0; n < 1000; n++ {
		dest := filepath.Join(dir, CandidateName(base, stamp, n))
		// Re-check right before the move.
		if err := fsid.Revalidate(path, want); err != nil {
			return Result{}, err
		}
		// os.Link fails if dest exists, giving an atomic no-overwrite move.
		err := os.Link(path, dest)
		switch {
		case err == nil:
			if err := os.Remove(path); err != nil {
				// Both names exist; nothing lost. Undo the trash link.
				os.Remove(dest)
				return Result{}, fmt.Errorf("could not remove original after linking into Trash: %w", err)
			}
			return Result{Path: path, Dest: dest, Method: "moved to " + dir}, nil
		case errors.Is(err, fs.ErrExist):
			continue
		case errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EMLINK) || errors.Is(err, syscall.EXDEV):
			// Filesystem without hard links: check-then-rename.
			if _, lerr := os.Lstat(dest); lerr == nil {
				continue
			}
			if err := os.Rename(path, dest); err != nil {
				return Result{}, explain(dest, err)
			}
			return Result{Path: path, Dest: dest, Method: "moved to " + dir}, nil
		default:
			return Result{}, explain(dir, err)
		}
	}
	return Result{}, fmt.Errorf("could not find a free name in %s", dir)
}

func explain(path string, err error) error {
	if errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EPERM) {
		return fmt.Errorf("%w: macOS denied access to %s (%v). Grant your terminal Full Disk Access in System Settings > Privacy & Security, or drag the file to the Trash in Finder. The file was left in place", ErrNoTrash, path, err)
	}
	return fmt.Errorf("%w: %v. The file was left in place", ErrNoTrash, err)
}
