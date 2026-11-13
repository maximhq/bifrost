// Package suggestrun is one phrase-suggestion run over captured traffic: it
// reads the requests captured in the lookback window, clusters a sample of
// them and counts the rest into the clusters found, asks the suggestion model
// about each cluster through package suggest, and reconciles what comes back
// with the suggestions already stored.
//
// Reconciliation is what lets a run repeat on a schedule. Each cluster is
// matched to a stored suggestion by centroid. One the user accepted or
// dismissed is left exactly as it is and costs no model call, so a decision
// sticks; an open one is refreshed in place; an open one no cluster matched
// any more, or one whose cluster is now covered, is superseded. New clusters
// become new open suggestions.
package suggestrun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/maximhq/bifrost/plugins/routing/complexity/suggest"
)

const (
	// matchFloor is the similarity a phrase needs to count as describing a
	// request, for clustering's view of today's routing and the suggester's
	// coverage check alike.
	matchFloor = 0.40
	// matchSimilarity is how close a cluster's centre must be to a stored
	// suggestion's to be the same request type. It is the merge threshold:
	// two clusters this close within one run would have been merged.
	matchSimilarity = 0.85
	// assignFactor bounds the requests counted into clusters after the
	// clustered sample, as a multiple of the sample: assignment is one dot
	// product per cluster per request, and past this the counts are already
	// a sample of the window.
	assignFactor = 10
	// DefaultMaxSuggestedClusters bounds the clusters one run sends to the
	// model, heaviest first; each costs one or two calls.
	DefaultMaxSuggestedClusters = 200
	// keepRuns is how many run records are kept.
	keepRuns = 200
)

// Space is the serving embedding space a run reads; *complexity.SuggestionSpace
// is one.
type Space interface {
	Namespace() string
	LivePhrases() []complexity.SuggestionPhrase
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	EachCaptured(ctx context.Context, from, to time.Time, fn func([]complexity.CapturedRequest) error) error
}

// Store is the part of the log store a run writes to.
type Store interface {
	ListComplexitySuggestions(ctx context.Context, filter logstore.ComplexitySuggestionFilter) ([]logstore.ComplexitySuggestion, error)
	ReconcileComplexitySuggestions(ctx context.Context, writes []logstore.ComplexitySuggestionWrite) ([]string, error)
	UpsertComplexitySuggestionRun(ctx context.Context, run *logstore.ComplexitySuggestionRun) error
	InsertComplexitySuggestionRun(ctx context.Context, run *logstore.ComplexitySuggestionRun) (bool, error)
	ListComplexitySuggestionRuns(ctx context.Context, filter logstore.ComplexitySuggestionRunFilter, limit int) ([]logstore.ComplexitySuggestionRun, error)
	PruneComplexitySuggestionRuns(ctx context.Context, routerID string, keep int) (int64, error)
}

// Traffic is where a run finds the requests one router served in its window,
// with their vectors. With one router that is everything captured in the
// router's embedding space, which is what a run reads when Settings.Traffic is
// nil; with several, the requests are selected from the logs by router and
// their vectors fetched by log ID.
type Traffic interface {
	EachRequest(ctx context.Context, from, to time.Time, fn func([]complexity.CapturedRequest) error) error
}

// Texts returns the request text of the given logs, keyed by log ID. A log
// that is gone or hides its content is left out.
type Texts func(ctx context.Context, logIDs []string) (map[string]string, error)

// Settings are one run's parameters.
type Settings struct {
	RunID       string
	TriggeredBy string
	// RouterID names the router whose traffic is read and whose suggestions
	// are written; required. RouterVersion is the version it serves, and
	// defaults to the space's phrase bank.
	RouterID      string
	RouterVersion string
	Now           time.Time // the window ends here; zero means time.Now
	// Lookback is how far back captured traffic is kept. Window is how much of
	// it this run reads; zero, or anything longer, reads all of it.
	Lookback time.Duration
	Window   time.Duration
	// Traffic supplies the router's requests; nil scans the space's captured
	// requests.
	Traffic Traffic
	// MaxClustered is the clustered sample's size; the rest of the window is
	// assigned.
	MaxClustered       int
	MinClusterRequests int
	// MaxSuggestedClusters bounds the clusters sent to the model; zero takes
	// DefaultMaxSuggestedClusters.
	MaxSuggestedClusters int
	// Instructions and Guidance are the suggestion prompt's editable parts;
	// empty takes the shipped ones.
	Instructions string
	Guidance     string
	Model        string // provider/model, for the run record
	Workers      int
	Seed         int64
	// Cost reports what the model calls have cost so far, read when the run
	// ends; nil records no cost.
	Cost func() float64
}

