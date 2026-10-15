package scan

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"spacehound/internal/fsid"
	"spacehound/internal/model"
	"spacehound/internal/units"
)

// cacheRoots are user-level cache directories (relative to home) whose
// contents applications regenerate on demand.
var cacheRoots = []string{
	"Library/Caches",
	".cache",
	"Library/Developer/Xcode/DerivedData",
	".npm/_cacache",
}

// logRoots are user log directories where apps write rotating logs and
// diagnostic reports.
var logRoots = []string{"Library/Logs"}

var logLikeExts = map[string]bool{
	".log": true, ".ips": true, ".crash": true, ".spin": true, ".hang": true, ".diag": true,
}

// fileInfo is the scanner's view of one regular file.
type fileInfo struct {
	Path    string
	Rel     string // relative to home, or ""
	ModTime time.Time
	ID      fsid.Identity
}

func days(d time.Duration) int { return int(d.Hours() / 24) }

// Classify applies the large/log/cache detectors to one regular file and
// returns an Item, or nil if nothing matched. Duplicates are added later.
func (o *Options) Classify(f fileInfo) *model.Item {
	it := &model.Item{
		Path:      f.Path,
		Size:      f.ID.Size,
		Allocated: f.ID.Allocated,
		ModTime:   f.ModTime,
		Identity:  f.ID,
	}
	sens := sensitivity(f.Path, f.Rel)
	age := o.Now.Sub(f.ModTime)
	ageDays := days(age)

	// Large files.
	if f.ID.Size >= o.LargeThreshold {
		reason := fmt.Sprintf("Large file: %s (threshold %s)", units.FormatSize(f.ID.Size), units.FormatSize(o.LargeThreshold))
		risk := model.RiskReview
		if sens != "" {
			risk = model.RiskDangerous
			reason += "; " + sens
		} else {
			reason += "; personal data cannot be judged automatically, review before removing or offload it"
		}
		it.Add(model.CatLarge, risk, reason)
	}

	// Stale logs.
	if logMatch := o.logEvidence(f); logMatch != "" && f.ID.Size >= o.LogMinSize && ageDays >= o.StaleDays {
		reason := fmt.Sprintf("Stale log: %s, not modified for %d days (threshold %d days, %s)", logMatch, ageDays, o.StaleDays, units.FormatSize(o.LogMinSize))
		risk := model.RiskReview
		_, inLogRoot := hasPrefixFold(f.Rel, logRoots...)
		switch {
		case sens != "":
			risk = model.RiskDangerous
			reason += "; but " + sens
		case inLogRoot:
			risk = model.RiskLow
			reason += "; apps recreate logs in ~/Library/Logs as needed"
		default:
			reason += "; matched by name only, outside ~/Library/Logs it may belong to a project or app"
		}
		it.Add(model.CatStaleLog, risk, reason)
	}

	// Caches.
	if root, ok := hasPrefixFold(f.Rel, cacheRoots...); ok && sens == "" &&
		f.ID.Size >= o.CacheMinSize && ageDays >= o.CacheDays {
		owner := cacheOwner(f.Rel, root)
		reason := fmt.Sprintf("Cache file in ~/%s (owner: %s), %s, not modified for %d days (threshold %d days, %s)",
			root, owner, units.FormatSize(f.ID.Size), ageDays, o.CacheDays, units.FormatSize(o.CacheMinSize))
		risk := model.RiskLow
		lowerRel := strings.ToLower(f.Rel)
		switch {
		case strings.HasPrefix(strings.ToLower(owner), "com.apple."):
			risk = model.RiskReview
			reason += "; managed by macOS, which also purges it automatically"
		case strings.Contains(lowerRel, "huggingface") || strings.Contains(lowerRel, "/models/") ||
			strings.Contains(lowerRel, "ollama") || strings.Contains(lowerRel, "lm-studio"):
			risk = model.RiskReview
			reason += "; looks like a downloaded ML model, expensive to re-download"
		default:
			reason += "; the app will rebuild it if needed (may be slower on next launch)"
		}
		it.Add(model.CatCache, risk, reason)
	}

	if len(it.Categories) == 0 {
		return nil
	}
	o.annotate(it)
	return it
}

// annotate adds space-accounting caveats that apply to any flagged file.
func (o *Options) annotate(it *model.Item) {
	if it.Identity.Nlink > 1 {
		it.Reasons = append(it.Reasons, fmt.Sprintf(
			"Has %d hard links: removing this path frees no space while other links remain", it.Identity.Nlink))
		if it.Risk < model.RiskReview {
			it.Risk = model.RiskReview
		}
	}
	if it.Allocated < it.Size*9/10 {
		it.Reasons = append(it.Reasons, fmt.Sprintf(
			"Sparse or compressed: only %s allocated on disk, so expect to free about that much", units.FormatSize(it.Allocated)))
	}
}

// logEvidence explains why a file looks like a log, or returns "".
func (o *Options) logEvidence(f fileInfo) string {
	base := strings.ToLower(filepath.Base(f.Path))
	ext := filepath.Ext(base)
	if root, ok := hasPrefixFold(f.Rel, logRoots...); ok {
		return "in ~/" + root
	}
	if ext == ".log" {
		return "named *.log"
	}
	// Rotated logs: foo.log.1, foo.log.2.gz, foo.log.gz
	if i := strings.Index(base, ".log."); i > 0 {
		return "rotated log (" + base[i+1:] + ")"
	}
	if logLikeExts[ext] {
		return "diagnostic report (" + ext + ")"
	}
	return ""
}

// cacheOwner returns the first path component under the cache root.
func cacheOwner(rel, root string) string {
	rest := rel[len(root):]
	rest = strings.TrimPrefix(rest, "/")
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		return rest[:i]
	}
	return "(top level)"
}
