// Package offload copies a regular file to an external volume, verifies the
// copy byte-for-byte, and only then lets the caller remove the original.
//
// The sequence is deliberately conservative:
//
//  1. re-check the source against its scan identity;
//  2. re-check destination suitability and free space (with headroom);
//  3. build the destination path without following symlinks and refuse to
//     overwrite anything;
//  4. copy to a hidden partial name in the destination folder (ditto by
//     default, preserving macOS metadata);
//  5. verify regular-file type, byte count and SHA-256 against the source,
//     then confirm the source still matches its scan identity;
//  6. publish the partial under its final name without overwriting.
//
// Copy never removes the source. RemoveSource does, and callers must only
// invoke it after a separate explicit user confirmation.
package offload

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"spacehound/internal/diskspace"
	"spacehound/internal/fsid"
	"spacehound/internal/volumes"
)

// ArchiveDir is the top-level folder created on the destination volume.
const ArchiveDir = "SpaceHound Archive"

// Ditto is the macOS copy tool that preserves resource forks, extended
// attributes, ACLs and timestamps.
const Ditto = "/usr/bin/ditto"

var (
	// ErrDestExists means the destination already exists; nothing is overwritten.
	ErrDestExists = errors.New("destination already exists")
	// ErrVerify means the copy does not match the source.
	ErrVerify = errors.New("copy verification failed")
)

// CopyFunc copies the regular file src to the new path dst. dst must not
// exist; implementations must not overwrite it.
type CopyFunc func(ctx context.Context, src, dst string) error

// ProgressFunc receives coarse progress: phase is "copying" or "verifying".
type ProgressFunc func(phase string, done, total int64)

// Offloader performs offloads. Fields are injectable for tests and demo mode.
type Offloader struct {
	Home   string
	Host   string
	Now    func() time.Time
	Copy   CopyFunc
	Statfs func(string) (diskspace.Info, error)
	// AllowSameDevice permits a destination on the source's own filesystem.
	// Only tests and --demo set it; on a real system it would free nothing.
	AllowSameDevice bool
	// PollInterval controls how often copy progress is sampled.
	PollInterval time.Duration
}

// New returns an Offloader using ditto when available.
func New(home string) *Offloader {
	o := &Offloader{Home: home, Host: Hostname(), Statfs: diskspace.Statfs, Copy: GoCopy}
	if fi, err := os.Stat(Ditto); err == nil && fi.Mode()&0o111 != 0 {
		o.Copy = DittoCopy
	}
	return o
}

// Hostname returns a short, filesystem-safe host name.
func Hostname() string {
	h, _ := os.Hostname()
	h = strings.TrimSuffix(h, ".local")
	return SanitizeName(h)
}

// SanitizeName makes s usable as a single path component.
func SanitizeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '/' || r == ':' || r == '\\' || r < 0x20 || r == 0x7f {
			return '_'
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if s == "" || strings.Trim(s, ".") == "" {
		return "Mac"
	}
	return s
}

// Request describes one file to offload.
type Request struct {
	Source   string
	Identity fsid.Identity
	Volume   volumes.Volume
}

// Result describes an offload attempt. Verified is true only when the
// destination was proven identical and published under Dest.
type Result struct {
	Source         string        `json:"source"`
	Dest           string        `json:"dest"`
	Partial        string        `json:"partial,omitempty"`
	Size           int64         `json:"size"`
	SHA256         string        `json:"sha256,omitempty"`
	Verified       bool          `json:"verified"`
	SourceIdentity fsid.Identity `json:"-"`
	DestIdentity   fsid.Identity `json:"-"`
}

func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../") && !filepath.IsAbs(rel)
}

// DestPath returns <vol>/SpaceHound Archive/<host>/<YYYY-MM-DD>/<rel>, where
// rel is the source path relative to home (or "_outside_home/<abs path>").
// It rejects anything that could escape the archive folder.
func DestPath(vol, host string, date time.Time, home, src string) (string, error) {
	if !filepath.IsAbs(src) || filepath.Clean(src) != src {
		return "", fmt.Errorf("source must be a clean absolute path: %q", src)
	}
	if !filepath.IsAbs(vol) || filepath.Clean(vol) != vol || vol == "/" {
		return "", fmt.Errorf("invalid volume path %q", vol)
	}
	var rel string
	if home != "" && src != home && strings.HasPrefix(src, filepath.Clean(home)+"/") {
		rel = src[len(filepath.Clean(home))+1:]
	} else {
		rel = filepath.Join("_outside_home", strings.TrimPrefix(src, "/"))
	}
	for _, c := range strings.Split(rel, "/") {
		if c == "" || c == "." || c == ".." {
			return "", fmt.Errorf("unsafe path component %q in %q", c, src)
		}
	}
	base := filepath.Join(vol, ArchiveDir, SanitizeName(host), date.Format("2006-01-02"))
	dest := filepath.Join(base, rel)
	if !within(dest, base) || dest == base {
		return "", fmt.Errorf("destination %q escapes %q", dest, base)
	}
	return dest, nil
}

