package server

import "testing"

func TestInferProfileName(t *testing.T) {
	tests := map[string]string{
		"/tmp/run/heap.prof":         "heap",
		"/tmp/run/allocs.prof":       "allocs",
		"/tmp/run/cpu.prof":          "cpu",
		"/tmp/run/goroutine.prof":    "goroutine",
		"/tmp/run/block.prof":        "block",
		"/tmp/run/mutex.prof":        "mutex",
		"pod/profile-cpu.pb.gz":      "cpu",
		"pod/service-allocs.pprof":   "allocs",
		"pod/service-goroutines.out": "goroutine",
		"pod/notes.txt":              "",
	}
	for name, want := range tests {
		if got := inferProfileName(name); got != want {
			t.Fatalf("inferProfileName(%q) = %q, want %q", name, got, want)
		}
	}
}
