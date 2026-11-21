package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

// Mode is what a run does with each request.
type Mode string

const (
	ModeRecord Mode = "record"
	ModeReplay Mode = "replay"
	ModeFork   Mode = "fork"
)

// Match decides what happens when a replayed request does not match the cassette.
type Match string

const (
	MatchStrict  Match = "strict"
	MatchLenient Match = "lenient"
)

// Where a step's response came from.
const (
	SourceLive     = "live"
	SourceReplayed = "replayed"
	SourceMutated  = "mutated"
	SourceDiverged = "diverged"
)

// Mutation is the single change applied at a fork point.
type Mutation struct {
	Type   string `json:"type"`
	Model  string `json:"model,omitempty"`
	Status int    `json:"status,omitempty"`
	Text   string `json:"text,omitempty"`
	Body   string `json:"body,omitempty"`
}

// Tokens is the usage reported by the provider.
type Tokens struct {
	Prompt     int `json:"prompt"`
	Completion int `json:"completion"`
}

// Step is one request/response pair seen by the gateway.
type Step struct {
	I               int             `json:"i"`
	Kind            string          `json:"kind"`
	Path            string          `json:"path"`
	Fingerprint     string          `json:"fingerprint"`
	Source          string          `json:"source"`
	Mutation        *Mutation       `json:"mutation,omitempty"`
	Request         json.RawMessage `json:"request"`
	Response        json.RawMessage `json:"response"`
	Status          int             `json:"status"`
	Model           string          `json:"model,omitempty"`
	ServedModel     string          `json:"served_model,omitempty"`
	Tokens          Tokens          `json:"tokens"`
	CostUSD         float64         `json:"cost_usd"`
	OriginalCostUSD float64         `json:"original_cost_usd,omitempty"`
	LatencyMS       int64           `json:"latency_ms"`
	TS              string          `json:"ts"`
}

func (s *Step) normalized() any {
	_, n := Fingerprint(s.Path, s.Request)
	return n
}

// FieldDiff is one differing field between two normalised requests.
type FieldDiff struct {
	Path     string `json:"path"`
	Expected string `json:"expected"`
	Got      string `json:"got"`
}

// Divergence records a replayed request that did not match its cassette step.
type Divergence struct {
	I        int         `json:"i"`
	Expected string      `json:"expected"`
	Got      string      `json:"got"`
	Diff     []FieldDiff `json:"diff"`
}

// Run is one cassette, being written.
type Run struct {
	mu          sync.Mutex
	flushMu     sync.Mutex // serialises writes to the same tmp file
	dir         string
	source      *Run
	next        int
	ID          string         `json:"run_id"`
	Mode        Mode           `json:"mode"`
	Source      string         `json:"source,omitempty"`
	Parent      *string        `json:"parent"`
	ForkAt      int            `json:"fork_at"`
	Mutation    *Mutation      `json:"mutation"`
	Match       Match          `json:"match"`
	Meta        map[string]any `json:"meta"`
	StartedAt   string         `json:"started_at"`
	EndedAt     string         `json:"ended_at,omitempty"`
	Steps       []Step         `json:"steps"`
	Divergences []Divergence   `json:"divergences"`
}

// NextIndex hands out step numbers in arrival order.
func (r *Run) NextIndex() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	i := r.next
	r.next++
	return i
}

// SourceStep returns step i of the cassette this run replays or forks from.
func (r *Run) SourceStep(i int) *Step {
	if r.source == nil {
		return nil
	}
	r.source.mu.Lock()
	defer r.source.mu.Unlock()
	for k := range r.source.Steps {
		if r.source.Steps[k].I == i {
			s := r.source.Steps[k]
			return &s
		}
	}
	return nil
}

// Append adds a finished step and writes the cassette to disk.
func (r *Run) Append(s Step) {
	r.mu.Lock()
	r.Steps = append(r.Steps, s)
	sort.Slice(r.Steps, func(a, b int) bool { return r.Steps[a].I < r.Steps[b].I })
	r.mu.Unlock()
	_ = r.Flush()
}

// AddDivergence records a replay miss.
func (r *Run) AddDivergence(d Divergence) {
	r.mu.Lock()
	r.Divergences = append(r.Divergences, d)
	r.mu.Unlock()
}