// mkdirNoFollow creates every directory from root down to dir, refusing to
// traverse symlinks or non-directories. root itself must already exist.
func mkdirNoFollow(root, dir string) error {
	fi, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory (symlinks are not followed)", root)
	}
	if !within(dir, root) {
		return fmt.Errorf("%s is outside %s", dir, root)
	}
	rel, _ := filepath.Rel(root, dir)
	cur := root
	for _, c := range strings.Split(rel, string(filepath.Separator)) {
		if c == "." || c == "" {
			continue
		}
		cur = filepath.Join(cur, c)
		fi, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			if err := os.Mkdir(cur, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
			fi, err = os.Lstat(cur)
		}
		if err != nil {
			return err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("refusing to follow symlink %s on destination", cur)
		}
		if !fi.IsDir() {
			return fmt.Errorf("%s exists and is not a directory", cur)
		}
	}
	return nil
}

func (o *Offloader) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// Plan returns the destination path for req without touching anything.
func (o *Offloader) Plan(req Request) (string, error) {
	return DestPath(req.Volume.Path, o.Host, o.now(), o.Home, req.Source)
}

// Offload copies and verifies req. It never removes or alters the source.
// On failure after copying began, Result.Partial names the leftover copy.
func (o *Offloader) Offload(ctx context.Context, req Request, progress ProgressFunc) (Result, error) {
	res := Result{Source: req.Source, Size: req.Identity.Size, SourceIdentity: req.Identity}
	if progress == nil {
		progress = func(string, int64, int64) {}
	}
	if err := fsid.Revalidate(req.Source, req.Identity); err != nil {
		return res, err
	}
	if req.Identity.Nlink > 1 {
		return res, fmt.Errorf("%s has %d hard links; offloading one name frees no space", req.Source, req.Identity.Nlink)
	}
	vol := req.Volume
	if !o.AllowSameDevice && vol.Dev == req.Identity.Dev {
		return res, fmt.Errorf("%s is on the same disk as the source; offloading would free nothing", vol.Path)
	}
	if o.Statfs != nil {
		info, err := o.Statfs(vol.Path)
		if err != nil {
			return res, fmt.Errorf("destination unavailable: %w", err)
		}
		if !o.AllowSameDevice && info.MountPoint != vol.Path {
			return res, fmt.Errorf("%s is no longer mounted", vol.Path)
		}
		if info.ReadOnly() {
			return res, fmt.Errorf("%s is now read-only", vol.Path)
		}
		vol.Free, vol.Total = info.Free, info.Total
	}
	if err := vol.CanHold(req.Identity.Size); err != nil {
		return res, err
	}
	dest, err := o.Plan(req)
	if err != nil {
		return res, err
	}
	res.Dest = dest
	if _, err := os.Lstat(dest); err == nil {
		return res, fmt.Errorf("%w: %s (nothing was overwritten)", ErrDestExists, dest)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return res, err
	}
	dir := filepath.Dir(dest)
	if err := mkdirNoFollow(vol.Path, dir); err != nil {
		return res, err
	}
	partial := filepath.Join(dir, fmt.Sprintf(".%s.spacehound-partial-%d", filepath.Base(dest), o.now().UnixNano()))
	if _, err := os.Lstat(partial); err == nil {
		return res, fmt.Errorf("%w: %s", ErrDestExists, partial)
	}

	// Copy.
	if err := o.copyWithProgress(ctx, req.Source, partial, req.Identity.Size, progress); err != nil {
		return o.leftover(res, partial, fmt.Errorf("copy failed: %w", err))
	}

	// Verify the copy.
	cur, fi, err := fsid.Of(partial)
	if err != nil {
		return o.leftover(res, partial, fmt.Errorf("%w: cannot stat copy: %v", ErrVerify, err))
	}
	if !fi.Mode().IsRegular() {
		return o.leftover(res, partial, fmt.Errorf("%w: copy is not a regular file (%s)", ErrVerify, fi.Mode().Type()))
	}
	if cur.Size != req.Identity.Size {
		return o.leftover(res, partial, fmt.Errorf("%w: copy has %d bytes, source has %d", ErrVerify, cur.Size, req.Identity.Size))
	}
	srcSum, err := hashFile(ctx, req.Source, &req.Identity, func(n int64) { progress("verifying", n, 2*req.Identity.Size) })
	if err != nil {
		return o.leftover(res, partial, fmt.Errorf("%w: hashing source: %v", ErrVerify, err))
	}
	dstSum, err := hashFile(ctx, partial, nil, func(n int64) { progress("verifying", req.Identity.Size+n, 2*req.Identity.Size) })
	if err != nil {
		return o.leftover(res, partial, fmt.Errorf("%w: hashing copy: %v", ErrVerify, err))
	}
	if srcSum != dstSum {
		return o.leftover(res, partial, fmt.Errorf("%w: SHA-256 mismatch (source %s, copy %s)", ErrVerify, srcSum, dstSum))
	}
	if err := fsid.Revalidate(req.Source, req.Identity); err != nil {
		return o.leftover(res, partial, fmt.Errorf("source changed during offload: %w", err))
	}
	res.SHA256 = srcSum

	// Publish without overwriting.
	if err := publish(partial, dest); err != nil {
		return o.leftover(res, partial, err)
	}
	did, dfi, err := fsid.Of(dest)
	if err != nil || !dfi.Mode().IsRegular() || did.Size != req.Identity.Size {
		return o.leftover(res, "", fmt.Errorf("%w: published copy could not be re-checked: %v", ErrVerify, err))
	}
	res.DestIdentity = did
	res.Verified = true
	return res, nil
}

