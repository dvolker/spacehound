// Package units parses and formats byte sizes.
//
// Parsing rules (case-insensitive, optional space between number and unit):
//
//	1024, 1024B         bytes
//	K, KiB, M, MiB ...  binary units (powers of 1024)
//	KB, MB, GB, TB      decimal units (powers of 1000), as shown by Finder
//
// Fractions are allowed ("1.5GiB"). Negative values are rejected.
package units

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
)

const (
	KiB int64 = 1 << 10
	MiB int64 = 1 << 20
	GiB int64 = 1 << 30
	TiB int64 = 1 << 40
)

var multipliers = map[string]float64{
	"":    1,
	"b":   1,
	"k":   float64(KiB),
	"kib": float64(KiB),
	"m":   float64(MiB),
	"mib": float64(MiB),
	"g":   float64(GiB),
	"gib": float64(GiB),
	"t":   float64(TiB),
	"tib": float64(TiB),
	"kb":  1e3,
	"mb":  1e6,
	"gb":  1e9,
	"tb":  1e12,
}

// ParseSize converts a human size string into bytes.
func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}
	i := 0
	for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.') {
		i++
	}
	num, unit := s[:i], strings.ToLower(strings.TrimSpace(s[i:]))
	if num == "" {
		return 0, fmt.Errorf("invalid size %q: missing number", s)
	}
	for _, r := range unit {
		if !unicode.IsLetter(r) {
			return 0, fmt.Errorf("invalid size %q", s)
		}
	}
	mult, ok := multipliers[unit]
	if !ok {
		return 0, fmt.Errorf("invalid size %q: unknown unit %q", s, unit)
	}
	v, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	b := v * mult
	if math.IsInf(b, 0) || math.IsNaN(b) || b > math.MaxInt64/2 {
		return 0, fmt.Errorf("size %q is too large", s)
	}
	return int64(math.Round(b)), nil
}

// FormatSize renders bytes using binary (IEC) units with one decimal place.
func FormatSize(b int64) string {
	neg := b < 0
	if neg {
		b = -b
	}
	var out string
	switch {
	case b < KiB:
		out = fmt.Sprintf("%d B", b)
	case b < MiB:
		out = fmt.Sprintf("%.1f KiB", float64(b)/float64(KiB))
	case b < GiB:
		out = fmt.Sprintf("%.1f MiB", float64(b)/float64(MiB))
	case b < TiB:
		out = fmt.Sprintf("%.1f GiB", float64(b)/float64(GiB))
	default:
		out = fmt.Sprintf("%.1f TiB", float64(b)/float64(TiB))
	}
	if neg {
		return "-" + out
	}
	return out
}
