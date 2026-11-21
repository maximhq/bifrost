package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func newStore(t testing.TB) *Store {
	t.Helper()
	s, err := NewStore(filepath.Join(t.TempDir(), "cassettes"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func readCassette(t *testing.T, dir, id string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("cassette is not valid JSON: %v", err)
	}
	return m
}

func TestStoreOpenValidation(t *testing.T) {
	s := newStore(t)
	bad := []RunSpec{
		{RunID: ""},
		{RunID: "../escape"},
		{RunID: "a/b"},
		{RunID: "ok", Mode: "rewindify"},
		{RunID: "ok", Mode: "replay", Source: "missing"},
		{RunID: "ok", Mode: "replay", Source: "../x"},
	}
	for _, spec := range bad {
		if _, err := s.Open(spec); err == nil {
			t.Errorf("Open(%+v) should fail", spec)
		}
	}
	if _, err := s.Open(RunSpec{RunID: "src"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open(RunSpec{RunID: "f", Mode: "fork", Source: "src"}); err == nil {
		t.Error("fork without mutation should fail")
	}
}

func TestStoreOpenDefaults(t *testing.T) {
	s := newStore(t)
	r, err := s.Open(RunSpec{RunID: "r1", Meta: map[string]any{"agent": "a"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Mode != ModeRecord || r.Match != MatchLenient || r.ForkAt != -1 || r.Parent != nil {
		t.Fatalf("defaults: %+v", r)
	}
	m := readCassette(t, s.dir, "r1")
	if m["run_id"] != "r1" || m["mode"] != "record" || m["started_at"] == "" {
		t.Fatalf("cassette = %v", m)
	}
	if steps, ok := m["steps"].([]any); !ok || len(steps) != 0 {
		t.Fatalf("steps should be an empty list, got %v", m["steps"])
	}

	f, err := s.Open(RunSpec{RunID: "f1", Mode: "fork", Source: "r1", ForkAt: 2, Match: "strict",
		Mutation: &Mutation{Type: "outage", Status: 503}})
	if err != nil {
		t.Fatal(err)
	}
	if f.Parent == nil || *f.Parent != "r1" || f.ForkAt != 2 || f.Match != MatchStrict || f.Mutation.Type != "outage" || f.source == nil {
		t.Fatalf("fork = %+v", f)
	}
}

func TestStoreAppendFlushLoadCloseList(t *testing.T) {
	s := newStore(t)
	r, _ := s.Open(RunSpec{RunID: "r"})
	// out of order arrival is stored in index order
	r.Append(Step{I: 1, Kind: "relay", CostUSD: 0.008, Request: json.RawMessage(`{}`), Response: json.RawMessage(`{}`)})
	r.Append(Step{I: 0, Kind: "llm", CostUSD: 0.5, Request: json.RawMessage(`{}`), Response: json.RawMessage(`{}`)})
	r.AddDivergence(Divergence{I: 3, Expected: "a", Got: "b"})
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}

	// a fresh store on the same dir reads it from disk
	s2, _ := NewStore(s.dir)
	l, err := s2.Load("r")
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Steps) != 2 || l.Steps[0].I != 0 || l.Steps[1].I != 1 || len(l.Divergences) != 1 {
		t.Fatalf("loaded = %+v", l)
	}
	// open runs are served from memory
	if l2, _ := s.Load("r"); l2 != r {
		t.Fatal("Load should return the open run")
	}
	if _, err := s.Load("nope"); err == nil {
		t.Fatal("Load of missing run should fail")
	}
	if _, err := s.Load("bad/id"); err == nil {
		t.Fatal("Load of bad id should fail")
	}

	c, err := s.Close("r")
	if err != nil || c.EndedAt == "" {
		t.Fatalf("close: %v %+v", err, c)
	}
	if _, err := s.Close("r"); err == nil {
		t.Fatal("second close should fail")
	}
	if m := readCassette(t, s.dir, "r"); m["ended_at"] == "" || m["ended_at"] == nil {
		t.Fatal("ended_at not written")
	}

	s.Open(RunSpec{RunID: "other"})
	os.WriteFile(filepath.Join(s.dir, "notes.txt"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(s.dir, "broken.json"), []byte("{"), 0o644)
	list := s.List()
	byID := map[string]Summary{}
	for _, x := range list {
		byID[x.ID] = x
	}
	if len(list) != 2 {
		t.Fatalf("list = %+v", list)
	}
	if got := byID["r"]; got.Steps != 2 || got.Divergences != 1 || !near(got.CostUSD, 0.508) {
		t.Fatalf("summary = %+v", got)
	}
}

func TestStoreGetOrCreate(t *testing.T) {
	s := newStore(t)
	a := s.GetOrCreate("agent-picked")
	if a == nil || a.Mode != ModeRecord {
		t.Fatalf("GetOrCreate = %+v", a)
	}
	if s.GetOrCreate("agent-picked") != a {
		t.Fatal("GetOrCreate should return the open run")
	}
	if b := s.GetOrCreate("bad id with spaces"); b == nil || b.ID != "invalid-run-id" {
		t.Fatalf("bad id = %+v", b)
	}
}

func TestStoreFlushAll(t *testing.T) {
	s := newStore(t)
	r, _ := s.Open(RunSpec{RunID: "x"})
	r.mu.Lock()
	r.Steps = append(r.Steps, Step{I: 0, Request: json.RawMessage(`{}`), Response: json.RawMessage(`{}`)})
	r.mu.Unlock()
	s.FlushAll()
	m := readCassette(t, s.dir, "x")
	if len(m["steps"].([]any)) != 1 {
		t.Fatal("FlushAll did not write steps")
	}
}

func TestRunConcurrentAppend(t *testing.T) {
	s := newStore(t)
	r, _ := s.Open(RunSpec{RunID: "c"})
	const n = 40
	var wg sync.WaitGroup
	for k := 0; k < n; k++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			i := r.NextIndex()
			r.Append(Step{I: i, Request: json.RawMessage(`{}`), Response: json.RawMessage(`{"ok":true}`)})
		}()
	}
	wg.Wait()
	m := readCassette(t, s.dir, "c")
	steps := m["steps"].([]any)
	if len(steps) != n {
		t.Fatalf("want %d steps on disk, got %d", n, len(steps))
	}
	for k, st := range steps {
		if int(st.(map[string]any)["i"].(float64)) != k {
			t.Fatalf("steps out of order at %d", k)
		}
	}
}

func TestSourceStep(t *testing.T) {
	s := newStore(t)
	src, _ := s.Open(RunSpec{RunID: "src"})
	src.Append(Step{I: 0, Fingerprint: "f0", Request: json.RawMessage(`{}`), Response: json.RawMessage(`{}`)})
	rep, _ := s.Open(RunSpec{RunID: "rep", Mode: "replay", Source: "src"})
	if st := rep.SourceStep(0); st == nil || st.Fingerprint != "f0" {
		t.Fatalf("SourceStep(0) = %+v", st)
	}
	if rep.SourceStep(1) != nil {
		t.Fatal("SourceStep past the end should be nil")
	}
	if src.SourceStep(0) != nil {
		t.Fatal("record run has no source")
	}
}
