package warp

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/queryscope"
	"github.com/maximhq/bifrost/framework/sidekiq"
)

// Topic clusters.
//
// "What do people ask about?" has no aggregate in the log store: the answer
// lives in the meaning of the requests, which is exactly what the log
// vectors encode. No vector store clusters on its own, so a Sidekiq job pages
// the vectors out, clusters them here, asks the Warp model to name each
// cluster from a few of its members, and writes the result to the log store:
// one row per topic, and one row per request saying which topic it is in.
// The list_topics tool reads those tables, which turns the question into one
// tool call with a real answer, counted from rows rather than remembered from
// the run.
//
// k-means has to be told how many clusters to find, and nothing about a
// corpus's size says how many subjects are in it. So the run asks for more
// clusters than it expects to keep and then joins the ones that turned out to
// be the same subject. Too many is recoverable that way; too few is not,
// because two subjects sharing a cluster look like one large, clean topic.

const (
	// TopicsJobKind is the Sidekiq job that recomputes topic clusters.
	TopicsJobKind = "warp_topic_clustering"

	topicsPageSize = 200
	// topicsMaxVectors bounds how many requests one run takes in, which keeps
	// memory and runtime flat on a large deployment.
	topicsMaxVectors = 20000
	// topicsProgressEvery is how often, in requests, a run that is assigning
	// reports how far it has got.
	topicsProgressEvery = 5000
	// topicsMaxClusters caps k before merging. It bounds the run's cost, not
	// what a person reads: list_topics returns the largest few.
	topicsMaxClusters = 100
	// topicsOverclusterFactor is how far past the rule-of-thumb count a run
	// asks k-means to go, leaving the merge to bring it back.
	topicsOverclusterFactor = 2
	// topicsOverclusterMinAverage keeps over-clustering from producing
	// clusters too small to measure: past the rule of thumb, k only grows
	// while the average cluster would still hold this many requests.
	topicsOverclusterMinAverage = 5
	// topicsParallelMinPoints is where sharing the assignment step across
	// workers starts to pay for the goroutines.
	topicsParallelMinPoints = 512
	// topicsMinClusterSize drops singletons on a corpus large enough to have
	// real clusters. A topic of one request is that request, not a
	// pattern.
	topicsMinClusterSize = 2
	topicsSmallCorpus    = 10
	topicsLabelSamples   = 4
	// topicsLabelWorkers is how many naming calls run at once. Enough to stop
	// naming from being the slowest part of a run, few enough not to look
	// like a burst to the provider's rate limiter.
	topicsLabelWorkers     = 4
	topicsKMeansIterations = 15
	topicsMaxLabelChars    = 60
	topicsLabelTextChars   = 400
	topicsCleanupTimeout   = 30 * time.Second
	// topicsHydrationBatch is how many sample logs are asked for at once. The
	// gateway's reader answers for at most this many ids and drops the rest
	// silently, which is also why the backfill pages at the same size.
	topicsHydrationBatch = backfillBatchSize
	topicsMaxListed      = 200
	// topicsWriteBatch is how many assignments are written in one call, which
	// is also how often a run that is writing reports progress.
	topicsWriteBatch = 5000
)

// Topic is one cluster as the tool reports it.
type Topic struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Size  int    `json:"size"`
	// Share is Size over every request that has a place among the topics,
	// those that matched none included, so the model can say "a third of
	// requests" honestly.
	Share float64 `json:"share"`
	// Cohesion is how alike the topic's requests are: their average
	// similarity to its centre. A low figure beside a large size is a topic
	// that is probably several.
	Cohesion     float64  `json:"cohesion,omitempty"`
	SampleLogIDs []string `json:"example_log_ids,omitempty"`
	// SampleLinks each open one of the requests nearest the topic's
	// centre. Named as examples on the wire, because a link on a row is
	// otherwise read as "the requests behind this row".
	SampleLinks []string  `json:"example_links,omitempty"`
	ComputedAt  time.Time `json:"computed_at"`
}

// TopicSummary is one topic as the job status reports it.
type TopicSummary struct {
	Label    string  `json:"label"`
	Size     int     `json:"size"`
	Cohesion float64 `json:"cohesion"`
}

// TopicLister is what the list_topics tool needs.
type TopicLister interface {
	ListTopics(ctx context.Context, limit int) ([]Topic, int, error)
}

// TopicReader reads the topics for the tool.
type TopicReader struct {
	store  configstore.WarpStore
	topics logstore.WarpTopicStore
}

// NewTopicReader builds a reader over the log store's topic tables.
func NewTopicReader(store configstore.WarpStore, topics logstore.WarpTopicStore) *TopicReader {
	return &TopicReader{store: store, topics: topics}
}

