package demo

import (
	"context"
	"os"
	"strings"
	"testing"

	"spacehound/internal/model"
	"spacehound/internal/scan"
)

func TestDemoFixtureScan(t *testing.T) {
	f, err := Build()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Remove()
	rep, err := scan.Run(context.Background(), f.Options(), nil)
	if err != nil {
		t.Fatal(err)
	}
	cats := map[model.Category]int{}
	for _, it := range rep.Items {
		if strings.Contains(it.Path, ".Trash") || strings.Contains(it.Path, "Mobile Documents") || strings.Contains(it.Path, "Desktop-link") {
			t.Errorf("flagged excluded path %s", it.Path)
		}
		for _, c := range it.Categories {
			cats[c]++
		}
	}
	for _, c := range model.Categories {
		if cats[c] == 0 {
			t.Errorf("demo has no %s items", c)
		}
	}
	if len(rep.Duplicates) != 1 {
		t.Errorf("want 1 duplicate group, got %d", len(rep.Duplicates))
	}
	vols, err := f.Lister().List()
	if err != nil || len(vols) != 3 || !vols[0].Suitable || vols[1].Suitable || vols[2].Suitable {
		t.Errorf("demo volumes: %+v %v", vols, err)
	}
	dir := f.Dir
	if err := f.Remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("fixture not removed")
	}
}

func TestRemoveRefusesForeignDir(t *testing.T) {
	f := &Fixture{Dir: t.TempDir()}
	if err := f.Remove(); err == nil {
		t.Error("removed a non-demo directory")
	}
}
