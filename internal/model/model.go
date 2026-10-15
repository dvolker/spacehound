// Package model defines the scan result types shared by the scanner, the
// JSON output and the TUI.
package model

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"spacehound/internal/fsid"
)

// Category is the kind of finding.
type Category string

const (
	CatLarge     Category = "large"
	CatStaleLog  Category = "stale-log"
	CatCache     Category = "cache"
	CatDuplicate Category = "duplicate"
)

// Categories lists all categories in display order.
var Categories = []Category{CatLarge, CatDuplicate, CatCache, CatStaleLog}

// Risk expresses how much human judgement an item needs before removal.
type Risk int

const (
	// RiskLow: strong evidence the file is regenerable or disposable.
	RiskLow Risk = iota
	// RiskReview: probably user data or ambiguous; look before acting.
	RiskReview
	// RiskDangerous: removal may break an app or lose data; manual review only.
	RiskDangerous
)

// Risks lists all risk levels in ascending order.
var Risks = []Risk{RiskLow, RiskReview, RiskDangerous}

func (r Risk) String() string {
	switch r {
	case RiskLow:
		return "low"
	case RiskReview:
		return "review"
	case RiskDangerous:
		return "dangerous"
	}
	return fmt.Sprintf("risk(%d)", int(r))
}

// MarshalJSON encodes a Risk as its string name.
func (r Risk) MarshalJSON() ([]byte, error) { return json.Marshal(r.String()) }

// UnmarshalJSON decodes a Risk from its string name.
func (r *Risk) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	for _, v := range Risks {
		if v.String() == s {
			*r = v
			return nil
		}
	}
	return fmt.Errorf("unknown risk %q", s)
}

// Item is a single flagged regular file. A file appears at most once even if
// several detectors matched; Categories lists every match and Risk is the
// most cautious level any detector assigned.
type Item struct {
	Path       string        `json:"path"`
	Size       int64         `json:"size"`
	Allocated  int64         `json:"allocated"`
	ModTime    time.Time     `json:"mtime"`
	Category   Category      `json:"category"`
	Categories []Category    `json:"categories"`
	Reasons    []string      `json:"reasons"`
	Risk       Risk          `json:"risk"`
	Identity   fsid.Identity `json:"identity"`
	DupGroup   int           `json:"duplicate_group,omitempty"`
}

// Has reports whether the item matched category c.
func (it *Item) Has(c Category) bool {
	for _, x := range it.Categories {
		if x == c {
			return true
		}
	}
	return false
}

// Add records a detector match.
func (it *Item) Add(c Category, r Risk, reason string) {
	if !it.Has(c) {
		it.Categories = append(it.Categories, c)
	}
	if it.Category == "" {
		it.Category = c
	}
	if r > it.Risk {
		it.Risk = r
	}
	if reason != "" {
		it.Reasons = append(it.Reasons, reason)
	}
}

// LikelySavings is a conservative estimate of physical space freed by
// removing the item: allocated bytes when known, never more than the
// logical size, and zero when other hard links keep the data alive.
func (it *Item) LikelySavings() int64 {
	if it.Identity.Nlink > 1 {
		return 0
	}
	if it.Allocated < it.Size {
		return it.Allocated
	}
	return it.Size
}

// DuplicateGroup is a set of files with identical content.
type DuplicateGroup struct {
	ID          int      `json:"id"`
	Size        int64    `json:"size"`
	SHA256      string   `json:"sha256"`
	Keep        string   `json:"keep"`
	Copies      []string `json:"copies"`
	Recoverable int64    `json:"recoverable"`
}

// Skip records a directory or file deliberately not scanned.
type Skip struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Totals aggregates bytes and counts.
type Totals struct {
	Count     int   `json:"count"`
	Size      int64 `json:"size"`
	Allocated int64 `json:"allocated"`
}

// Summary aggregates items without double counting files matched by
// several detectors.
type Summary struct {
	Total      Totals              `json:"total"`
	ByCategory map[Category]Totals `json:"by_category"`
	ByRisk     map[string]Totals   `json:"by_risk"`
	// DuplicateRecoverable is the logical size of all redundant copies.
	DuplicateRecoverable int64 `json:"duplicate_recoverable"`
}

// Report is the complete scan output.
type Report struct {
	Tool       string           `json:"tool"`
	Version    string           `json:"version"`
	Root       string           `json:"root"`
	Home       string           `json:"home"`
	StartedAt  time.Time        `json:"started_at"`
	FinishedAt time.Time        `json:"finished_at"`
	Cancelled  bool             `json:"cancelled"`
	Options    any              `json:"options"`
	FilesSeen  int64            `json:"files_seen"`
	DirsSeen   int64            `json:"dirs_seen"`
	BytesSeen  int64            `json:"bytes_seen"`
	Items      []*Item          `json:"items"`
	Duplicates []DuplicateGroup `json:"duplicate_groups"`
	Skipped    []Skip           `json:"skipped"`
	Errors     []string         `json:"errors"`
	ErrorCount int              `json:"error_count"`
	Summary    Summary          `json:"summary"`
	FreeBytes  int64            `json:"internal_free_bytes"`
	TotalBytes int64            `json:"internal_total_bytes"`
	Notes      []string         `json:"notes,omitempty"`
}

// Summarize computes summary totals for items.
func Summarize(items []*Item, dups []DuplicateGroup) Summary {
	s := Summary{ByCategory: map[Category]Totals{}, ByRisk: map[string]Totals{}}
	for _, it := range items {
		add := func(t Totals) Totals {
			t.Count++
			t.Size += it.Size
			t.Allocated += it.Allocated
			return t
		}
		s.Total = add(s.Total)
		for _, c := range it.Categories {
			s.ByCategory[c] = add(s.ByCategory[c])
		}
		s.ByRisk[it.Risk.String()] = add(s.ByRisk[it.Risk.String()])
	}
	for _, g := range dups {
		s.DuplicateRecoverable += g.Recoverable
	}
	return s
}

// SortItems orders items by descending size then path, the default view.
func SortItems(items []*Item) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Size != items[j].Size {
			return items[i].Size > items[j].Size
		}
		return items[i].Path < items[j].Path
	})
}