// ListTopics returns the active topics by size, largest first, and the number
// of requests that have a place among them.
//
// Only the topics built with the embedding model Warp is configured with are
// listed: a topic's centre means nothing against vectors from another model,
// and a changed configuration is read as a deployment with no topics yet
// rather than one with stale ones.
func (r *TopicReader) ListTopics(ctx context.Context, limit int) ([]Topic, int, error) {
	if r == nil || r.store == nil || r.topics == nil {
		return nil, 0, ErrUnavailable
	}
	row, err := r.store.GetWarpConfig(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("read Warp configuration: %w", err)
	}
	config := configFromRow(row)
	if limit < 1 || limit > topicsMaxListed {
		limit = topicsMaxListed
	}
	rows, err := r.topics.ListWarpTopics(ctx, logstore.WarpTopicFilter{
		Kinds: []string{logstore.WarpTopicKindTopic}, Statuses: []string{logstore.WarpTopicStatusActive},
		EmbeddingSignature: embeddingConfigSignature(config), OmitCentroids: true,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("list topics: %w", err)
	}
	if len(rows) == 0 {
		return nil, 0, nil
	}
	// Counted over the active topics and the requests that matched none.
	// Requests still filed under a topic that has since been retired are
	// neither: they belong to a set of topics that is no longer the answer.
	ids := make([]string, 0, len(rows)+1)
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	usage, err := r.topics.SummarizeWarpTopicAssignments(ctx, logstore.WarpTopicAssignmentFilter{TopicIDs: append(ids, "")})
	if err != nil {
		return nil, 0, fmt.Errorf("count topic requests: %w", err)
	}
	sizes := make(map[string]int, len(usage))
	total := 0
	for _, entry := range usage {
		sizes[entry.TopicID] = int(entry.Requests)
		total += int(entry.Requests)
	}
	topics := make([]Topic, 0, len(rows))
	for _, row := range rows {
		topic := Topic{ID: row.ID, Label: row.Label, Size: sizes[row.ID], Cohesion: row.Cohesion, ComputedAt: row.CreatedAt.UTC()}
		for _, logID := range row.ExampleLogIDs() {
			topic.SampleLogIDs = append(topic.SampleLogIDs, logID)
			topic.SampleLinks = append(topic.SampleLinks, logDetailLink(logID))
		}
		topics = append(topics, topic)
	}
	sort.SliceStable(topics, func(i, j int) bool {
		if topics[i].Size != topics[j].Size {
			return topics[i].Size > topics[j].Size
		}
		return topics[i].Label < topics[j].Label
	})
	if len(topics) > limit {
		topics = topics[:limit]
	}
	for index := range topics {
		if total > 0 {
			topics[index].Share = float64(topics[index].Size) / float64(total)
		}
	}
	return topics, total, nil
}

// propertyInt reads an integer property whatever type the backend handed it
// back as. Most decode numbers; Redis stores every field as text and returns
// it that way. A value outside int's range saturates at its bound rather
// than wrapping, which a plain conversion does on a 32-bit build.
func propertyInt(value interface{}) int {
	switch number := value.(type) {
	case int:
		return number
	case int64:
		if number > math.MaxInt {
			return math.MaxInt
		}
		if number < math.MinInt {
			return math.MinInt
		}
		return int(number)
	case float64:
		return floatToInt(number)
	case float32:
		return floatToInt(float64(number))
	case string:
		if parsed, err := strconv.ParseFloat(strings.TrimSpace(number), 64); err == nil {
			return floatToInt(parsed)
		}
		return 0
	default:
		return 0
	}
}

// floatToInt truncates toward zero, saturating at int's bounds; NaN is 0.
// The upper bound is -float64(math.MinInt), which is exact, rather than
// float64(math.MaxInt), which rounds up past the largest int.
func floatToInt(number float64) int {
	switch {
	case math.IsNaN(number):
		return 0
	case number >= -float64(math.MinInt):
		return math.MaxInt
	case number < float64(math.MinInt):
		return math.MinInt
	default:
		return int(number)
	}
}

// propertyBool reads a boolean property whatever type the backend handed it
// back as: a boolean, or the text or number a store without one keeps.
func propertyBool(value interface{}) bool {
	switch flag := value.(type) {
	case bool:
		return flag
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(flag))
		return err == nil && parsed
	default:
		return propertyInt(value) != 0
	}
}

