// Package volumes discovers mounted external volumes under /Volumes and
// decides, read-only, whether each is a suitable offload destination.
package volumes

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"

	"spacehound/internal/diskspace"
	"spacehound/internal/fsid"
	"spacehound/internal/units"
)

// DefaultRoot is where macOS mounts additional volumes.
const DefaultRoot = "/Volumes"

// FATMaxFile is the largest file a FAT32 (msdos) volume can store.
const FATMaxFile = 4*units.GiB - 1

// Volume is a mounted filesystem and its suitability as an offload target.
type Volume struct {
	Path     string `json:"path"`
	Name     string `json:"name"`
	FSType   string `json:"fs_type"`
	Dev      uint64 `json:"dev"`
	Writable bool   `json:"writable"`
	Free     int64  `json:"free"`
	Total    int64  `json:"total"`
	Suitable bool   `json:"suitable"`
	// Reasons explains why an unsuitable volume was rejected.
	Reasons []string `json:"reasons,omitempty"`
	// MaxFileSize is the largest file the filesystem stores (0 = no limit).
	MaxFileSize int64 `json:"max_file_size,omitempty"`
}

// Candidate is the raw evidence gathered about one /Volumes entry.
type Candidate struct {
	Path      string
	IsSymlink bool
	IsDir     bool
	Dev       uint64
	Info      diskspace.Info
	Writable  bool
	// RootNames are the names of entries at the volume root, used to detect
	// backup volumes.
	RootNames []string
}

var networkFS = map[string]bool{"smbfs": true, "nfs": true, "afpfs": true, "webdav": true, "ftp": true, "cifs": true}
var unsuitableFS = map[string]string{
	"devfs":  "device filesystem",
	"autofs": "automounter placeholder",
	"cd9660": "optical disc (read-only)",
	"udf":    "optical disc",
	"nullfs": "system bind mount",
}

var tmSnapshotName = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}-\d{6}\.(backup|previous|inprogress|interrupted)$`)

// backupMarker reports whether a root entry name indicates Time Machine or
// another backup structure.
func backupMarker(name string) string {
	l := strings.ToLower(name)
	switch {
	case name == "Backups.backupdb":
		return "Time Machine backup disk (Backups.backupdb)"
	case strings.HasPrefix(l, ".com.apple.timemachine"):
		return "Time Machine destination (" + name + ")"
	case strings.HasSuffix(l, ".backupbundle"):
		return "Time Machine backup bundle"
	case tmSnapshotName.MatchString(name):
		return "Time Machine APFS backup volume"
	}
	return ""
}

// Assess decides whether a candidate is a suitable offload destination.
// internalDevs are device numbers of internal volumes (root and home).
func Assess(c Candidate, internalDevs map[uint64]bool) Volume {
	v := Volume{
		Path:     c.Path,
		Name:     filepath.Base(c.Path),
		FSType:   c.Info.FSType,
		Dev:      c.Dev,
		Writable: c.Writable && !c.Info.ReadOnly(),
		Free:     c.Info.Free,
		Total:    c.Info.Total,
	}
	reject := func(format string, a ...any) { v.Reasons = append(v.Reasons, fmt.Sprintf(format, a...)) }
	if c.IsSymlink {
		reject("symlink (the startup disk appears this way)")
	}
	if !c.IsDir && !c.IsSymlink {
		reject("not a directory")
	}
	if c.Info.MountPoint != c.Path {
		reject("not a mount point; files would land on %q", c.Info.MountPoint)
	}
	if internalDevs[c.Dev] || c.Info.Flags&diskspace.FlagRootFS != 0 {
		reject("internal disk; offloading there frees nothing")
	}
	if c.Info.ReadOnly() {
		reject("mounted read-only")
	} else if !c.Writable {
		reject("not writable by you")
	}
	if c.Info.Flags&diskspace.FlagDontBrowse != 0 {
		reject("hidden system volume")
	}
	if networkFS[c.Info.FSType] || (!c.Info.Local() && c.Info.FSType != "") {
		reject("network volume (MVP offloads only to locally attached drives)")
	}
	if why, ok := unsuitableFS[c.Info.FSType]; ok {
		reject("%s", why)
	}
	for _, n := range c.RootNames {
		if m := backupMarker(n); m != "" {
			reject("%s", m)
			break
		}
	}
	if c.Info.FSType == "msdos" {
		v.MaxFileSize = FATMaxFile
	}
	v.Suitable = len(v.Reasons) == 0
	return v
}

// Headroom is the space SpaceHound leaves free on a destination beyond the
// file itself: 1% of the volume, at least 100 MiB.
func Headroom(total int64) int64 {
	h := total / 100
	if h < 100*units.MiB {
		h = 100 * units.MiB
	}
	return h
}

// CanHold returns nil if v can store a file of the given size with headroom.
func (v Volume) CanHold(size int64) error {
	if !v.Suitable {
		return fmt.Errorf("%s is not a suitable destination: %s", v.Path, strings.Join(v.Reasons, "; "))
	}
	if v.MaxFileSize > 0 && size > v.MaxFileSize {
		return fmt.Errorf("%s (%s) cannot store files larger than %s", v.Name, v.FSType, units.FormatSize(v.MaxFileSize))
	}
	if need := size + Headroom(v.Total); v.Free < need {
		return fmt.Errorf("%s has %s free; need %s including %s headroom",
			v.Name, units.FormatSize(v.Free), units.FormatSize(need), units.FormatSize(Headroom(v.Total)))
	}
	return nil
}

// Lister enumerates volumes. Its fields are injectable for tests and demo mode.
type Lister struct {
	Root         string
	Statfs       func(string) (diskspace.Info, error)
	Writable     func(string) bool
	InternalDevs map[uint64]bool
}

// NewLister returns a Lister for the real /Volumes, treating the devices
// of "/" and home as internal.
func NewLister(home string) *Lister {
	devs := map[uint64]bool{}
	for _, p := range []string{"/", "/System/Volumes/Data", home} {
		if id, _, err := fsid.Of(p); err == nil {
			devs[id.Dev] = true
		}
	}
	return &Lister{Root: DefaultRoot, Statfs: diskspace.Statfs, Writable: writable, InternalDevs: devs}
}

func writable(path string) bool { return syscall.Access(path, 2 /* W_OK */) == nil }

// List returns all volumes under Root, suitable ones first. It only reads
// directory entries and metadata.
func (l *Lister) List() ([]Volume, error) {
	ents, err := os.ReadDir(l.Root)
	if err != nil {
		return nil, err
	}
	var out []Volume
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		p := filepath.Join(l.Root, e.Name())
		c := Candidate{Path: p}
		fi, err := os.Lstat(p)
		if err != nil {
			continue
		}
		c.IsSymlink = fi.Mode()&fs.ModeSymlink != 0
		c.IsDir = fi.IsDir()
		if id, ok := fsid.FromFileInfo(fi); ok {
			c.Dev = id.Dev
		}
		if c.IsDir {
			if c.Info, err = l.Statfs(p); err != nil {
				continue
			}
			c.Writable = l.Writable(p)
			if names, err := readNames(p); err == nil {
				c.RootNames = names
			}
		}
		out = append(out, Assess(c, l.InternalDevs))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Suitable != out[j].Suitable {
			return out[i].Suitable
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func readNames(dir string) ([]string, error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Readdirnames(256)
}
