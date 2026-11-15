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
	"strings"
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
	// DefaultMinClusterShare raises the smallest cluster a run keeps with
	// the window's size: a cluster must hold at least 0.1% of the requests
	// read (6 up to 6,000 requests, 27 at 27,000, 100 at 100,000), so a busy
	// gateway is not asked about thousands of small request types and the
	// phrases a run suggests fit the phrase bank.
	DefaultMinClusterShare = 0.001
	// callsPerCluster bounds one cluster's model calls: two phrase attempts
	// and one tier decision.
	callsPerCluster = 3
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
	UpdateComplexitySuggestionRunStatus(ctx context.Context, id string, from []string, to string, finishedAt *time.Time) (bool, error)
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
	// MinClusterShare raises MinClusterRequests with the window's size: a
	// run keeps only clusters holding at least this share of the requests it
	// read. Zero takes DefaultMinClusterShare.
	MinClusterShare float64
	// MaxPhrases is the phrase bank's capacity. A run suggests no more
	// phrases than the bank has room for (MaxPhrases less the live phrases),
	// keeping the heaviest clusters' phrases. Zero means no bound.
	MaxPhrases int
	// MaxSuggestedClusters bounds the clusters sent to the model; zero takes
	// DefaultMaxSuggestedClusters.
	MaxSuggestedClusters int
	// Instructions are the phrase step's editable prompt; empty takes the
	// shipped one.
	Instructions string
	// Tier picks each cluster's tier; required. TierPrompt identifies what
	// it was told (classifier, model, instructions, definitions, examples) so
	// the run's prompt hash changes when any of it does.
	Tier       suggest.TierDecider
	TierPrompt string
	Model      string // the phrase model's provider/model, for the run record
	Workers    int
	Seed       int64
	// Cost reports what the model calls have cost so far, read when the run
	// ends; nil records no cost.
	Cost func() float64
	// Cancelled reports whether a user cancelled the run. It is asked only
	// when the run's context ended: a cancelled run is recorded as cancelled
	// rather than failed. Nil records every early end as a failure.
	Cancelled func(ctx context.Context) bool
	// Backfill, when set, fills the window with the requests the router took
	// before it captured them, from their logs, and reports how many it
	// added. It runs first, as the run's backfilling stage, so the run reads
	// a complete window.
	Backfill func(ctx context.Context, from, to time.Time) (int, error)
}

// progressSteps is how many progress writes the suggesting stage makes at
// most: one each time another tenth of the groups has been asked about.
const progressSteps = 10

// Result is what a run did beyond its record.
type Result struct {
	Run        logstore.ComplexitySuggestionRun
	Clustered  int // requests in the clustered sample
	Assigned   int // requests counted into clusters after it
	Unmatched  int // requests after the sample that matched no cluster
	Skipped    int // requests past the assignment bound, not counted
	Backfilled int // requests embedded from their logs before the run read the window
}

// PromptHash identifies the prompts a run used, so a suggestion can be traced
// to the instructions that wrote its phrases and picked its tier.
func PromptHash(instructions, tierPrompt string) string {
	if instructions == "" {
		instructions = suggest.DefaultInstructions()
	}
	sum := sha256.Sum256([]byte(instructions + "\x00" + tierPrompt))
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
		EmbeddingSignature: space.Namespace(), Model: s.Model, PromptHash: PromptHash(s.Instructions, s.TierPrompt),
		StartedAt: s.Now, WindowStart: s.Now.Add(-s.Window),
	}
	// Written on a context of its own, like the outcome below: a run whose
	// context has already ended (a cancel that came as it started) is then
	// still recorded, and ends through the outcome below.
	startCtx, cancelStart := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	err := store.UpsertComplexitySuggestionRun(startCtx, &run)
	cancelStart()
	if err != nil {
		return Result{}, fmt.Errorf("record suggestion run: %w", err)
	}
	result, err := run1(ctx, space, store, texts, llm, s, &run)
	finished := time.Now().UTC()
	run.FinishedAt = &finished
	run.Stage = ""
	if s.Cost != nil {
		run.Cost = s.Cost()
	}
	// The outcome is recorded even when the run's own context has ended, or a
	// cancelled run would read as still running.
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	switch {
	case err == nil:
		run.Status = logstore.ComplexitySuggestionRunSucceeded
	case (errors.Is(err, context.Canceled) || ctx.Err() != nil) && s.Cancelled != nil && s.Cancelled(recordCtx):
		// Stopped on purpose: not a failure, and nothing was written past
		// the stage it reached. The run's own context is checked too, since
		// not every error wraps the cancel it came from (the Weaviate
		// client's do not).
		run.Status, run.Error, err = logstore.ComplexitySuggestionRunCancelled, "", nil
	default:
		run.Status, run.Error = logstore.ComplexitySuggestionRunFailed, err.Error()
	}
	if recordErr := store.UpsertComplexitySuggestionRun(recordCtx, &run); recordErr != nil {
		err = errors.Join(err, fmt.Errorf("record suggestion run outcome: %w", recordErr))
	}
	_, _ = store.PruneComplexitySuggestionRuns(recordCtx, s.RouterID, keepRuns)
	result.Run = run
	return result, err
}