// listTopicsTool answers "what do people ask about" from the computed
// clusters.
func listTopicsTool() Tool {
	return Tool{
		name: "list_topics",
		description: "List the topics people talk to the models about, largest first, from clusters computed over the indexed requests. " +
			"Use this first for questions about what users ask, what conversations are about, or which topics are most common. " +
			"Sizes and shares count requests, not conversations or people: every model call is one request, so a five-turn chat is five. " +
			"Each topic carries a size, its share of clustered requests, a cohesion score (how alike its requests are, 0 to 1), " +
			"and example_links that each open one example request from the topic. Nothing links to a whole topic.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "limit": {"type": "integer", "minimum": 1, "maximum": 50, "description": "Topics to return, largest first. Default 15."}
  }
}`,
		execute: func(ctx context.Context, deps *ToolDeps, args map[string]any) (any, error) {
			if deps.topics == nil {
				return nil, fmt.Errorf("topic clusters are not available: connect a vector store and configure embeddings")
			}
			// Topics cover the whole deployment, and a topic is not a row: the
			// store's row-level scope has nothing to apply to. A caller it
			// restricts is sent to the sample instead, which it does scope. Any
			// scope counts as a restriction, since one cannot be inspected, and
			// so does the turn's resolved scope, which a deployment's caller
			// restriction can narrow without putting a scope on the context.
			if !deps.scope.Unrestricted || queryscope.FromContext(ctx) != nil || queryscope.DimensionFromContext(ctx) != nil {
				return map[string]any{
					"available": false,
					"hint": "Topics are computed across the whole deployment, so they are not available to someone whose access covers part of it. " +
						"Instead, take one bounded sample with query_logs (include_content, limit 25) and summarise the themes, saying it is a sample of what they can see.",
				}, nil
			}
			limit, err := intArg(args, "limit", 15, 50)
			if err != nil {
				return nil, err
			}
			topics, total, err := deps.topics.ListTopics(ctx, limit)
			if err != nil {
				return nil, err
			}
			out := map[string]any{
				"topics":          topics,
				"returned":        len(topics),
				"total_clustered": total,
			}
			if len(topics) > 0 {
				computedAt := topics[0].ComputedAt
				for _, topic := range topics[1:] {
					if topic.ComputedAt.After(computedAt) {
						computedAt = topic.ComputedAt
					}
				}
				out["computed_at"] = computedAt.Format(time.RFC3339)
				out["scope"] = "every indexed request the last run read, across all users, teams and customers"
				out["cannot_answer"] = "These topics are one snapshot, over the requests the last run read. There are no topics for a different time range, " +
					"for one team, customer, user, model or app, no trend for a topic, no list of every request in a topic, and no cost, latency or errors per topic. " +
					"Asked for one of those, say it is not available yet; do not approximate it from other tools."
				out["counts"] = "size, share and total_clustered count requests. Every model call is one request, so a chat of five turns " +
					"or an agent making five calls is five. Say requests, not conversations or people."
				out["links"] = "Each entry of example_links opens one example request from its topic, not the topic. " +
					"The Logs page cannot show a topic's requests, so leave topic names unlinked and offer the examples as examples."
			} else {
				out["hint"] = "No topic clusters have been computed yet. An administrator can compute them from Warp settings. " +
					"Until then, take one bounded sample with query_logs (include_content, limit 25) and summarise the themes, saying it is a sample."
			}
			return out, nil
		},
	}
}

// RegisterTopics binds the clustering job to the shared Sidekiq runner.
func (s *Service) RegisterTopics(runner *sidekiq.Runner) {
	if runner == nil || s.vectorStore == nil || s.logs == nil || s.store == nil {
		return
	}
	runner.Register(TopicsJobKind, s.RunTopicsJob)
}

// topicStore returns the log store's topic tables under the read lock, or nil
// on a deployment whose log reader cannot reach them.
func (s *Service) topicStore() logstore.WarpTopicStore {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return topicStoreOf(s.logs)
}

// topicStoreOf returns the topic tables behind a reader, or nil when the
// reader has none to offer.
func topicStoreOf(logs LogReader) logstore.WarpTopicStore {
	provider, ok := logs.(TopicStoreProvider)
	if !ok {
		return nil
	}
	return provider.TopicStore()
}

// BuildTopicsJobMeta freezes the window and embedding space for one run. It
// reuses the backfill's checkpoint shape so the same status endpoint and the
// same progress card serve both jobs. A config without an embedding provider
// or model is ErrUnavailable from Config, not ErrInvalidConfig: IsConfigured
// requires both, so only the time range is a caller error here.
func (s *Service) BuildTopicsJobMeta(ctx context.Context, start, end time.Time) (string, error) {
	if s.vectorStore == nil || s.logs == nil || s.store == nil {
		return "", ErrUnavailable
	}
	if s.topicStore() == nil {
		return "", fmt.Errorf("%w: the log store cannot keep topics", ErrUnavailable)
	}
	config, err := s.Config(ctx)
	if err != nil {
		return "", err
	}
	if !end.After(start) {
		return "", fmt.Errorf("%w: end_time must be after start_time", ErrInvalidConfig)
	}
	return marshalBackfillMeta(BackfillJobMeta{
		StartTime:       start.UTC(),
		EndTime:         end.UTC(),
		ConfigSignature: embeddingConfigSignature(config),
		Namespace:       config.EffectiveLogVectorStoreNamespace(),
	})
}

// topicVector is one indexed request as the clustering sees it. The
// vector is unit length.
type topicVector struct {
	logID  string
	vector []float32
}

// RunTopicsJob is the Sidekiq handler: read, cluster, merge, assign, label,
// write. A run either replaces the previous topics or leaves them as they
// were.
func (s *Service) RunTopicsJob(ctx context.Context, job tables.TableSidekiqJob, progress sidekiq.ProgressFunc) (string, error) {
	var meta BackfillJobMeta
	if err := sonic.Unmarshal([]byte(job.Metadata), &meta); err != nil {
		return job.Metadata, fmt.Errorf("parse Warp topics metadata: %w", err)
	}
	snapshot := func() string {
		encoded, err := marshalBackfillMeta(meta)
		if err != nil {
			return job.Metadata
		}
		return encoded
	}
	checkpoint := func(message string) error {
		meta.Message = message
		return progress(snapshot())
	}

	config, err := s.Config(ctx)
	if err != nil {
		return snapshot(), err
	}
	if embeddingConfigSignature(config) != meta.ConfigSignature {
		return snapshot(), fmt.Errorf("Warp embedding configuration changed while topics were being computed")
	}
	topicStore := s.topicStore()
	if topicStore == nil {
		return snapshot(), fmt.Errorf("%w: the log store cannot keep topics", ErrUnavailable)
	}

	// 1-3. Read the window, cluster it in batches, merge the batches' clusters,
	// and give whatever is read after that to the nearest topic.
	limits := s.topicLimits
	if limits.batch == 0 {
		limits = defaultTopicLimits()
	}
	run := newTopicRun(limits, topicsSeed(meta))
	stage := "clustering"
	if err := checkpoint("Reading the requests in the window."); err != nil {
		return snapshot(), err
	}
	err = s.readTopicWindow(ctx, config, limits, meta.StartTime, meta.EndTime, func(entry topicVector) error {
		before := run.batches
		if err := run.add(ctx, entry); err != nil {
			return err
		}
		read := run.read()
		switch {
		case run.merged && stage == "clustering":
			stage = "assigning"
			meta.Scanned, meta.Total = read, int64(read)
			return checkpoint(fmt.Sprintf("Clustered %d request(s) into %d topic(s); assigning the rest.", run.clustered, len(run.clusters)))
		case run.batches != before || (run.merged && read%topicsProgressEvery == 0):
			meta.Scanned, meta.Total = read, int64(read)
			return checkpoint(fmt.Sprintf("Read %d request(s); %s.", read, stage))
		}
		return nil
	})
	if errors.Is(err, errTopicReadLimit) {
		// The window holds more than one run takes in. What was read is
		// clustered, and the run says that it stopped.
		meta.Capped, err = true, nil
	}
	if err == nil {
		err = run.finish(ctx)
	}
	meta.Scanned, meta.Total = run.read(), int64(run.read())
	if err != nil {
		if ctx.Err() != nil {
			meta.Message = "Stopped while " + stage + "."
			_ = progress(snapshot())
			return snapshot(), ctx.Err()
		}
		meta.LastError = err.Error()
		return snapshot(), err
	}
	if run.read() == 0 {
		meta.Message = "No indexed requests in this window; nothing to cluster."
		return snapshot(), nil
	}
	clusters := run.clusters
	meta.Merged, meta.MergeThreshold = run.folded, run.threshold
	meta.Clustered, meta.Assigned, meta.Unassigned = run.clustered, run.assigned, run.dropped+run.unmatched
	if err := checkpoint(fmt.Sprintf("Found %d cluster(s) after merging %d; labelling.", len(clusters), meta.Merged)); err != nil {
		return snapshot(), err
	}

	// 4. Label. After the merge, so a subject that was split is named once.
	labelCtx := withBackgroundWork(ctx)
	failed, lastError := labelTopics(labelCtx, s.chatFuncFor(labelCtx, config, ""), config, clusters, s.topicSampleTexts(ctx, clusters))
	if ctx.Err() != nil {
		meta.Message = "Stopped while labelling."
		_ = progress(snapshot())
		return snapshot(), ctx.Err()
	}
	if failed > 0 {
		// A model that cannot be reached must not sink the whole run: the
		// clusters are still real, so they get a fallback name and the
		// failure is counted where the operator can see it.
		meta.Failed += failed
		meta.LastError = lastError
	}
	clusters, meta.MergedByName = joinTopicsByName(clusters)

	// 5. Write the topics, then every request's place among them, then retire
	// the previous topics. In that order, so there is no moment with no
	// topics, and a run that cannot finish writing retires what it wrote and
	// leaves the previous topics as they were.
	if err := checkpoint(fmt.Sprintf("Writing %d topic(s).", len(clusters))); err != nil {
		return snapshot(), err
	}
	// What this run replaces is what TopicReader serves: active topics under
	// this embedding model. Themes and other models' topics are not its to
	// retire. Centroids are read, because retiring rewrites the whole row.
	previous, err := topicStore.ListWarpTopics(ctx, logstore.WarpTopicFilter{
		Kinds: []string{logstore.WarpTopicKindTopic}, Statuses: []string{logstore.WarpTopicStatusActive},
		EmbeddingSignature: meta.ConfigSignature,
	})
	if err != nil {
		meta.LastError = err.Error()
		return snapshot(), fmt.Errorf("read previous topics: %w", err)
	}
	written, err := writeTopics(ctx, topicStore, &meta, run, clusters, limits.write)
	if err != nil {
		meta.LastError = err.Error()
		meta.Message = "Could not write the new topics; the previous topics were kept."
		meta.Indexed, meta.Topics = 0, nil
		// Best effort, and on a context of its own: the run may be failing
		// because its context is gone, and what it wrote must be taken back
		// regardless. Retired rather than deleted, so the assignments already
		// written against them stay consistent and simply stop counting.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), topicsCleanupTimeout)
		retireErr := retireTopics(cleanupCtx, topicStore, written)
		cancel()
		if retireErr != nil {
			// The topics it wrote are still active, so the run says so.
			meta.LastError += fmt.Sprintf("; retire written topics: %v", retireErr)
			return snapshot(), fmt.Errorf("write topics: %w; retire written topics: %v", err, retireErr)
		}
		return snapshot(), fmt.Errorf("write topics: %w", err)
	}
	if err := retireTopics(ctx, topicStore, previous); err != nil {
		// The new topics are in place and are what list_topics serves, so
		// this is reported rather than failing a run that did its job.
		meta.LastError = fmt.Sprintf("retire previous topics: %v", err)
	}
	meta.Message = fmt.Sprintf("Clustered %d request(s) into %d topic(s).", meta.Clustered, meta.Indexed)
	if meta.Assigned > 0 {
		meta.Message += fmt.Sprintf(" %d more were given to the nearest topic.", meta.Assigned)
	}
	if meta.Unassigned > 0 {
		meta.Message += fmt.Sprintf(" %d matched no topic.", meta.Unassigned)
	}
	if meta.Skipped > 0 {
		meta.Message += fmt.Sprintf(" %d were indexed but are no longer in the log store.", meta.Skipped)
	}
	if meta.Capped {
		meta.Message += fmt.Sprintf(" Stopped at the limit of %d request(s) for one run; the window holds more, and these were drawn from across it.", limits.read)
	}
	if meta.MergeThreshold != nil {
		meta.Message += fmt.Sprintf(" Merged %d near-duplicate cluster(s) at similarity %.2f or above.", meta.Merged, *meta.MergeThreshold)
	}
	if meta.MergedByName > 0 {
		meta.Message += fmt.Sprintf(" %d more joined for sharing a name.", meta.MergedByName)
	}
	return snapshot(), progress(snapshot())
}

// writeTopics writes the run's topics and the place of every request it read,
// and returns the topics it wrote, whether or not it finished.
//
// Each request's row copies the columns the log store answers topic
// questions from, read back from the log rather than from the vector's
// metadata: the vector carries a lossy copy of them, and the log is what the
// access scope on a restricted caller's reads is written against. A request
// whose log has gone since it was indexed has no row to copy and is counted
// as skipped.
//
// Each batch replaces the rows its requests already had. The store has no
// transaction to span the batches (ClickHouse has none), so the rows a batch
// replaces are read first and put back if the run fails, and the rows it adds
// are removed: a run that stops partway leaves every request under the topic
// it had. The unmatched pool is written only after every batch has landed.
func writeTopics(ctx context.Context, store logstore.WarpTopicStore, meta *BackfillJobMeta, run *topicRun, clusters []topicCluster, batchSize int) (_ []logstore.WarpTopic, err error) {
	if batchSize <= 0 {
		batchSize = topicsWriteBatch
	}
	now := time.Now().UTC()
	topics := make([]logstore.WarpTopic, 0, len(clusters))
	summaries := make([]TopicSummary, 0, len(clusters))
	// members is every request that ended in a topic; pooled every one that
	// ended in none, with its vector.
	members := make(map[string]topicMember, run.read())
	memberTopic := make(map[string]string, run.read())
	for _, cluster := range clusters {
		topic := logstore.WarpTopic{
			ID: uuid.NewString(), Kind: logstore.WarpTopicKindTopic, Label: cluster.label,
			Centroid: logstore.EncodeWarpVector(cluster.centroid()), AssignFloor: float64(run.floor), Cohesion: cluster.cohesion(),
			EmbeddingSignature: meta.ConfigSignature, Status: logstore.WarpTopicStatusActive, CreatedAt: now,
		}
		if err := topic.SetExampleLogIDs(cluster.sampleIDs); err != nil {
			return nil, fmt.Errorf("encode topic examples: %w", err)
		}
		topics = append(topics, topic)
		summaries = append(summaries, TopicSummary{Label: cluster.label, Size: cluster.size, Cohesion: roundTo(cluster.cohesion(), 3)})
		for _, member := range cluster.members {
			members[member.logID] = member
			memberTopic[member.logID] = topic.ID
		}
	}
	if err := store.UpsertWarpTopics(ctx, topics); err != nil {
		return nil, err
	}
	meta.Indexed, meta.Topics = len(topics), summaries

	// Every unmatched request is filed under no topic, which also replaces a
	// row an earlier run wrote against a topic this run retires. Only the
	// pooled ones, which the cap bounds, go on to the unmatched pool.
	ids := make([]string, 0, len(members)+len(run.unmatchedIDs))
	for logID := range members {
		ids = append(ids, logID)
	}
	listed := make(map[string]struct{}, len(run.unmatchedIDs))
	unmatchedID := func(logID string) {
		if _, inTopic := members[logID]; inTopic {
			return
		}
		if _, dup := listed[logID]; dup {
			return
		}
		listed[logID] = struct{}{}
		ids = append(ids, logID)
	}
	for _, logID := range run.unmatchedIDs {
		unmatchedID(logID)
	}
	pooled := make(map[string][]float32, len(run.pooled))
	for _, entry := range run.pooled {
		if _, inTopic := members[entry.logID]; inTopic {
			continue
		}
		pooled[entry.logID] = entry.vector
		unmatchedID(entry.logID)
	}
	sort.Strings(ids)
	var (
		replaced []logstore.WarpTopicAssignment
		added    []string
	)
	defer func() {
		if err == nil || (len(replaced) == 0 && len(added) == 0) {
			return
		}
		// On a context of its own: the run may be failing because its context
		// is gone, and the previous rows must go back regardless.
		restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), topicsCleanupTimeout)
		defer cancel()
		if restoreErr := restoreTopicAssignments(restoreCtx, store, replaced, added); restoreErr != nil {
			err = fmt.Errorf("%w; restore previous assignments: %v", err, restoreErr)
		}
	}()
	found := 0
	var unmatched []logstore.WarpTopicAssignment
	for start := 0; start < len(ids); start += batchSize {
		if err := ctx.Err(); err != nil {
			return topics, err
		}
		batch := ids[start:min(start+batchSize, len(ids))]
		rows, err := store.ListLogsForWarpTopics(ctx, batch, meta.StartTime, meta.EndTime)
		if err != nil {
			return topics, fmt.Errorf("read requests: %w", err)
		}
		found += len(rows)
		assignments := make([]logstore.WarpTopicAssignment, 0, len(rows))
		batchIDs := make([]string, 0, len(rows))
		for _, row := range rows {
			row.AssignedAt = now
			if member, ok := members[row.LogID]; ok {
				row.TopicID, row.Similarity = memberTopic[row.LogID], float64(member.similarity)
			} else if _, ok := pooled[row.LogID]; ok {
				unmatched = append(unmatched, row)
			}
			assignments = append(assignments, row)
			batchIDs = append(batchIDs, row.LogID)
		}
		// Every request in the batch is gone from the log store. An empty id
		// filter is no filter, so reading by it would take other requests'
		// rows into replaced, and a failed run would write them back.
		if len(batchIDs) == 0 {
			continue
		}
		// The job's context carries no access scope, so this reads every row.
		previous, err := store.ListWarpTopicAssignments(ctx, logstore.WarpTopicAssignmentFilter{LogIDs: batchIDs}, len(batchIDs))
		if err != nil {
			return topics, fmt.Errorf("read previous assignments: %w", err)
		}
		had := make(map[string]struct{}, len(previous))
		for _, row := range previous {
			had[row.LogID] = struct{}{}
		}
		for _, logID := range batchIDs {
			if _, ok := had[logID]; !ok {
				added = append(added, logID)
			}
		}
		replaced = append(replaced, previous...)
		if err := store.UpsertWarpTopicAssignments(ctx, assignments); err != nil {
			return topics, fmt.Errorf("write assignments: %w", err)
		}
		meta.Message = fmt.Sprintf("Wrote %d topic(s); filed %d of %d request(s).", len(topics), min(start+batchSize, len(ids)), len(ids))
	}
	for start := 0; start < len(unmatched); start += batchSize {
		batch := unmatched[start:min(start+batchSize, len(unmatched))]
		rows := make([]logstore.WarpTopicUnmatched, 0, len(batch))
		for _, row := range batch {
			rows = append(rows, logstore.WarpTopicUnmatched{
				LogID: row.LogID, Timestamp: row.Timestamp, LogCreatedAt: row.LogCreatedAt,
				Vector: logstore.EncodeWarpVector(pooled[row.LogID]), EmbeddingSignature: meta.ConfigSignature,
			})
		}
		if err := store.AddWarpTopicUnmatched(ctx, rows); err != nil {
			return topics, fmt.Errorf("pool unmatched requests: %w", err)
		}
	}
	// A request that is in a topic now is no longer waiting for one.
	inTopics := make([]string, 0, len(members))
	for logID := range members {
		inTopics = append(inTopics, logID)
	}
	if err := store.DeleteWarpTopicUnmatched(ctx, inTopics); err != nil {
		return topics, fmt.Errorf("clear pooled requests: %w", err)
	}
	meta.Skipped = len(ids) - found
	return topics, nil
}

// restoreTopicAssignments puts back the rows a failed run replaced and
// removes the ones it added.
func restoreTopicAssignments(ctx context.Context, store logstore.WarpTopicStore, replaced []logstore.WarpTopicAssignment, added []string) error {
	if err := store.UpsertWarpTopicAssignments(ctx, replaced); err != nil {
		return err
	}
	return store.DeleteWarpTopicAssignments(ctx, added)
}

// retireTopics marks topics as taking nothing new. Their rows stay, so the
// assignments written against them still resolve.
func retireTopics(ctx context.Context, store logstore.WarpTopicStore, topics []logstore.WarpTopic) error {
	if len(topics) == 0 {
		return nil
	}
	for index := range topics {
		topics[index].Status = logstore.WarpTopicStatusRetired
	}
	return store.UpsertWarpTopics(ctx, topics)
}

// topicsSeed starts k-means from the same place for the same window over the
// same embedding space, so recomputing data that has not changed gives the
// same topics rather than a reshuffle of them.
func topicsSeed(meta BackfillJobMeta) int64 {
	hash := fnv.New64a()
	fmt.Fprintf(hash, "%s|%d|%d", meta.ConfigSignature, meta.StartTime.Unix(), meta.EndTime.Unix())
	return int64(hash.Sum64())
}

// labelTopics names every cluster, a few at a time, and reports how many fell
// back to a name made from their own words and the last reason why.
func labelTopics(ctx context.Context, chat ChatFunc, config *schemas.WarpConfig, clusters []topicCluster, texts map[string]string) (int, string) {
	var (
		mu        sync.Mutex
		group     sync.WaitGroup
		failed    int
		lastError string
	)
	slots := make(chan struct{}, topicsLabelWorkers)
	for index := range clusters {
		if ctx.Err() != nil {
			break
		}
		slots <- struct{}{}
		group.Add(1)
		go func(cluster *topicCluster) {
			defer func() {
				<-slots
				group.Done()
			}()
			samples := make([]string, 0, len(cluster.sampleIDs))
			for _, id := range cluster.sampleIDs {
				if text := texts[id]; text != "" {
					samples = append(samples, text)
				}
			}
			label, err := labelTopic(ctx, chat, config, samples)
			if err != nil {
				mu.Lock()
				failed++
				lastError = err.Error()
				mu.Unlock()
				label = fallbackTopicLabel(samples)
			}
			cluster.label = label
		}(&clusters[index])
	}
	group.Wait()
	return failed, lastError
}

// topicSampleTexts hydrates every cluster's sample logs, a batch at a time,
// and returns what the users asked, by log id. Content-hidden rows are left out:
// a label must never be derived from text the operator asked not to keep.
func (s *Service) topicSampleTexts(ctx context.Context, clusters []topicCluster) map[string]string {
	ids := make([]string, 0, len(clusters)*topicsLabelSamples)
	for _, cluster := range clusters {
		ids = append(ids, cluster.sampleIDs...)
	}
	if len(ids) == 0 {
		return nil
	}
	// Batch hydration is an optional capability of the reader, the same one
	// semantic search depends on. Without it the clusters keep their fallback
	// labels.
	hydrator, ok := s.logs.(SemanticHydrator)
	if !ok {
		return nil
	}
	texts := make(map[string]string, len(ids))
	for start := 0; start < len(ids); start += topicsHydrationBatch {
		logs, err := hydrator.GetLogsByIDs(ctx, ids[start:min(start+topicsHydrationBatch, len(ids))])
		if err != nil {
			// The clusters in this batch are named from their own words.
			continue
		}
		for index := range logs {
			if text := logUserText(&logs[index]); text != "" {
				texts[logs[index].ID] = text
			}
		}
	}
	return texts
}

// logUserText is the user's side of a conversation as a cluster is named
// from it, or nothing for a row whose content is hidden.
func logUserText(entry *logstore.Log) string {
	if entry == nil || entry.ContentHidden {
		return ""
	}
	opening, last := conversationUserTexts(entry)
	if opening == "" {
		return truncateText(last, topicsLabelTextChars)
	}
	// The question first and in full measure: it is the subject. The last
	// message follows because it can narrow what was asked.
	return truncateText(opening, topicsLabelTextChars) + " (later: " + truncateText(last, topicsLabelTextChars/4) + ")"
}

const topicLabelInstructions = "You name topics for a dashboard. Given a few user messages from one cluster of requests, " +
	"reply with a short topic name of two to five words that a product manager would recognise, in title case, " +
	"with no punctuation, no quotes and no explanation. Name the subject, not the wording."

// labelTopic asks the Warp model for a name. No model means the fallback.
func labelTopic(ctx context.Context, chat ChatFunc, config *schemas.WarpConfig, texts []string) (string, error) {
	if chat == nil || len(texts) == 0 {
		return fallbackTopicLabel(texts), nil
	}
	var prompt strings.Builder
	prompt.WriteString("User messages:\n")
	for _, text := range texts {
		prompt.WriteString("- ")
		prompt.WriteString(text)
		prompt.WriteString("\n")
	}
	prompt.WriteString("\nTopic name:")
	itemType := schemas.ResponsesMessageTypeMessage
	role := schemas.ResponsesInputMessageRoleUser
	content := prompt.String()
	instructions := topicLabelInstructions
	provider, model := requestTarget(config)
	response, bifrostErr := chat(ctx, &schemas.BifrostResponsesRequest{
		Provider: provider,
		Model:    model,
		Input: []schemas.ResponsesMessage{{
			Type:    &itemType,
			Role:    &role,
			Content: &schemas.ResponsesMessageContent{ContentStr: &content},
		}},
		Params: &schemas.ResponsesParameters{Instructions: &instructions},
	})
	if bifrostErr != nil {
		return "", fmt.Errorf("label topic: %s", errorMessage(bifrostErr))
	}
	if response == nil {
		return "", fmt.Errorf("label topic: the model returned no output")
	}
	label := cleanTopicLabel(responsesText(response.Output))
	if label == "" {
		return fallbackTopicLabel(texts), nil
	}
	return label, nil
}

// cleanTopicLabel trims a model's answer down to a label: first line, no
// quotes or trailing punctuation, bounded length.
func cleanTopicLabel(raw string) string {
	label := strings.TrimSpace(raw)
	if index := strings.IndexAny(label, "\n\r"); index >= 0 {
		label = label[:index]
	}
	label = strings.Trim(label, "\"'`*#.:- ")
	label = strings.TrimSpace(label)
	if utf8.RuneCountInString(label) > topicsMaxLabelChars {
		label = strings.TrimSpace(string([]rune(label)[:topicsMaxLabelChars]))
	}
	return label
}

