package main

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"spacehound/internal/demo"
	"spacehound/internal/model"
	"spacehound/internal/units"
)

func TestParseArgsFlags(t *testing.T) {
	c, err := parseArgs([]string{"--root", "/tmp/x", "--large", "500MB", "--dup-threshold", "10MiB",
		"--stale-days", "30", "--quick", "--exclude", "node_modules", "--exclude", "~/VMs", "--json"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	o := c.scanOptions(nil)
	if o.Root != "/tmp/x" || o.LargeThreshold != 500e6 || o.DupThreshold != 10*units.MiB ||
		o.StaleDays != 30 || o.Duplicates || !c.JSON {
		t.Errorf("unexpected options %+v", o)
	}
	if len(o.Excludes) != 2 || o.Excludes[0] != "node_modules" || o.Excludes[1] != "~/VMs" {
		t.Errorf("excludes = %v", o.Excludes)
	}
}

func TestParseArgsDefaults(t *testing.T) {
	c, err := parseArgs(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	o := c.scanOptions(nil)
	if o.LargeThreshold != units.GiB || o.DupThreshold != 50*units.MiB || o.StaleDays != 90 || !o.Duplicates {
		t.Errorf("defaults changed: %+v", o)
	}
}

func TestParseArgsRejects(t *testing.T) {
	for _, args := range [][]string{
		{"--large", "-1"},
		{"--large", "0"},
		{"--large", "lots"},
		{"--stale-days", "-3"},
		{"--exclude", ""},
		{"stray"},
		{"--demo", "--root", "/"},
		{"--demo", "--include", "~/Library/Mobile Documents"},
	} {
		if _, err := parseArgs(args, io.Discard); err == nil {
			t.Errorf("%v: expected error", args)
		}
	}
}

func TestDemoOptionsStayInFixture(t *testing.T) {
	fx := &demo.Fixture{Home: "/tmp/spacehound-demo-x/home"}
	c, err := parseArgs([]string{"--demo", "--stale-days", "7"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	o := c.scanOptions(fx)
	if o.Root != fx.Home || o.Home != fx.Home {
		t.Errorf("demo scan escapes fixture: root=%s home=%s", o.Root, o.Home)
	}
	if o.StaleDays != 7 || o.LargeThreshold != fx.Options().LargeThreshold {
		t.Errorf("demo overrides wrong: %+v", o)
	}
}

func TestRunDemoJSON(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"--demo", "--json"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	var rep model.Report
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rep.Root, "spacehound-demo-") || len(rep.Items) == 0 || rep.Version != version {
		t.Errorf("unexpected demo report: root=%s items=%d", rep.Root, len(rep.Items))
	}
}