// Result is what a run did beyond its record.
type Result struct {
	Run       logstore.ComplexitySuggestionRun
	Clustered int // requests in the clustered sample
	Assigned  int // requests counted into clusters after it
	Unmatched int // requests after the sample that matched no cluster
	Skipped   int // requests past the assignment bound, not counted
}

// PromptHash identifies the prompt a run used, so a suggestion can be traced
// to the instructions that wrote it.
func PromptHash(instructions, guidance string) string {
	if instructions == "" {
		instructions = suggest.DefaultInstructions()
	}
	if guidance == "" {
		guidance = complexity.DefaultLLMClassifierGuidance()
	}
	sum := sha256.Sum256([]byte(instructions + "\x00" + guidance))
	return hex.EncodeToString(sum[:])
}

// Run runs one suggestion run and records it. The run record is written when
// the run starts and again when it ends; a failed run is recorded as failed
// and its error returned.
func Run(ctx context.Context, space Space, store Store, texts Texts, llm suggest.LLM, s Settings) (Result, error) {
	if s.Now.IsZero() {
		s.Now = time.Now()
	}
	s.Now = s.Now.UTC()
	if s.MaxSuggestedClusters <= 0 {
		s.MaxSuggestedClusters = DefaultMaxSuggestedClusters
	}
	if s.RouterID == "" {
		return Result{}, errors.New("suggestion run needs a router")
	}
	if s.RouterVersion == "" {
		if v, ok := space.(interface{ ServedVersion() string }); ok {
			s.RouterVersion = v.ServedVersion()
		}
	}
	if s.Window <= 0 || s.Window > s.Lookback {
		s.Window = s.Lookback
	}
	if s.Traffic == nil {
		s.Traffic = capturedTraffic{space}
	}
	run := logstore.ComplexitySuggestionRun{
		ID: s.RunID, RouterID: s.RouterID, RouterVersion: s.RouterVersion, TriggeredBy: s.TriggeredBy, Status: logstore.ComplexitySuggestionRunRunning,
		EmbeddingSignature: space.Namespace(), Model: s.Model, PromptHash: PromptHash(s.Instructions, s.Guidance),
		StartedAt: s.Now, WindowStart: s.Now.Add(-s.Window),
	}
	if err := store.UpsertComplexitySuggestionRun(ctx, &run); err != nil {
		return Result{}, fmt.Errorf("record suggestion run: %w", err)
	}
	result, err := run1(ctx, space, store, texts, llm, s, &run)
	finished := time.Now().UTC()
	run.FinishedAt = &finished
	if s.Cost != nil {
		run.Cost = s.Cost()
	}
	run.Status = logstore.ComplexitySuggestionRunSucceeded
	if err != nil {
		run.Status, run.Error = logstore.ComplexitySuggestionRunFailed, err.Error()
	}
	// The outcome is recorded even when the run's own context has ended, or a
	// cancelled run would read as still running.
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if recordErr := store.UpsertComplexitySuggestionRun(recordCtx, &run); recordErr != nil {
		err = errors.Join(err, fmt.Errorf("record suggestion run outcome: %w", recordErr))
	}
	_, _ = store.PruneComplexitySuggestionRuns(recordCtx, s.RouterID, keepRuns)
	result.Run = run
	return result, err
}