// topicStopWords are dropped from fallback labels; they carry no subject.
var topicStopWords = map[string]struct{}{}

func init() {
	for _, word := range strings.Fields("a an the and or but if then of to in on at for with by from as is are was were be been being am do does did doing have has had having i me my we our you your it its this that these those what which who whom how why when where can could should would will shall may might must not no yes please get got give tell show me want need about into over under again there here all any some more most other than too very just also up down out off so s t don didn isn aren") {
		topicStopWords[word] = struct{}{}
	}
}

// fallbackTopicLabel names a cluster from the words its questions share most.
// Not as good as a model's name, but always available and never wrong about
// what the requests contain.
func fallbackTopicLabel(texts []string) string {
	counts := map[string]int{}
	order := []string{}
	for _, text := range texts {
		for _, raw := range strings.Fields(strings.ToLower(text)) {
			word := strings.Trim(raw, ".,;:!?\"'()[]{}")
			if len(word) < 3 {
				continue
			}
			if _, stop := topicStopWords[word]; stop {
				continue
			}
			if counts[word] == 0 {
				order = append(order, word)
			}
			counts[word]++
		}
	}
	if len(order) == 0 {
		return "Untitled topic"
	}
	sort.SliceStable(order, func(i, j int) bool { return counts[order[i]] > counts[order[j]] })
	if len(order) > 3 {
		order = order[:3]
	}
	return strings.Join(order, " ")
}