// Flush writes the cassette atomically.
func (r *Run) Flush() error {
	r.flushMu.Lock()
	defer r.flushMu.Unlock()
	r.mu.Lock()
	data, err := json.MarshalIndent(r, "", "  ")
	r.mu.Unlock()
	if err != nil {
		return err
	}
	tmp := filepath.Join(r.dir, r.ID+".json.tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(r.dir, r.ID+".json"))
}

// Summary is the short form returned by GET /rewind/runs.
type Summary struct {
	ID          string         `json:"run_id"`
	Mode        Mode           `json:"mode"`
	Parent      *string        `json:"parent"`
	ForkAt      int            `json:"fork_at"`
	Mutation    *Mutation      `json:"mutation"`
	Meta        map[string]any `json:"meta"`
	Steps       int            `json:"steps"`
	Divergences int            `json:"divergences"`
	CostUSD     float64        `json:"cost_usd"`
}

func (r *Run) summary() Summary {
	r.mu.Lock()
	defer r.mu.Unlock()
	cost := 0.0
	for _, s := range r.Steps {
		cost += s.CostUSD
	}
	return Summary{r.ID, r.Mode, r.Parent, r.ForkAt, r.Mutation, r.Meta, len(r.Steps), len(r.Divergences), cost}
}

// Store keeps open runs in memory and cassettes on disk.
type Store struct {
	mu   sync.Mutex
	dir  string
	runs map[string]*Run
}

var safeID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,200}$`)

// NewStore opens (and creates) the cassette directory.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir, runs: map[string]*Run{}}, nil
}

// Open starts a run. A replay or fork loads its source cassette first.
func (s *Store) Open(spec RunSpec) (*Run, error) {
	if !safeID.MatchString(spec.RunID) {
		return nil, fmt.Errorf("run_id must match %s", safeID)
	}
	mode := Mode(spec.Mode)
	if mode == "" {
		mode = ModeRecord
	}
	match := Match(spec.Match)
	if match == "" {
		match = MatchLenient
	}
	r := &Run{
		dir: s.dir, ID: spec.RunID, Mode: mode, Match: match, Meta: spec.Meta,
		ForkAt: -1, StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Steps: []Step{}, Divergences: []Divergence{},
	}
	switch mode {
	case ModeRecord:
	case ModeReplay, ModeFork:
		src, err := s.Load(spec.Source)
		if err != nil {
			return nil, fmt.Errorf("source %q: %w", spec.Source, err)
		}
		r.source = src
		r.Source = spec.Source
		if mode == ModeFork {
			if spec.Mutation == nil {
				return nil, fmt.Errorf("fork needs a mutation")
			}
			parent := spec.Source
			r.Parent = &parent
			r.ForkAt = spec.ForkAt
			r.Mutation = spec.Mutation
		}
	default:
		return nil, fmt.Errorf("unknown mode %q", spec.Mode)
	}
	s.mu.Lock()
	s.runs[r.ID] = r
	s.mu.Unlock()
	return r, r.Flush()
}

// GetOrCreate returns an open run, or starts recording one the agent named itself.
func (s *Store) GetOrCreate(id string) *Run {
	s.mu.Lock()
	r := s.runs[id]
	s.mu.Unlock()
	if r != nil {
		return r
	}
	if !safeID.MatchString(id) {
		id = "invalid-run-id"
	}
	r, _ = s.Open(RunSpec{RunID: id, Mode: string(ModeRecord)})
	return r
}

// Load reads a cassette (open or on disk).
func (s *Store) Load(id string) (*Run, error) {
	if !safeID.MatchString(id) {
		return nil, fmt.Errorf("bad run id")
	}
	s.mu.Lock()
	if r, ok := s.runs[id]; ok {
		s.mu.Unlock()
		return r, nil
	}
	s.mu.Unlock()
	data, err := os.ReadFile(filepath.Join(s.dir, id+".json"))
	if err != nil {
		return nil, err
	}
	r := &Run{dir: s.dir}
	if err := json.Unmarshal(data, r); err != nil {
		return nil, err
	}
	return r, nil
}

// Close finishes a run and removes it from memory.
func (s *Store) Close(id string) (*Run, error) {
	s.mu.Lock()
	r, ok := s.runs[id]
	delete(s.runs, id)
	s.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("run %q is not open", id)
	}
	r.mu.Lock()
	r.EndedAt = time.Now().UTC().Format(time.RFC3339Nano)
	r.mu.Unlock()
	return r, r.Flush()
}

// List returns summaries of every cassette on disk.
func (s *Store) List() []Summary {
	entries, _ := os.ReadDir(s.dir)
	out := []Summary{}
	for _, e := range entries {
		name := e.Name()
		if filepath.Ext(name) != ".json" {
			continue
		}
		if r, err := s.Load(name[:len(name)-5]); err == nil {
			out = append(out, r.summary())
		}
	}
	return out
}

// FlushAll writes every open run.
func (s *Store) FlushAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.runs {
		_ = r.Flush()
	}
}

// RunSpec is the body of POST /rewind/runs.
type RunSpec struct {
	RunID    string         `json:"run_id"`
	Mode     string         `json:"mode"`
	Source   string         `json:"source"`
	ForkAt   int            `json:"fork_at"`
	Mutation *Mutation      `json:"mutation"`
	Match    string         `json:"match"`
	Meta     map[string]any `json:"meta"`
}
