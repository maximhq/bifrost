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

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/queryscope"
	"github.com/maximhq/bifrost/framework/sidekiq"
	"github.com/maximhq/bifrost/framework/vectorstore"
)

// Topic clusters.
//
// "What do people ask about?" has no aggregate in the log store: the answer
// lives in the meaning of the requests, which is exactly what the log
// vectors encode. Weaviate does not cluster on its own, so a Sidekiq job pages
// the vectors out, clusters them here, asks the Warp model to name each
// cluster from a few of its members, and writes one centroid per topic into a
// namespace beside the logs. The list_topics tool then reads that namespace,
// which turns the question into one tool call with a real answer.
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
)

// topicsNamespaceFor names the namespace that holds a log namespace's topic
// centroids. Derived rather than configured so the two always travel
// together, including through retirement when the embedding model changes.
func topicsNamespaceFor(logNamespace string) string {
	return logNamespace + "Topics"
}

// Topic is one cluster as the tool reports it.
type Topic struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Size  int    `json:"size"`
	// Share is Size over every request that was clustered in the same
	// run, so the model can say "a third of requests" honestly.
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
	WindowStart time.Time `json:"window_start"`
	WindowEnd   time.Time `json:"window_end"`
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

// TopicReader reads computed topics for the tool.
type TopicReader struct {
	store   configstore.WarpStore
	vectors vectorstore.VectorStore

	// ensured is the namespace and dimension last declared to the store, so
	// a read declares them once per configuration rather than every time.
	ensureMu         sync.Mutex
	ensuredNamespace string
	ensuredDimension int
}

// ensureNamespace declares the topics namespace before it is read. Stores
// that keep a namespace's dimension in memory cannot page it otherwise, and
// the process answering a question is rarely the one that computed the
// topics. A configuration with no dimension yet has no topics to read.
func (r *TopicReader) ensureNamespace(ctx context.Context, namespace string, dimension int) error {
	if dimension <= 0 {
		return nil
	}
	r.ensureMu.Lock()
	defer r.ensureMu.Unlock()
	if r.ensuredNamespace == namespace && r.ensuredDimension == dimension {
		return nil
	}
	if err := ensureTopicsNamespace(ctx, r.vectors, namespace, dimension); err != nil {
		return err
	}
	r.ensuredNamespace, r.ensuredDimension = namespace, dimension
	return nil
}

// NewTopicReader builds a reader over the configured topics namespace.
func NewTopicReader(store configstore.WarpStore, vectors vectorstore.VectorStore) *TopicReader {
	return &TopicReader{store: store, vectors: vectors}
}