// topicClusterCount picks how many clusters to ask k-means for.
//
// It starts from the square root of half the count, the usual rule of thumb,
// and goes past it by topicsOverclusterFactor so the merge has something to
// work with. The overshoot stops where the average cluster would get too
// small to measure, which leaves a handful of requests at the rule of
// thumb.
func topicClusterCount(n int) int {
	if n <= 0 {
		return 0
	}
	base := max(int(math.Round(math.Sqrt(float64(n)/2))), 1)
	k := max(base, min(base*topicsOverclusterFactor, n/topicsOverclusterMinAverage))
	return min(k, n, topicsMaxClusters)
}

// forEachRange runs work over [0, n) split across the available processors,
// or inline when n is too small for the goroutines to pay for themselves.
func forEachRange(n int, work func(lo, hi int)) {
	workers := min(runtime.GOMAXPROCS(0), n/topicsParallelMinPoints)
	if workers < 2 {
		work(0, n)
		return
	}
	var group sync.WaitGroup
	size := (n + workers - 1) / workers
	for lo := 0; lo < n; lo += size {
		group.Add(1)
		go func(lo, hi int) {
			defer group.Done()
			work(lo, hi)
		}(lo, min(lo+size, n))
	}
	group.Wait()
}

// kMeans clusters unit vectors by cosine similarity. Centroids are seeded with
// k-means++ so two seeds rarely land in the same group, then refined for a
// bounded number of iterations. Returns each point's cluster and the unit
// centroids; an empty cluster keeps its last centroid.
//
// Both halves compare every point with every centroid, which is where a run
// spends its time, so both are shared across processors. Seeding keeps each
// point's best similarity so far and compares only with the newest seed:
// comparing with every seed again for each new one costs k times as much and
// finds the same answer. The result depends on the seed alone, not on how the
// work was divided.
//
// The context is checked between passes over the points, which is as often
// as a cancelled run can be noticed without slowing the passes themselves.
func kMeans(ctx context.Context, points [][]float32, k, iterations int, seed int64) ([]int, [][]float32, error) {
	n := len(points)
	if n == 0 || k <= 0 {
		return nil, nil, nil
	}
	k = min(k, n)
	rng := rand.New(rand.NewSource(seed))
	dimension := len(points[0])

	centroids := make([][]float32, 0, k)
	centroids = append(centroids, append([]float32(nil), points[rng.Intn(n)]...))
	nearest := make([]float32, n)
	distances := make([]float64, n)
	for len(centroids) < k {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		newest := centroids[len(centroids)-1]
		forEachRange(n, func(lo, hi int) {
			for index := lo; index < hi; index++ {
				nearest[index] = max(nearest[index], dot(points[index], newest))
				// Distance in cosine terms: 1 - similarity, squared as k-means++ does.
				distance := 1 - float64(nearest[index])
				distances[index] = distance * distance
			}
		})
		total := 0.0
		for _, distance := range distances {
			total += distance
		}
		if total == 0 {
			// Every point already sits on a centroid; any remaining seed is a
			// duplicate, so stop with fewer clusters.
			break
		}
		target := rng.Float64() * total
		chosen := n - 1
		for index, distance := range distances {
			target -= distance
			if target <= 0 {
				chosen = index
				break
			}
		}
		centroids = append(centroids, append([]float32(nil), points[chosen]...))
	}
	k = len(centroids)

	assignments := make([]int, n)
	sums := make([][]float32, k)
	for cluster := range sums {
		sums[cluster] = make([]float32, dimension)
	}
	counts := make([]int, k)
	for iteration := 0; iteration < iterations; iteration++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		var changed sync.Once
		moved := false
		forEachRange(n, func(lo, hi int) {
			for index := lo; index < hi; index++ {
				best, bestScore := 0, float32(math.Inf(-1))
				for cluster, centroid := range centroids {
					if score := dot(points[index], centroid); score > bestScore {
						best, bestScore = cluster, score
					}
				}
				if assignments[index] != best {
					assignments[index] = best
					changed.Do(func() { moved = true })
				}
			}
		})
		for cluster := range sums {
			clear(sums[cluster])
			counts[cluster] = 0
		}
		for index, point := range points {
			cluster := assignments[index]
			counts[cluster]++
			for d, value := range point {
				sums[cluster][d] += value
			}
		}
		for cluster := range centroids {
			if counts[cluster] == 0 {
				continue
			}
			centroids[cluster] = unitVector(sums[cluster])
		}
		if !moved && iteration > 0 {
			break
		}
	}
	return assignments, centroids, nil
}

// unitVector scales a vector to length one; a zero vector stays zero.
func unitVector(vector []float32) []float32 {
	var norm float64
	for _, value := range vector {
		norm += float64(value) * float64(value)
	}
	out := make([]float32, len(vector))
	if norm == 0 {
		return out
	}
	scale := float32(1 / math.Sqrt(norm))
	for index, value := range vector {
		out[index] = value * scale
	}
	return out
}

// dot is the cosine similarity of two unit vectors.
func dot(a, b []float32) float32 {
	var sum float32
	for index := range min(len(a), len(b)) {
		sum += a[index] * b[index]
	}
	return sum
}
