// Package diskspace reports filesystem capacity and explains, read-only, why
// freed bytes may not show up as free space on APFS.
package diskspace

import (
	"fmt"
	"strings"
	"syscall"

	"spacehound/internal/units"
)

// Mount flags from <sys/mount.h>.
const (
	FlagReadOnly   = 0x00000001 // MNT_RDONLY
	FlagLocal      = 0x00001000 // MNT_LOCAL
	FlagRootFS     = 0x00004000 // MNT_ROOTFS
	FlagDontBrowse = 0x00100000 // MNT_DONTBROWSE
)

// Info describes the filesystem containing a path.
type Info struct {
	Path       string `json:"path"`
	MountPoint string `json:"mount_point"`
	MountFrom  string `json:"mount_from"`
	FSType     string `json:"fs_type"`
	Flags      uint32 `json:"flags"`
	Free       int64  `json:"free"`  // bytes available to unprivileged users
	Total      int64  `json:"total"` // filesystem size
}

// ReadOnly reports whether the filesystem is mounted read-only.
func (i Info) ReadOnly() bool { return i.Flags&FlagReadOnly != 0 }

// Local reports whether the filesystem is local (not a network share).
func (i Info) Local() bool { return i.Flags&FlagLocal != 0 }

// Statfs returns capacity information for the filesystem containing path.
func Statfs(path string) (Info, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return Info{}, fmt.Errorf("statfs %s: %w", path, err)
	}
	return Info{
		Path:       path,
		MountPoint: cString(st.Mntonname[:]),
		MountFrom:  cString(st.Mntfromname[:]),
		FSType:     cString(st.Fstypename[:]),
		Flags:      st.Flags,
		Free:       int64(st.Bavail) * int64(st.Bsize),
		Total:      int64(st.Blocks) * int64(st.Bsize),
	}, nil
}

func cString(b []int8) string {
	var sb strings.Builder
	for _, c := range b {
		if c == 0 {
			break
		}
		sb.WriteByte(byte(c))
	}
	return sb.String()
}

// DiagnosticCommands are read-only commands that explain where space went.
// SpaceHound only prints them; it never runs commands that delete snapshots.
var DiagnosticCommands = []struct{ Cmd, Why string }{
	{"df -h /System/Volumes/Data", "current free space on the internal Data volume"},
	{"tmutil listlocalsnapshots /", "Time Machine local snapshots that still reference deleted blocks"},
	{"diskutil apfs listSnapshots /System/Volumes/Data", "all APFS snapshots on the Data volume"},
	{"lsof +L1", "files that were deleted but are still held open by a running app"},
}

// Shortfall compares the observed free-space change with the expected
// savings. It returns guidance text when much less space appeared than
// expected (for example because snapshots or open files retain the blocks),
// or "" when the result looks normal.
func Shortfall(before, after, expected int64) string {
	if expected <= 0 {
		return ""
	}
	gained := after - before
	// Allow for unrelated disk activity and allocation granularity.
	if gained >= expected/2 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Expected about %s more free space but observed %s.\n", units.FormatSize(expected), units.FormatSize(gained))
	b.WriteString("Common reasons on macOS (APFS):\n")
	b.WriteString("  - Items in the Trash still use space until the Trash is emptied.\n")
	b.WriteString("  - APFS/Time Machine local snapshots keep deleted blocks until the snapshot expires.\n")
	b.WriteString("  - A running app may still hold a deleted file open; quitting it releases the space.\n")
	b.WriteString("  - APFS clones and hard links share blocks, so removing one name frees little.\n")
	b.WriteString("  - macOS may report \"purgeable\" space differently from df.\n")
	b.WriteString("Read-only diagnostics you can run in Terminal:\n")
	for _, c := range DiagnosticCommands {
		fmt.Fprintf(&b, "  %-48s # %s\n", c.Cmd, c.Why)
	}
	b.WriteString("SpaceHound never deletes snapshots. If you decide to, use Time Machine settings or tmutil yourself.")
	return b.String()
}