func run1(ctx context.Context, space Space, store Store, texts Texts, llm suggest.LLM, s Settings, run *logstore.ComplexitySuggestionRun) (Result, error) {
	if llm == nil {
		return Result{}, errors.New("no suggestion model: set the suggestions provider and model, or configure the llm block")
	}
	if s.MaxClustered <= 0 || s.Window <= 0 {
		return Result{}, errors.New("suggestion run needs a positive window and cluster budget")
	}
	from, to := s.Now.Add(-s.Window), s.Now
	live := livePhrases(space.LivePhrases())
	if len(live) == 0 {
		return Result{}, complexity.ErrNoServingGeneration
	}
	var result Result

	// 1. A uniform sample of the window: the store pages in no useful order,
	//    so reservoir sampling is what spreads the sample across the window
	//    instead of taking whatever came first.
	rng := rand.New(rand.NewSource(s.Seed))
	sample := make([]suggest.Request, 0, min(s.MaxClustered, 4096))
	var seen int
	if err := s.Traffic.EachRequest(ctx, from, to, func(page []complexity.CapturedRequest) error {
		for _, r := range page {
			seen++
			req := suggest.Request{ID: r.LogID, Count: 1, Vector: unit(r.Vector)}
			if len(sample) < s.MaxClustered {
				sample = append(sample, req)
			} else if j := rng.Intn(seen); j < s.MaxClustered {
				sample[j] = req
			}
		}
		return ctx.Err()
	}); err != nil {
		return result, err
	}
	run.RequestsRead = int64(seen)
	result.Clustered = len(sample)

	// 2. Cluster the sample, then count the rest of the window into it.
	clusterer := suggest.NewClusterer(suggest.ClusterOptions{MinRequests: s.MinClusterRequests, Seed: max(s.Seed, 1)}, live, matchFloor)
	clusterer.Add(sample)
	if seen > len(sample) {
		inSample := make(map[string]struct{}, len(sample))
		for _, r := range sample {
			inSample[r.ID] = struct{}{}
		}
		sample = nil
		limit := assignFactor * s.MaxClustered
		if err := s.Traffic.EachRequest(ctx, from, to, func(page []complexity.CapturedRequest) error {
			batch := make([]suggest.Request, 0, len(page))
			for _, r := range page {
				if _, ok := inSample[r.LogID]; ok {
					continue
				}
				if result.Assigned+len(batch) >= limit {
					result.Skipped++
					continue
				}
				batch = append(batch, suggest.Request{ID: r.LogID, Count: 1, Vector: unit(r.Vector)})
			}
			result.Unmatched += clusterer.Assign(batch)
			result.Assigned += len(batch)
			return ctx.Err()
		}); err != nil {
			return result, err
		}
	}
	clusters := clusterer.Clusters()
	run.Clusters = int64(len(clusters))

	// 3. Match clusters to stored suggestions; only open, superseded and new
	//    ones go to the model.
	stored, err := store.ListComplexitySuggestions(ctx, logstore.ComplexitySuggestionFilter{RouterID: s.RouterID})
	if err != nil {
		return result, fmt.Errorf("read stored suggestions: %w", err)
	}
	plan := matchClusters(clusters, stored, space.Namespace())
	var ask []*suggest.Cluster
	for _, cl := range clusters {
		if m, ok := plan.matched[cl]; ok && decided(m.Status) {
			continue
		}
		if len(ask) < s.MaxSuggestedClusters {
			ask = append(ask, cl)
		}
	}

	// 4. Ask the model.
	opts := suggest.Options{MatchFloor: matchFloor, Instructions: s.Instructions, Guidance: s.Guidance, Texts: texts}
	suggestions, failed, firstErr := suggestEach(ctx, suggest.NewSuggester(live, space, llm, opts), ask, s.Workers)
	if failed > 0 && failed == len(ask) {
		return result, fmt.Errorf("suggest phrases: every cluster failed: %w", firstErr)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if failed > 0 {
		// Recorded on a run that still succeeded: the clusters that answered
		// are written, and the failed ones are asked again next run.
		run.Error = fmt.Sprintf("%d of %d clusters failed and were left for the next run: %v", failed, len(ask), firstErr)
	}

	// 5. Reconcile.
	writes, created, refreshed, superseded := reconcile(suggestions, plan, stored, rowScope{
		routerID: s.RouterID, routerVersion: s.RouterVersion, namespace: space.Namespace(), runID: s.RunID,
	}, s.Now)
	// A suggestion the user accepted or dismissed while the run was going is
	// left as they decided, and not counted as refreshed or superseded.
	skipped, err := store.ReconcileComplexitySuggestions(ctx, writes)
	if err != nil {
		return result, fmt.Errorf("store suggestions: %w", err)
	}
	for _, id := range skipped {
		for _, w := range writes {
			if w.Suggestion.ID != id {
				continue
			}
			if w.Suggestion.Status == logstore.ComplexitySuggestionStatusSuperseded {
				superseded--
			} else {
				refreshed--
			}
			break
		}
	}
	run.Created, run.Refreshed, run.Superseded = int64(created), int64(refreshed), int64(superseded)
	return result, nil
}

// suggestEach asks about each cluster on workers goroutines. A cluster whose
// model call fails is counted and skipped rather than failing the others.
func suggestEach(ctx context.Context, suggester *suggest.Suggester, clusters []*suggest.Cluster, workers int) ([]suggest.Suggestion, int, error) {
	if workers <= 0 {
		workers = 4
	}
	out := make([]suggest.Suggestion, len(clusters))
	ok := make([]bool, len(clusters))
	errs := make([]error, len(clusters))
	var wg sync.WaitGroup
	slots := make(chan struct{}, workers)
	for i, cl := range clusters {
		if ctx.Err() != nil {
			errs[i] = ctx.Err()
			continue
		}
		slots <- struct{}{}
		wg.Go(func() {
			defer func() { <-slots }()
			sg, err := suggester.Suggest(ctx, cl)
			out[i], ok[i], errs[i] = sg, err == nil, err
		})
	}
	wg.Wait()
	var kept []suggest.Suggestion
	var failed int
	var first error
	for i := range clusters {
		if ok[i] {
			kept = append(kept, out[i])
			continue
		}
		failed++
		if first == nil {
			first = errs[i]
		}
	}
	return kept, failed, first
}

// decided reports whether the user has made a call on a suggestion that a
// run must leave alone.
func decided(status string) bool {
	return status == logstore.ComplexitySuggestionStatusAccepted || status == logstore.ComplexitySuggestionStatusDismissed
}

// matchPlan maps clusters to the stored suggestion each is.
type matchPlan struct {
	matched map[*suggest.Cluster]*logstore.ComplexitySuggestion
	claimed map[string]bool // stored IDs a cluster matched
}

// matchClusters pairs clusters with stored suggestions of the same embedding
// space, heaviest cluster first, each taking its most similar unclaimed
// suggestion at or above matchSimilarity.
func matchClusters(clusters []*suggest.Cluster, stored []logstore.ComplexitySuggestion, namespace string) matchPlan {
	plan := matchPlan{matched: map[*suggest.Cluster]*logstore.ComplexitySuggestion{}, claimed: map[string]bool{}}
	type candidate struct {
		row      *logstore.ComplexitySuggestion
		centroid []float32
	}
	var candidates []candidate
	for i := range stored {
		row := &stored[i]
		if row.EmbeddingSignature != namespace {
			continue
		}
		centroid, err := logstore.DecodeWarpVector(row.Centroid)
		if err != nil || len(centroid) == 0 {
			continue
		}
		candidates = append(candidates, candidate{row: row, centroid: centroid})
	}
	ordered := append([]*suggest.Cluster(nil), clusters...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Weight > ordered[j].Weight })
	for _, cl := range ordered {
		var best *logstore.ComplexitySuggestion
		bestSim := float32(matchSimilarity)
		for _, c := range candidates {
			if plan.claimed[c.row.ID] || len(c.centroid) != len(cl.Centroid) {
				continue
			}
			if sim := dot(cl.Centroid, c.centroid); sim >= bestSim {
				best, bestSim = c.row, sim
			}
		}
		if best != nil {
			plan.matched[cl] = best
			plan.claimed[best.ID] = true
		}
	}
	return plan
}