func run1(ctx context.Context, space Space, store Store, texts Texts, llm suggest.LLM, s Settings, run *logstore.ComplexitySuggestionRun) (Result, error) {
	if llm == nil {
		return Result{}, errors.New("no suggestion model: set the suggestions provider and model")
	}
	if s.Tier == nil {
		return Result{}, errors.New("no tier model: set the suggestions tier classifier and model")
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
	if s.Backfill != nil {
		run.Stage = logstore.ComplexitySuggestionRunStageBackfilling
		if err := store.UpsertComplexitySuggestionRun(ctx, run); err != nil {
			return result, fmt.Errorf("record suggestion run progress: %w", err)
		}
		n, err := s.Backfill(ctx, from, to)
		if err != nil {
			return result, fmt.Errorf("backfill the window from logs: %w", err)
		}
		result.Backfilled = n
	}
	run.Stage = logstore.ComplexitySuggestionRunStageReading
	if err := store.UpsertComplexitySuggestionRun(ctx, run); err != nil {
		return result, fmt.Errorf("record suggestion run progress: %w", err)
	}

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
	run.RequestsClustered = int64(result.Clustered)
	run.Stage = logstore.ComplexitySuggestionRunStageClustering
	if err := store.UpsertComplexitySuggestionRun(ctx, run); err != nil {
		return result, fmt.Errorf("record suggestion run progress: %w", err)
	}

	// 2. Cluster the sample, then count the rest of the window into it. The
	//    smallest cluster kept scales with the requests clustered and counted,
	//    so a busy gateway is not asked about thousands of small request types;
	//    requests past the assignment bound are in no cluster, so they do not
	//    raise it.
	minCluster := ClusterFloor(s.MinClusterRequests, s.MinClusterShare, representedRequests(seen, s.MaxClustered))
	clusterer := suggest.NewClusterer(suggest.ClusterOptions{MinRequests: minCluster, Seed: max(s.Seed, 1)}, live, matchFloor)
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
	run.RequestsAssigned, run.RequestsSkipped = int64(result.Assigned), int64(result.Skipped)
	for _, cl := range clusters {
		run.RequestsGrouped += int64(cl.Weight)
	}

	// 3. Match clusters to stored suggestions; only open, superseded and new
	//    ones go to the model.
	stored, err := store.ListComplexitySuggestions(ctx, logstore.ComplexitySuggestionFilter{RouterID: s.RouterID})
	if err != nil {
		return result, fmt.Errorf("read stored suggestions: %w", err)
	}
	plan := matchClusters(clusters, stored, space.Namespace())
	// The phrase bank's room is not a bound on the clusters asked: a cluster
	// can come back covered or incoherent with no phrase at all. How many
	// phrases a cluster yields is known only once it is asked, so suggestEach
	// stops once the phrases written fill the room.
	room := -1
	limit := s.MaxSuggestedClusters
	if s.MaxPhrases > 0 {
		room = max(0, s.MaxPhrases-len(live))
	}
	var ask []*suggest.Cluster
	for _, cl := range clusters {
		if m, ok := plan.matched[cl]; ok && decided(m.Status) {
			run.ClustersDecided++
			continue
		}
		if len(ask) < limit {
			ask = append(ask, cl)
		} else {
			run.ClustersOverLimit++
		}
	}
	run.ClustersAsked = int64(len(ask))
	run.Stage = logstore.ComplexitySuggestionRunStageSuggesting
	if err := store.UpsertComplexitySuggestionRun(ctx, run); err != nil {
		return result, fmt.Errorf("record suggestion run progress: %w", err)
	}

	// 4. Ask the model. Progress is written each time another tenth of the
	//    groups is done, under one lock so the writes are in order; a failed
	//    progress write only costs the reader a stale count.
	var progressMu sync.Mutex
	var done int
	step := max(1, len(ask)/progressSteps)
	progress := func() {
		progressMu.Lock()
		defer progressMu.Unlock()
		done++
		if done%step != 0 || done == len(ask) {
			return
		}
		run.ClustersDone = int64(done)
		_ = store.UpsertComplexitySuggestionRun(ctx, run)
	}
	opts := suggest.Options{MatchFloor: matchFloor, Instructions: s.Instructions, Tier: s.Tier, Texts: texts}
	suggestions, asked, failed, firstErr := suggestEach(ctx, suggest.NewSuggester(live, space, llm, opts), ask, s.Workers, room, progress)
	run.ClustersOverLimit += int64(len(ask) - asked)
	run.ClustersAsked, run.ClustersDone, run.ClustersFailed = int64(asked), int64(asked), int64(failed)
	// Clusters still being asked when the room filled can overflow it; the
	// lighter ones are left out like the clusters never asked.
	suggestions = FitSuggestions(suggestions, room, &run.ClustersOverLimit)
	for _, sg := range suggestions {
		switch sg.Category {
		case suggest.CategoryCovered:
			run.ClustersCovered++
		case suggest.CategoryUnreadable:
			run.ClustersUnreadable++
		case suggest.CategoryIncoherent:
			run.ClustersIncoherent++
		}
	}
	// A run whose context ended was stopped, not failed: the clusters' errors
	// are what that ending did to the calls in flight, and a model adapter's
	// error need not wrap the context's, so it is checked first.
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if failed > 0 && failed == asked {
		return result, fmt.Errorf("suggest phrases: every cluster failed: %w", firstErr)
	}
	if failed > 0 {
		// Recorded on a run that still succeeded: the clusters that answered
		// are written, and the failed ones are asked again next run.
		run.Error = fmt.Sprintf("%d of %d clusters failed and were left for the next run: %v", failed, asked, firstErr)
	}

	// 5. Reconcile.
	run.Stage = logstore.ComplexitySuggestionRunStageSaving
	if err := store.UpsertComplexitySuggestionRun(ctx, run); err != nil {
		return result, fmt.Errorf("record suggestion run progress: %w", err)
	}
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

// suggestEach asks about clusters in order, heaviest first, on workers
// goroutines. A cluster whose model call fails is counted and skipped rather
// than failing the others. With a room of zero or more it starts no cluster
// once the distinct phrases written so far fill the room; asked is how many
// it started. done, when set, is called once per cluster as it finishes.
func suggestEach(ctx context.Context, suggester *suggest.Suggester, clusters []*suggest.Cluster, workers, room int, done func()) (kept []suggest.Suggestion, asked, failed int, first error) {
	if workers <= 0 {
		workers = 4
	}
	out := make([]suggest.Suggestion, len(clusters))
	ok := make([]bool, len(clusters))
	errs := make([]error, len(clusters))
	var mu sync.Mutex
	written := map[string]bool{}
	full := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return room >= 0 && len(written) >= room
	}
	var wg sync.WaitGroup
	slots := make(chan struct{}, workers)
	for i, cl := range clusters {
		if ctx.Err() != nil {
			errs[i] = ctx.Err()
			asked = i + 1
			continue
		}
		if full() {
			break
		}
		slots <- struct{}{}
		// Answers that came in while waiting for a worker may have filled it.
		if full() {
			<-slots
			break
		}
		asked = i + 1
		wg.Go(func() {
			defer func() { <-slots }()
			sg, err := suggester.Suggest(ctx, cl)
			out[i], ok[i], errs[i] = sg, err == nil, err
			if err == nil && sg.Category.Actionable() {
				mu.Lock()
				for _, phrase := range sg.Phrases {
					if key := normalizeText(phrase); key != "" {
						written[key] = true
					}
				}
				mu.Unlock()
			}
			if done != nil {
				done()
			}
		})
	}
	wg.Wait()
	for i := range asked {
		if ok[i] {
			kept = append(kept, out[i])
			continue
		}
		failed++
		if first == nil {
			first = errs[i]
		}
	}
	return kept, asked, failed, first
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
	_ = row.SetCurrentRouting(currentRouting(sg.Cluster))
}

// currentRouting is how a cluster's requests route today, by tier, with the
// ones that match no phrase above the floor under
// ComplexitySuggestionUnmatchedTier.
func currentRouting(cl *suggest.Cluster) map[string]int64 {
	routing := make(map[string]int64, 4)
	for tier, n := range cl.CurrentRouting() {
		if n <= 0 {
			continue
		}
		if tier == "" {
			tier = logstore.ComplexitySuggestionUnmatchedTier
		}
		routing[tier] += int64(n)
	}
	return routing
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
	// MaxModelCalls bounds the model calls: at most three per cluster sent
	// (two phrase attempts and one tier decision), and at most
	// MaxSuggestedClusters clusters are sent.
	MaxModelCalls int `json:"max_model_calls"`
	// Backfill is how many of the window's requests the run would first embed
	// from their logs, because they came before the router captured them; they
	// are counted in Requests.
	Backfill int `json:"backfill,omitempty"`
	// BackfillUnknown is true when what the run would embed from logs could
	// not be planned; Requests then counts only what is captured.
	BackfillUnknown bool `json:"backfill_unknown,omitempty"`
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
	minCluster := ClusterFloor(s.MinClusterRequests, s.MinClusterShare, e.Clustered+e.Assigned)
	clusters := (e.Clustered + e.Assigned) / minCluster
	e.MaxModelCalls = callsPerCluster * min(clusters, maxSuggested)
	return e
}

// representedRequests is how many of seen requests a run can place in
// clusters: the clustered sample and the requests counted into it after,
// at most assignFactor times the sample.
func representedRequests(seen, maxClustered int) int {
	return min(seen, (assignFactor+1)*max(0, maxClustered))
}

// ClusterFloor is the smallest cluster a run keeps: the configured minimum,
// raised to share of the requests read when that is larger. A share of zero
// takes DefaultMinClusterShare; a negative one leaves the minimum alone.
func ClusterFloor(minRequests int, share float64, requests int) int {
	if minRequests <= 0 {
		minRequests = suggest.DefaultMinClusterRequests
	}
	if share == 0 {
		share = DefaultMinClusterShare
	}
	if share > 0 {
		minRequests = max(minRequests, int(math.Ceil(share*float64(requests))))
	}
	return minRequests
}

// FitSuggestions is what a run keeps of what the model wrote, in one pass in
// the order the clusters were asked (heaviest first). Each phrase stays with
// the first kept suggestion written it; a lighter suggestion left with no
// phrase of its own is covered by that one when that one has the same tier.
// When a phrase it was written went to another tier, the two clusters
// disagree rather than one covering the other, so it is left out of the run,
// keeping the stored suggestion its cluster matches as it was (reconcile
// leaves a matched row it is not given untouched). A suggestion whose new
// phrases do not fit the room left in the phrase bank is dropped and counted,
// and reserves nothing, so a lighter one written one of its phrases can still
// keep it. Non-actionable outcomes, and actionable ones without phrases of
// their own (a misrouted cluster whose phrase repeats a live one), add no
// phrases and are always kept, so reconciliation still sees them. A negative
// room means no bound.
func FitSuggestions(suggestions []suggest.Suggestion, room int, dropped *int64) []suggest.Suggestion {
	seen := map[string]string{} // phrase key -> the tier of the suggestion that kept it
	kept := suggestions[:0]
	used := 0
	for _, sg := range suggestions {
		if !sg.Category.Actionable() || len(sg.Phrases) == 0 {
			kept = append(kept, sg)
			continue
		}
		var unique, keys []string
		mine := map[string]bool{}
		conflict := false
		for _, phrase := range sg.Phrases {
			key := strings.ToLower(strings.TrimSpace(phrase))
			if key == "" || mine[key] {
				continue
			}
			if tier, taken := seen[key]; taken {
				conflict = conflict || tier != sg.Tier
				continue
			}
			mine[key] = true
			unique = append(unique, phrase)
			keys = append(keys, key)
		}
		if len(unique) == 0 {
			if conflict {
				continue
			}
			sg.Phrases = []string{}
			sg.Category = suggest.CategoryCovered
			kept = append(kept, sg)
			continue
		}
		if room >= 0 && used+len(unique) > room {
			*dropped++
			continue
		}
		used += len(unique)
		for _, key := range keys {
			seen[key] = sg.Tier
		}
		sg.Phrases = unique
		kept = append(kept, sg)
	}
	return kept
}

// WithinRoom keeps the suggestions whose phrases fit the room left in the
// phrase bank, heaviest cluster first (the order they were asked in), and
// counts the actionable ones that did not fit. Non-actionable outcomes add no
// phrases and are always kept, so reconciliation still supersedes what they
// cover. A negative room means no bound.
func WithinRoom(suggestions []suggest.Suggestion, room int, dropped *int64) []suggest.Suggestion {
	if room < 0 {
		return suggestions
	}
	kept := suggestions[:0]
	used := 0
	for _, sg := range suggestions {
		if sg.Category.Actionable() {
			if used+len(sg.Phrases) > room {
				*dropped++
				continue
			}
			used += len(sg.Phrases)
		}
		kept = append(kept, sg)
	}
	return kept
}

// withoutRepeatedPhrases is FitSuggestions with no bound on the room.
func withoutRepeatedPhrases(suggestions []suggest.Suggestion) []suggest.Suggestion {
	var dropped int64
	return FitSuggestions(suggestions, -1, &dropped)
}