// ListTopics returns topics by size, largest first, and the number of
// requests the run clustered.
func (r *TopicReader) ListTopics(ctx context.Context, limit int) ([]Topic, int, error) {
	if r == nil || r.store == nil || r.vectors == nil {
		return nil, 0, ErrUnavailable
	}
	row, err := r.store.GetWarpConfig(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("read Warp configuration: %w", err)
	}
	config := configFromRow(row)
	namespace := topicsNamespaceFor(config.EffectiveLogVectorStoreNamespace())
	if limit < 1 || limit > topicsMaxListed {
		limit = topicsMaxListed
	}
	if err := r.ensureNamespace(ctx, namespace, config.EmbeddingDimension); err != nil {
		return nil, 0, fmt.Errorf("prepare topics namespace: %w", err)
	}
	results, _, err := r.vectors.GetAll(ctx, namespace,
		[]vectorstore.Query{{Field: "warp_topic", Operator: vectorstore.QueryOperatorEqual, Value: true}},
		[]string{"topic_id", "label", "size", "cohesion_milli", "sample_log_ids", "window_start", "window_end", "computed_at", "total_clustered", "run_id", "run_topics"},
		nil, int64(topicsMaxListed))
	if err != nil {
		return nil, 0, fmt.Errorf("list topics: %w", err)
	}
	results = newestCompleteTopicRun(results)
	topics := make([]Topic, 0, len(results))
	total := 0
	for _, result := range results {
		topic := topicFromProperties(result.ID, result.Properties)
		if topic.Label == "" {
			continue
		}
		total = max(total, propertyInt(result.Properties["total_clustered"]))
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

// newestCompleteTopicRun keeps the topics of one run: the most recent that
// wrote everything it set out to.
//
// A run writes its topics before it removes the previous run's, so for a
// moment the namespace holds both, and the new set is partial until the last
// write lands. Every topic records how many its run wrote, which is what lets
// a partial set be told from a whole one. With no whole run there is nothing
// to answer from.
func newestCompleteTopicRun(results []vectorstore.SearchResult) []vectorstore.SearchResult {
	type run struct {
		entries    []vectorstore.SearchResult
		expected   int
		computedAt int
	}
	runs := map[string]*run{}
	for _, result := range results {
		computedAt := propertyInt(result.Properties["computed_at"])
		key, _ := result.Properties["run_id"].(string)
		if key == "" {
			key = fmt.Sprintf("at-%d", computedAt)
		}
		entry, ok := runs[key]
		if !ok {
			entry = &run{expected: propertyInt(result.Properties["run_topics"]), computedAt: computedAt}
			runs[key] = entry
		}
		entry.entries = append(entry.entries, result)
	}
	var newest *run
	for _, entry := range runs {
		if len(entry.entries) < entry.expected {
			continue
		}
		if newest == nil || entry.computedAt > newest.computedAt {
			newest = entry
		}
	}
	if newest == nil {
		return nil
	}
	return newest.entries
}

// topicFromProperties decodes one stored centroid's metadata.
func topicFromProperties(id string, properties map[string]interface{}) Topic {
	topic := Topic{ID: id, Size: propertyInt(properties["size"])}
	if topicID, ok := properties["topic_id"].(string); ok && topicID != "" {
		topic.ID = topicID
	}
	topic.Label, _ = properties["label"].(string)
	topic.Cohesion = float64(propertyInt(properties["cohesion_milli"])) / 1000
	if raw, ok := properties["sample_log_ids"].(string); ok && raw != "" {
		topic.SampleLogIDs = strings.Split(raw, ",")
		for _, logID := range topic.SampleLogIDs {
			topic.SampleLinks = append(topic.SampleLinks, logDetailLink(logID))
		}
	}
	topic.WindowStart = time.Unix(int64(propertyInt(properties["window_start"])), 0).UTC()
	topic.WindowEnd = time.Unix(int64(propertyInt(properties["window_end"])), 0).UTC()
	topic.ComputedAt = time.Unix(int64(propertyInt(properties["computed_at"])), 0).UTC()
	return topic
}

// propertyInt reads an integer property whatever type the backend handed it
// back as. Most decode numbers; Redis stores every field as text and returns
// it that way.
func propertyInt(value interface{}) int {
	switch number := value.(type) {
	case int:
		return number
	case int64:
		return int(number)
	case float64:
		return int(number)
	case float32:
		return int(number)
	case string:
		if parsed, err := strconv.ParseFloat(strings.TrimSpace(number), 64); err == nil {
			return int(parsed)
		}
		return 0
	default:
		return 0
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
			// scope counts as a restriction, since one cannot be inspected.
			if queryscope.FromContext(ctx) != nil || queryscope.DimensionFromContext(ctx) != nil {
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
				out["window"] = map[string]string{
					"start": topics[0].WindowStart.Format(time.RFC3339),
					"end":   topics[0].WindowEnd.Format(time.RFC3339),
				}
				out["computed_at"] = topics[0].ComputedAt.Format(time.RFC3339)
				out["scope"] = "every indexed request in the topic window, across all users, teams and customers"
				out["cannot_answer"] = "These topics are one snapshot, over the window above. There are no topics for a different time range, " +
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

// BuildTopicsJobMeta freezes the window and embedding space for one run. It
// reuses the backfill's checkpoint shape so the same status endpoint and the
// same progress card serve both jobs.
func (s *Service) BuildTopicsJobMeta(ctx context.Context, start, end time.Time) (string, error) {
	if s.vectorStore == nil || s.logs == nil || s.store == nil {
		return "", ErrUnavailable
	}
	config, err := s.Config(ctx)
	if err != nil {
		return "", err
	}
	if !config.IsConfigured() || config.EmbeddingProvider == "" || config.EmbeddingModel == "" {
		return "", fmt.Errorf("%w: configure an embedding provider and model before computing topics", ErrInvalidConfig)
	}
	if !end.After(start) {
		return "", fmt.Errorf("%w: end_time must be after start_time", ErrInvalidConfig)
	}
	return marshalBackfillMeta(BackfillJobMeta{
		StartTime:       start.UTC(),
		EndTime:         end.UTC(),
		ConfigSignature: embeddingConfigSignature(config),
		Namespace:       topicsNamespaceFor(config.EffectiveLogVectorStoreNamespace()),
	})
}

// topicVector is one indexed request as the clustering sees it. The
// vector is unit length.
type topicVector struct {
	logID  string
	vector []float32
}

// RunTopicsJob is the Sidekiq handler: read, cluster, merge, assign, label,
// replace. A run either replaces the previous topics or leaves them as they
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

	// 5. Write the new topics, then remove the previous ones. In that order,
	// so there is no moment with no topics, and a run that cannot finish
	// writing takes back what it wrote and leaves the previous topics alone.
	dimension := config.EmbeddingDimension
	if len(clusters) > 0 && len(clusters[0].sum) > 0 {
		dimension = len(clusters[0].sum)
	}
	if err := ensureTopicsNamespace(ctx, s.vectorStore, meta.Namespace, dimension); err != nil {
		meta.LastError = err.Error()
		return snapshot(), err
	}
	runID := job.ID
	if runID == "" {
		runID = uuid.NewString()
	}
	isTopic := vectorstore.Query{Field: "warp_topic", Operator: vectorstore.QueryOperatorEqual, Value: true}
	now := time.Now().UTC()
	summaries := make([]TopicSummary, 0, len(clusters))
	for _, cluster := range clusters {
		id := uuid.NewString()
		metadata := map[string]interface{}{
			"warp_topic": true, "topic_id": id, "label": cluster.label, "size": int64(cluster.size),
			"cohesion_milli": int64(math.Round(cluster.cohesion() * 1000)),
			"sample_log_ids": strings.Join(cluster.sampleIDs, ","), "total_clustered": int64(meta.Scanned),
			"window_start": meta.StartTime.Unix(), "window_end": meta.EndTime.Unix(), "computed_at": now.Unix(),
			"run_id": runID, "run_topics": int64(len(clusters)),
		}
		if err := s.vectorStore.Add(ctx, meta.Namespace, id, cluster.centroid(), metadata); err != nil {
			meta.LastError = err.Error()
			meta.Message = "Could not write the new topics; the previous topics were kept."
			// Best effort, and on a context of its own: the run may be failing
			// because its context is gone, and leftovers must go regardless.
			// Anything that survives is a partial run, which list_topics
			// already refuses to answer from.
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), topicsCleanupTimeout)
			_, _ = s.vectorStore.DeleteAll(cleanupCtx, meta.Namespace, []vectorstore.Query{isTopic, {Field: "run_id", Operator: vectorstore.QueryOperatorEqual, Value: runID}})
			cancel()
			return snapshot(), fmt.Errorf("write topics: %w", err)
		}
		summaries = append(summaries, TopicSummary{Label: cluster.label, Size: cluster.size, Cohesion: roundTo(cluster.cohesion(), 3)})
	}
	meta.Indexed, meta.Topics = len(summaries), summaries

	// Earlier runs are matched both ways: by run, and by age for topics
	// written before runs were recorded, which a run filter cannot see.
	for _, stale := range []vectorstore.Query{
		{Field: "run_id", Operator: vectorstore.QueryOperatorNotEqual, Value: runID},
		{Field: "computed_at", Operator: vectorstore.QueryOperatorLessThan, Value: now.Unix()},
	} {
		if _, err := s.vectorStore.DeleteAll(ctx, meta.Namespace, []vectorstore.Query{isTopic, stale}); err != nil {
			// The new topics are in place and are what list_topics serves, so
			// this is reported rather than failing a run that did its job.
			meta.LastError = fmt.Sprintf("clear previous topics: %v", err)
		}
	}
	meta.Message = fmt.Sprintf("Clustered %d request(s) into %d topic(s).", meta.Clustered, meta.Indexed)
	if meta.Assigned > 0 {
		meta.Message += fmt.Sprintf(" %d more were given to the nearest topic.", meta.Assigned)
	}
	if meta.Unassigned > 0 {
		meta.Message += fmt.Sprintf(" %d matched no topic.", meta.Unassigned)
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
	if len(label) > topicsMaxLabelChars {
		label = strings.TrimSpace(label[:topicsMaxLabelChars])
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

// ensureTopicsNamespace creates the centroid namespace with the properties
// the reader filters and reads.
func ensureTopicsNamespace(ctx context.Context, vectors vectorstore.VectorStore, namespace string, dimension int) error {
	if vectors == nil {
		return ErrNoVectorStore
	}
	if dimension <= 0 {
		return fmt.Errorf("%w: embedding dimension must be positive", ErrInvalidConfig)
	}
	properties := map[string]vectorstore.VectorStoreProperties{
		"warp_topic":      {DataType: vectorstore.VectorStorePropertyTypeBoolean, Description: "Marks Warp topic centroids"},
		"topic_id":        {DataType: vectorstore.VectorStorePropertyTypeString, Description: "Topic ID"},
		"label":           {DataType: vectorstore.VectorStorePropertyTypeString, Description: "Topic name"},
		"size":            {DataType: vectorstore.VectorStorePropertyTypeInteger, Description: "Requests in the topic"},
		"cohesion_milli":  {DataType: vectorstore.VectorStorePropertyTypeInteger, Description: "Average member similarity to the centroid, in thousandths"},
		"total_clustered": {DataType: vectorstore.VectorStorePropertyTypeInteger, Description: "Requests clustered in the same run"},
		"sample_log_ids":  {DataType: vectorstore.VectorStorePropertyTypeString, Description: "Comma-separated sample log IDs"},
		"window_start":    {DataType: vectorstore.VectorStorePropertyTypeInteger, Description: "Window start, Unix seconds"},
		"window_end":      {DataType: vectorstore.VectorStorePropertyTypeInteger, Description: "Window end, Unix seconds"},
		"computed_at":     {DataType: vectorstore.VectorStorePropertyTypeInteger, Description: "Computed at, Unix seconds"},
		"run_id":          {DataType: vectorstore.VectorStorePropertyTypeString, Description: "The run that wrote the topic"},
		"run_topics":      {DataType: vectorstore.VectorStorePropertyTypeInteger, Description: "Topics the run set out to write"},
	}
	return vectors.CreateNamespace(ctx, namespace, dimension, properties)
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