// rowScope is what every row a run writes is stamped with.
type rowScope struct {
	routerID, routerVersion, namespace, runID string
}

// reconcile turns the run's suggestions into rows to write and counts what
// changed.
//
// Each write to an existing row carries the status the run read it at, so the
// store writes it only while it is still there (see
// ReconcileComplexitySuggestions).
func reconcile(suggestions []suggest.Suggestion, plan matchPlan, stored []logstore.ComplexitySuggestion, scope rowScope, now time.Time) (writes []logstore.ComplexitySuggestionWrite, created, refreshed, superseded int) {
	namespace := scope.namespace
	supersede := func(row logstore.ComplexitySuggestion) {
		from := row.Status
		row.Status = logstore.ComplexitySuggestionStatusSuperseded
		row.ResolvedAt = &now
		writes = append(writes, logstore.ComplexitySuggestionWrite{Suggestion: row, From: from})
		superseded++
	}
	proposed := map[string]bool{}
	for _, sg := range suggestions {
		match := plan.matched[sg.Cluster]
		if sg.Category == suggest.CategoryUnreadable {
			// None of its examples could be read, so the run learned nothing
			// about it: the suggestion it matches is left as it is.
			if match != nil {
				proposed[match.ID] = true
			}
			continue
		}
		if !sg.Category.Actionable() {
			if match != nil && match.Status == logstore.ComplexitySuggestionStatusOpen {
				proposed[match.ID] = true
				supersede(*match) // its cluster is now covered, or no longer one request type
			}
			continue
		}
		row := logstore.ComplexitySuggestion{ID: uuid.NewString(), CreatedAt: now}
		from := ""
		if match != nil {
			row.ID, row.CreatedAt, from = match.ID, match.CreatedAt, match.Status
			proposed[match.ID] = true
			refreshed++
		} else {
			created++
		}
		fillSuggestion(&row, sg, scope, now)
		writes = append(writes, logstore.ComplexitySuggestionWrite{Suggestion: row, From: from})
	}
	// Open suggestions this run did not propose again are superseded: their
	// traffic stopped, or their embedding space is no longer the one served.
	for _, row := range stored {
		if row.Status != logstore.ComplexitySuggestionStatusOpen || proposed[row.ID] {
			continue
		}
		if row.EmbeddingSignature == namespace && plan.claimed[row.ID] {
			continue // matched, but not sent this run (past the cluster bound): kept as it is
		}
		supersede(row)
	}
	return writes, created, refreshed, superseded
}

