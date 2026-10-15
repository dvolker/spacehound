// Package scan walks a directory tree read-only and classifies files that
// may be worth removing or offloading. It never modifies anything.
package scan

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"spacehound/internal/fsid"
	"spacehound/internal/model"
)

// sfDataless marks APFS cloud placeholders whose contents are not local;
// reading them would trigger a download.
const sfDataless = 0x40000000

// Progress is updated concurrently by a running scan and may be read at any
// time from another goroutine.
type Progress struct {
	Files     atomic.Int64
	Dirs      atomic.Int64
	Bytes     atomic.Int64
	Errors    atomic.Int64
	HashTotal atomic.Int64
	HashDone  atomic.Int64
	mu        sync.Mutex
	phase     string
	current   string
}

func (p *Progress) set(phase, current string) {
	p.mu.Lock()
	p.phase, p.current = phase, current
	p.mu.Unlock()
}

// Snapshot returns the current phase and path being processed.
func (p *Progress) Snapshot() (phase, current string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.phase, p.current
}

// Run performs a scan. Cancelling ctx stops promptly and returns a partial
// report with Cancelled set; the error is nil in that case.
func Run(ctx context.Context, opts Options, prog *Progress) (*model.Report, error) {
	if err := opts.Normalize(); err != nil {
		return nil, err
	}
	if prog == nil {
		prog = &Progress{}
	}
	s := &scanner{opts: &opts, prog: prog, items: map[string]*model.Item{}}
	rep := &model.Report{
		Tool:      "spacehound",
		Root:      opts.Root,
		Home:      opts.Home,
		StartedAt: time.Now(),
		Options:   opts,
	}
	if err := s.walk(ctx); err != nil {
		return nil, err
	}
	var dups []model.DuplicateGroup
	if opts.Duplicates && ctx.Err() == nil {
		dups = s.findDuplicates(ctx)
	}
	rep.Cancelled = ctx.Err() != nil
	if rep.Cancelled {
		rep.Notes = append(rep.Notes, "Scan was cancelled; results are partial.")
		if opts.Duplicates {
			rep.Notes = append(rep.Notes, "Duplicate detection did not complete.")
		}
	}
	for _, it := range s.items {
		rep.Items = append(rep.Items, it)
	}
	model.SortItems(rep.Items)
	rep.Duplicates = dups
	rep.Skipped = s.skipped
	rep.Errors = s.errs
	rep.ErrorCount = int(prog.Errors.Load())
	rep.FilesSeen = prog.Files.Load()
	rep.DirsSeen = prog.Dirs.Load()
	rep.BytesSeen = prog.Bytes.Load()
	rep.Summary = model.Summarize(rep.Items, dups)
	rep.FinishedAt = time.Now()
	prog.set("done", "")
	return rep, nil
}

type scanner struct {
	opts      *Options
	prog      *Progress
	devs      map[uint64]bool
	items     map[string]*model.Item
	dupCands  []fileInfo
	skipped   []model.Skip
	errs      []string
	skipCount int
}

func (s *scanner) skip(path, reason string) {
	s.skipCount++
	if len(s.skipped) < s.opts.MaxRecorded {
		s.skipped = append(s.skipped, model.Skip{Path: path, Reason: reason})
	}
}

func (s *scanner) fail(path string, err error) {
	s.prog.Errors.Add(1)
	if len(s.errs) < s.opts.MaxRecorded {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			err = pe.Err
		}
		s.errs = append(s.errs, fmt.Sprintf("%s: %v", path, err))
	}
}

// allowedDevices returns the filesystems the walk may visit. On macOS the
// home directory lives on the Data volume, which is firmlinked into "/";
// scanning "/" therefore also admits the Data volume.
func (s *scanner) allowedDevices() error {
	id, _, err := fsid.Of(s.opts.Root)
	if err != nil {
		return err
	}
	s.devs = map[uint64]bool{id.Dev: true}
	if s.opts.Root == "/" {
		if d, _, err := fsid.Of("/System/Volumes/Data"); err == nil {
			s.devs[d.Dev] = true
		}
	}
	return nil
}

func (s *scanner) walk(ctx context.Context) error {
	if err := s.allowedDevices(); err != nil {
		return err
	}
	o := s.opts
	s.prog.set("scanning", o.Root)
	err := filepath.WalkDir(o.Root, func(path string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return fs.SkipAll
		}
		if err != nil {
			s.fail(path, err)
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		typ := d.Type()
		if typ&fs.ModeSymlink != 0 {
			return nil // never follow symlinks
		}
		if d.IsDir() {
			if path != o.Root {
				if reason := o.skipReason(path, true); reason != "" {
					s.skip(path, reason)
					return fs.SkipDir
				}
			}
			info, err := d.Info()
			if err != nil {
				s.fail(path, err)
				return fs.SkipDir
			}
			if id, ok := fsid.FromFileInfo(info); ok && !o.CrossDevices && !s.devs[id.Dev] {
				s.skip(path, "different filesystem (use --cross-devices to include)")
				return fs.SkipDir
			}
			s.prog.Dirs.Add(1)
			if n := s.prog.Dirs.Load(); n%64 == 0 {
				s.prog.set("scanning", path)
			}
			return nil
		}
		if !typ.IsRegular() {
			return nil // sockets, devices, fifos
		}
		if reason := o.skipReason(path, false); reason != "" {
			s.skip(path, reason)
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				s.fail(path, err)
			}
			return nil
		}
		st, _ := info.Sys().(*syscall.Stat_t)
		id, ok := fsid.FromFileInfo(info)
		if !ok || st == nil {
			return nil
		}
		s.prog.Files.Add(1)
		s.prog.Bytes.Add(id.Size)
		if st.Flags&sfDataless != 0 {
			s.skip(path, "cloud placeholder (contents not downloaded)")
			return nil
		}
		f := fileInfo{Path: path, Rel: homeRel(path, o.Home), ModTime: info.ModTime(), ID: id}
		if it := o.Classify(f); it != nil {
			s.items[path] = it
		}
		if o.Duplicates && id.Size >= o.DupThreshold {
			s.dupCands = append(s.dupCands, f)
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return err
	}
	if s.skipCount > len(s.skipped) {
		s.skipped = append(s.skipped, model.Skip{Path: "…", Reason: fmt.Sprintf("%d more skipped paths not listed", s.skipCount-len(s.skipped))})
	}
	return nil
}