func (o *Offloader) leftover(res Result, partial string, err error) (Result, error) {
	if partial != "" {
		if _, lerr := os.Lstat(partial); lerr == nil {
			res.Partial = partial
			return res, fmt.Errorf("%w. The original was kept; a partial copy was left at %s for manual inspection", err, partial)
		}
	}
	return res, fmt.Errorf("%w. The original was kept", err)
}

// publish gives partial its final name dest without ever replacing dest.
func publish(partial, dest string) error {
	err := os.Link(partial, dest)
	switch {
	case err == nil:
		return os.Remove(partial) // drop the temporary second name
	case errors.Is(err, fs.ErrExist):
		return fmt.Errorf("%w: %s appeared during the copy (nothing was overwritten)", ErrDestExists, dest)
	case errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EMLINK):
		// FAT/exFAT have no hard links: check, then rename.
		if _, lerr := os.Lstat(dest); lerr == nil {
			return fmt.Errorf("%w: %s (nothing was overwritten)", ErrDestExists, dest)
		}
		return os.Rename(partial, dest)
	default:
		return err
	}
}

func (o *Offloader) copyWithProgress(ctx context.Context, src, dst string, total int64, progress ProgressFunc) error {
	if o.Copy == nil {
		return errors.New("no copy method configured")
	}
	interval := o.PollInterval
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if fi, err := os.Lstat(dst); err == nil {
					progress("copying", fi.Size(), total)
				}
			}
		}
	}()
	progress("copying", 0, total)
	err := o.Copy(ctx, src, dst)
	close(done)
	<-stopped
	if err == nil {
		progress("copying", total, total)
	}
	return err
}

// hashFile returns the SHA-256 of path, opened without following symlinks.
// If want is set the open file must still match that identity.
func hashFile(ctx context.Context, path string, want *fsid.Identity, step func(int64)) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if want != nil {
		fi, err := f.Stat()
		if err != nil {
			return "", err
		}
		id, ok := fsid.FromFileInfo(fi)
		if !ok || !id.SameFile(*want) || id.Size != want.Size || id.MtimeNs != want.MtimeNs {
			return "", fmt.Errorf("%w: %s", fsid.ErrChanged, path)
		}
	}
	h := sha256.New()
	buf := make([]byte, 1<<20)
	var n int64
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		k, rerr := f.Read(buf)
		h.Write(buf[:k])
		n += int64(k)
		if step != nil && k > 0 {
			step(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return "", rerr
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// CheckRemovable re-confirms that a verified copy is still in place and the
// source is unchanged. Call it immediately before removing the original.
func CheckRemovable(res Result) error {
	if !res.Verified {
		return errors.New("copy was not verified; the original must be kept")
	}
	if err := fsid.Revalidate(res.Dest, res.DestIdentity); err != nil {
		return fmt.Errorf("verified copy changed or disappeared, keeping the original: %w", err)
	}
	if err := fsid.Revalidate(res.Source, res.SourceIdentity); err != nil {
		return err
	}
	return nil
}

// RemoveSource deletes the original after CheckRemovable succeeds. Callers
// must only use it after the user explicitly confirmed removal of a
// verified offload; the data then lives on at res.Dest.
func RemoveSource(res Result) error {
	if err := CheckRemovable(res); err != nil {
		return err
	}
	return os.Remove(res.Source)
}

// DittoCopy copies with /usr/bin/ditto, preserving macOS metadata.
func DittoCopy(ctx context.Context, src, dst string) error {
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("%w: %s", ErrDestExists, dst)
	}
	out, err := exec.CommandContext(ctx, Ditto, src, dst).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ditto: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// GoCopy is a portable fallback that copies data, permissions and mtime.
// It does not preserve extended attributes or resource forks.
func GoCopy(ctx context.Context, src, dst string) error {
	in, err := os.OpenFile(src, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", src)
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	_, cerr := io.Copy(out, ctxReader{ctx, in})
	if cerr == nil {
		cerr = out.Sync()
	}
	if err := out.Close(); cerr == nil {
		cerr = err
	}
	if cerr != nil {
		return cerr
	}
	os.Chmod(dst, fi.Mode().Perm())
	return os.Chtimes(dst, fi.ModTime(), fi.ModTime())
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
