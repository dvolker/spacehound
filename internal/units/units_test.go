package units

import "testing"

func TestParseSize(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"0", 0},
		{"1024", 1024},
		{"10B", 10},
		{"1K", KiB},
		{"1KiB", KiB},
		{"1kb", 1000},
		{"50MiB", 50 * MiB},
		{"50M", 50 * MiB},
		{"50MB", 50_000_000},
		{"1GiB", GiB},
		{"1 GiB", GiB},
		{"1.5g", GiB + GiB/2},
		{"2TB", 2_000_000_000_000},
		{" 3 mib ", 3 * MiB},
	}
	for _, c := range cases {
		got, err := ParseSize(c.in)
		if err != nil {
			t.Errorf("ParseSize(%q) error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseSize(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestParseSizeErrors(t *testing.T) {
	for _, in := range []string{"", "GiB", "-1", "1XB", "1.2.3G", "abc", "1 G B", "9999999999TiB"} {
		if _, err := ParseSize(in); err == nil {
			t.Errorf("ParseSize(%q) expected error", in)
		}
	}
}

func TestFormatSize(t *testing.T) {
	cases := map[int64]string{
		0:             "0 B",
		1023:          "1023 B",
		KiB:           "1.0 KiB",
		3 * MiB / 2:   "1.5 MiB",
		GiB:           "1.0 GiB",
		5 * TiB:       "5.0 TiB",
		-2 * MiB:      "-2.0 MiB",
		GiB + 100*MiB: "1.1 GiB",
	}
	for in, want := range cases {
		if got := FormatSize(in); got != want {
			t.Errorf("FormatSize(%d) = %q, want %q", in, got, want)
		}
	}
}
