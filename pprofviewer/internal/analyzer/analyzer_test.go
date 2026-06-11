package analyzer

import (
	"bytes"
	"testing"

	"github.com/google/pprof/profile"
)

func TestAnalyzeHeapProfile(t *testing.T) {
	prof := &profile.Profile{
		SampleType: []*profile.ValueType{
			{Type: "alloc_space", Unit: "bytes"},
			{Type: "inuse_space", Unit: "bytes"},
			{Type: "inuse_objects", Unit: "count"},
		},
		Sample: []*profile.Sample{
			{
				Location: []*profile.Location{
					location(1, "github.com/acme/service.leaf", "/repo/service/leaf.go", 12),
					location(2, "github.com/acme/service.root", "/repo/service/root.go", 8),
				},
				Value: []int64{2048, 1024, 4},
			},
		},
		Location: []*profile.Location{
			location(1, "github.com/acme/service.leaf", "/repo/service/leaf.go", 12),
			location(2, "github.com/acme/service.root", "/repo/service/root.go", 8),
		},
		Function: []*profile.Function{
			function(1, "github.com/acme/service.leaf", "/repo/service/leaf.go"),
			function(2, "github.com/acme/service.root", "/repo/service/root.go"),
		},
	}

	var buf bytes.Buffer
	if err := prof.Write(&buf); err != nil {
		t.Fatal(err)
	}

	got, err := Analyze(&buf, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.ProfileType != "heap" {
		t.Fatalf("profile type = %q, want heap", got.ProfileType)
	}
	if got.Total != 2048 {
		t.Fatalf("total = %d, want 2048", got.Total)
	}
	if len(got.Nodes) == 0 {
		t.Fatal("expected graph nodes")
	}
	if len(got.Leaks) != 1 {
		t.Fatalf("leaks = %d, want 1", len(got.Leaks))
	}
	if got.Leaks[0].RetainedBytes != 1024 {
		t.Fatalf("retained = %d, want 1024", got.Leaks[0].RetainedBytes)
	}
}

func location(id uint64, name, file string, line int64) *profile.Location {
	return &profile.Location{
		ID: id,
		Line: []profile.Line{{
			Function: function(id, name, file),
			Line:     line,
		}},
	}
}

func function(id uint64, name, file string) *profile.Function {
	return &profile.Function{ID: id, Name: name, SystemName: name, Filename: file}
}
