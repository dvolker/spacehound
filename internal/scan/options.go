package scan

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"spacehound/internal/units"
)

// Options configures a scan. Zero values are not meaningful; start from
// DefaultOptions.
type Options struct {
	Root           string   `json:"root"`
	Home           string   `json:"home"`
	LargeThreshold int64    `json:"large_threshold"`
	Duplicates     bool     `json:"duplicates"`
	DupThreshold   int64    `json:"duplicate_threshold"`
	StaleDays      int      `json:"stale_log_days"`
	LogMinSize     int64    `json:"stale_log_min_size"`
	CacheDays      int      `json:"cache_days"`
	CacheMinSize   int64    `json:"cache_min_size"`
	CrossDevices   bool     `json:"cross_devices"`
	Excludes       []string `json:"excludes"`
	Includes       []string `json:"includes"`
	// Now is the reference time for age calculations (defaults to time.Now).
	Now time.Time `json:"-"`
	// MaxRecorded caps how many skip/error records are kept in the report.
	MaxRecorded int `json:"-"`
}

// DefaultOptions returns the documented defaults for the current user.
func DefaultOptions() Options {
	home, _ := os.UserHomeDir()
	return Options{
		Root:           home,
		Home:           home,
		LargeThreshold: units.GiB,
		Duplicates:     true,
		DupThreshold:   50 * units.MiB,
		StaleDays:      90,
		LogMinSize:     units.MiB,
		CacheDays:      30,
		CacheMinSize:   10 * units.MiB,
		MaxRecorded:    200,
	}
}

// ExpandPath expands a leading "~" and makes the path absolute and clean.
func ExpandPath(p, home string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("empty path")
	}
	if p == "~" {
		p = home
	} else if strings.HasPrefix(p, "~/") {
		p = filepath.Join(home, p[2:])
	}
	return filepath.Abs(p)
}

// Normalize validates options and resolves paths.
func (o *Options) Normalize() error {
	if o.Home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("cannot determine home directory: %w", err)
		}
		o.Home = h
	}
	home, err := filepath.Abs(o.Home)
	if err != nil {
		return err
	}
	o.Home = filepath.Clean(home)
	if o.Root == "" {
		o.Root = o.Home
	}
	if o.Root, err = ExpandPath(o.Root, o.Home); err != nil {
		return err
	}
	fi, err := os.Lstat(o.Root)
	if err != nil {
		return fmt.Errorf("scan root: %w", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("scan root %s is a symlink; pass the real path (SpaceHound never follows symlinks)", o.Root)
	}
	if !fi.IsDir() {
		return fmt.Errorf("scan root %s is not a directory", o.Root)
	}
	for i, inc := range o.Includes {
		if o.Includes[i], err = ExpandPath(inc, o.Home); err != nil {
			return fmt.Errorf("--include %q: %w", inc, err)
		}
	}
	for i, ex := range o.Excludes {
		if strings.ContainsRune(ex, '/') || strings.HasPrefix(ex, "~") {
			if o.Excludes[i], err = ExpandPath(ex, o.Home); err != nil {
				return fmt.Errorf("--exclude %q: %w", ex, err)
			}
		} else if _, err := filepath.Match(ex, ""); err != nil {
			return fmt.Errorf("--exclude %q: bad pattern: %w", ex, err)
		}
	}
	switch {
	case o.LargeThreshold <= 0:
		return fmt.Errorf("large-file threshold must be positive")
	case o.DupThreshold <= 0:
		return fmt.Errorf("duplicate threshold must be positive")
	case o.StaleDays < 0 || o.CacheDays < 0:
		return fmt.Errorf("day thresholds must not be negative")
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	if o.MaxRecorded <= 0 {
		o.MaxRecorded = 200
	}
	return nil
}