// fillSuggestion writes one suggestion's content into a row.
func fillSuggestion(row *logstore.ComplexitySuggestion, sg suggest.Suggestion, scope rowScope, now time.Time) {
	row.RouterID, row.RouterVersion = scope.routerID, scope.routerVersion
	row.Status = logstore.ComplexitySuggestionStatusOpen
	row.ResolvedAt = nil
	row.Tier, row.Category, row.Reason, row.Confidence = sg.Tier, string(sg.Category), sg.Reason, sg.Confidence
	row.RequestCount = int64(sg.Cluster.Weight)
	row.Coverage = sg.Coverage
	row.NearestPhrase, row.NearestTier, row.NearestSimilarity = sg.Nearest.Text, sg.Nearest.Tier, float64(sg.NearestSim)
	row.Centroid = logstore.EncodeWarpVector(sg.Cluster.Centroid)
	row.EmbeddingSignature, row.RunID, row.LastSeenAt = scope.namespace, scope.runID, now
	_ = row.SetPhrases(sg.Phrases)
	ids := make([]string, 0, len(sg.Examples))
	for _, m := range sg.Examples {
		ids = append(ids, m.ID)
	}
	_ = row.SetExampleLogIDs(ids)
}

// capturedTraffic reads everything captured in a space, which is one router's
// traffic while a gateway has one router.
type capturedTraffic struct{ space Space }

func (t capturedTraffic) EachRequest(ctx context.Context, from, to time.Time, fn func([]complexity.CapturedRequest) error) error {
	return t.space.EachCaptured(ctx, from, to, fn)
}

func livePhrases(phrases []complexity.SuggestionPhrase) []suggest.Phrase {
	out := make([]suggest.Phrase, 0, len(phrases))
	for _, p := range phrases {
		out = append(out, suggest.Phrase{Tier: p.Tier, Text: p.Text, Vector: unit(p.Vector)})
	}
	return out
}

// unit returns v scaled to unit length; providers do not all normalize.
func unit(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return v
	}
	scale := float32(1 / math.Sqrt(sum))
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x * scale
	}
	return out
}

func dot(a, b []float32) float32 {
	var s float32
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

// Estimate is what a run over a window would do, before it runs.
type Estimate struct {
	Requests int `json:"requests"` // requests in the window
	// Clustered is the size of the sample the run would cluster; Assigned is
	// how many more it would try to assign to the clusters found (a request
	// joins one only if it is close enough); Skipped are past the bound.
	Clustered int `json:"clustered"`
	Assigned  int `json:"assigned"`
	Skipped   int `json:"skipped"`
	// MaxModelCalls bounds the suggestion model's calls: at most two per
	// cluster sent, and at most MaxSuggestedClusters clusters are sent.
	MaxModelCalls int `json:"max_model_calls"`
}

// EstimateRun sizes a run of these settings over requests requests. The
// cluster count is not known before clustering, so the model-call bound
// assumes every cluster is the smallest one kept: MinClusterRequests, or the
// clusterer's default when it is not set, as in Run.
func EstimateRun(requests int, s Settings) Estimate {
	maxSuggested := s.MaxSuggestedClusters
	if maxSuggested <= 0 {
		maxSuggested = DefaultMaxSuggestedClusters
	}
	e := Estimate{Requests: requests, Clustered: min(requests, max(0, s.MaxClustered))}
	rest := requests - e.Clustered
	e.Assigned = min(rest, assignFactor*max(0, s.MaxClustered))
	e.Skipped = rest - e.Assigned
	minCluster := s.MinClusterRequests
	if minCluster <= 0 {
		minCluster = suggest.DefaultMinClusterRequests
	}
	clusters := (e.Clustered + e.Assigned) / minCluster
	e.MaxModelCalls = 2 * min(clusters, maxSuggested)
	return e
}
