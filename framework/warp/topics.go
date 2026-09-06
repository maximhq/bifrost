package warp

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/sidekiq"
	"github.com/maximhq/bifrost/framework/vectorstore"
)

// Topic clusters.
//
// "What do people ask about?" has no aggregate in the log store: the answer
// lives in the meaning of the conversations, which is exactly what the log
// vectors encode. Weaviate does not cluster on its own, so a Sidekiq job pages
// the vectors out, clusters them here, asks the Warp model to name each
// cluster from a few of its members, and writes one centroid per topic into a
// namespace beside the logs. The list_topics tool then reads that namespace,
// which turns the question into one tool call with a real answer.

const (
	// TopicsJobKind is the Sidekiq job that recomputes topic clusters.
	TopicsJobKind = "warp_topic_clustering"

	topicsPageSize = 200
	// topicsMaxVectors bounds one run. Past this the job clusters the newest
	// vectors in the window rather than everything, which keeps memory and
	// runtime flat on a large deployment.
	topicsMaxVectors = 20000
	// topicsMaxClusters caps k. Forty named topics is already more than a
	// person reads; past that the labels stop distinguishing anything.
	topicsMaxClusters = 40
	// topicsMinClusterSize drops singletons on a corpus large enough to have
	// real clusters. A topic of one conversation is that conversation, not a
	// pattern.
	topicsMinClusterSize   = 2
	topicsSmallCorpus      = 10
	topicsLabelSamples     = 4
	topicsKMeansIterations = 15
	topicsMaxLabelChars    = 60
	topicsMaxListed        = 200
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
	// Share is Size over every conversation that was clustered in the same
	// run, so the model can say "a third of conversations" honestly.
	Share        float64   `json:"share"`
	SampleLogIDs []string  `json:"sample_log_ids,omitempty"`
	SampleLinks  []string  `json:"sample_links,omitempty"`
	WindowStart  time.Time `json:"window_start"`
	WindowEnd    time.Time `json:"window_end"`
	ComputedAt   time.Time `json:"computed_at"`
}

// TopicLister is what the list_topics tool needs.
type TopicLister interface {
	ListTopics(ctx context.Context, limit int) ([]Topic, int, error)
}

// TopicReader reads computed topics for the tool.
type TopicReader struct {
	store   configstore.WarpStore
	vectors vectorstore.VectorStore
}

// NewTopicReader builds a reader over the configured topics namespace.
func NewTopicReader(store configstore.WarpStore, vectors vectorstore.VectorStore) *TopicReader {
	return &TopicReader{store: store, vectors: vectors}
}

// ListTopics returns topics by size, largest first, and the number of
// conversations the run clustered.
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
	results, _, err := r.vectors.GetAll(ctx, namespace,
		[]vectorstore.Query{{Field: "warp_topic", Operator: vectorstore.QueryOperatorEqual, Value: true}},
		[]string{"topic_id", "label", "size", "sample_log_ids", "window_start", "window_end", "computed_at", "total_clustered"},
		nil, int64(topicsMaxListed))
	if err != nil {
		return nil, 0, fmt.Errorf("list topics: %w", err)
	}
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

// topicFromProperties decodes one stored centroid's metadata.
func topicFromProperties(id string, properties map[string]interface{}) Topic {
	topic := Topic{ID: id, Size: propertyInt(properties["size"])}
	if topicID, ok := properties["topic_id"].(string); ok && topicID != "" {
		topic.ID = topicID
	}
	topic.Label, _ = properties["label"].(string)
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

// propertyInt reads an integer property whatever numeric type the backend
// decoded it to.
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
	default:
		return 0
	}
}

// listTopicsTool answers "what do people ask about" from the computed
// clusters.
func listTopicsTool() Tool {
	return Tool{
		name: "list_topics",
		description: "List the topics people talk to the models about, largest first, from clusters computed over the indexed conversations. " +
			"Use this first for questions about what users ask, what conversations are about, or which topics are most common. " +
			"Each topic carries a size, its share of clustered conversations, and sample links.",
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
			limit := intArg(args, "limit", 15, 50)
			topics, total, err := deps.topics.ListTopics(ctx, limit)
			if err != nil {
				return nil, err
			}
			out := map[string]any{
				"topics":          topics,
				"returned":        len(topics),
				"total_clustered": total,
				"logs_link":       logsViewLink(nil),
			}
			if len(topics) > 0 {
				out["window"] = map[string]string{
					"start": topics[0].WindowStart.Format(time.RFC3339),
					"end":   topics[0].WindowEnd.Format(time.RFC3339),
				}
				out["computed_at"] = topics[0].ComputedAt.Format(time.RFC3339)
				out["scope"] = "every indexed conversation in the topic window, across all users, teams and customers"
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

// topicVector is one indexed conversation as the clustering sees it.
type topicVector struct {
	logID  string
	vector []float32
}

// RunTopicsJob is the Sidekiq handler: read, cluster, label, replace.
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

	// 1. Read the window's vectors.
	vectors, err := s.readTopicVectors(ctx, config, meta.StartTime, meta.EndTime)
	if err != nil {
		meta.LastError = err.Error()
		return snapshot(), err
	}
	meta.Scanned, meta.Total = len(vectors), int64(len(vectors))
	if len(vectors) == 0 {
		meta.Message = "No indexed conversations in this window; nothing to cluster."
		return snapshot(), nil
	}
	if err := checkpoint(fmt.Sprintf("Read %d conversation(s); clustering.", len(vectors))); err != nil {
		return snapshot(), err
	}

	// 2. Cluster.
	points := make([][]float32, len(vectors))
	for index, entry := range vectors {
		points[index] = unitVector(entry.vector)
	}
	k := topicClusterCount(len(points))
	assignments, centroids := kMeans(points, k, topicsKMeansIterations, time.Now().UnixNano())
	minSize := topicsMinClusterSize
	if len(points) < topicsSmallCorpus {
		minSize = 1
	}
	clusters := buildTopicClusters(vectors, points, assignments, centroids, minSize)
	if err := checkpoint(fmt.Sprintf("Found %d cluster(s); labelling.", len(clusters))); err != nil {
		return snapshot(), err
	}

	// 3. Label.
	chat := s.chatFuncFor(ctx, config, "")
	for index := range clusters {
		if ctx.Err() != nil {
			meta.Message = "Stopped while labelling."
			_ = progress(snapshot())
			return snapshot(), ctx.Err()
		}
		texts := s.topicSampleTexts(ctx, clusters[index].sampleIDs)
		label, labelErr := labelTopic(ctx, chat, config, texts)
		if labelErr != nil {
			// A model that cannot be reached must not sink the whole run: the
			// clusters are still real, so they get a fallback name and the
			// failure is counted where the operator can see it.
			meta.Failed++
			meta.LastError = labelErr.Error()
			label = fallbackTopicLabel(texts)
		}
		clusters[index].label = label
	}

	// 4. Replace the previous topics.
	dimension := config.EmbeddingDimension
	if len(points) > 0 && len(points[0]) > 0 {
		dimension = len(points[0])
	}
	if err := ensureTopicsNamespace(ctx, s.vectorStore, meta.Namespace, dimension); err != nil {
		meta.LastError = err.Error()
		return snapshot(), err
	}
	if _, err := s.vectorStore.DeleteAll(ctx, meta.Namespace, []vectorstore.Query{{Field: "warp_topic", Operator: vectorstore.QueryOperatorEqual, Value: true}}); err != nil {
		meta.LastError = err.Error()
		return snapshot(), fmt.Errorf("clear previous topics: %w", err)
	}
	now := time.Now().UTC()
	for _, cluster := range clusters {
		id := uuid.NewString()
		metadata := map[string]interface{}{
			"warp_topic": true, "topic_id": id, "label": cluster.label, "size": int64(len(cluster.memberIDs)),
			"sample_log_ids": strings.Join(cluster.sampleIDs, ","), "total_clustered": int64(len(vectors)),
			"window_start": meta.StartTime.Unix(), "window_end": meta.EndTime.Unix(), "computed_at": now.Unix(),
		}
		if err := s.vectorStore.Add(ctx, meta.Namespace, id, cluster.centroid, metadata); err != nil {
			meta.Failed++
			meta.LastError = err.Error()
			continue
		}
		meta.Indexed++
	}
	meta.Message = fmt.Sprintf("Clustered %d conversation(s) into %d topic(s).", len(vectors), meta.Indexed)
	return snapshot(), progress(snapshot())
}

// readTopicVectors pages the log namespace, vectors included, and keeps the
// entries inside the window.
//
// The window is applied here rather than as a store filter: Weaviate's cursor
// paging (the only way to walk past its result ceiling) refuses a where
// clause, and the namespace holds nothing but Warp's log vectors anyway. The
// read cap bounds entries read, so on a namespace far larger than the window
// the newest entries may be missed; the cap is generous for that reason.
func (s *Service) readTopicVectors(ctx context.Context, config *schemas.WarpConfig, start, end time.Time) ([]topicVector, error) {
	namespace := config.EffectiveLogVectorStoreNamespace()
	readCtx := vectorstore.WithIncludeVectors(vectorstore.WithDisableScanFallback(ctx))
	var cursor *string
	vectors := make([]topicVector, 0, topicsPageSize)
	read := 0
	for {
		results, next, err := s.vectorStore.GetAll(readCtx, namespace, nil, []string{"log_id", "timestamp", "warp_log"}, cursor, topicsPageSize)
		if err != nil {
			return nil, fmt.Errorf("read log vectors: %w", err)
		}
		read += len(results)
		for _, result := range results {
			logID, _ := result.Properties["log_id"].(string)
			isLog, _ := result.Properties["warp_log"].(bool)
			at := int64(propertyInt(result.Properties["timestamp"]))
			if logID == "" || !isLog || len(result.Vector) == 0 || at < start.Unix() || at > end.Unix() {
				continue
			}
			vectors = append(vectors, topicVector{logID: logID, vector: result.Vector})
		}
		if next == nil || len(results) == 0 || read >= topicsMaxVectors {
			break
		}
		cursor = next
	}
	return vectors, nil
}

// topicCluster is one cluster before it is written.
type topicCluster struct {
	centroid  []float32
	memberIDs []string
	sampleIDs []string
	label     string
}

// buildTopicClusters groups members under their centroid and picks the
// members nearest the centroid as samples for labelling.
func buildTopicClusters(vectors []topicVector, points [][]float32, assignments []int, centroids [][]float32, minSize int) []topicCluster {
	members := make([][]int, len(centroids))
	for index, cluster := range assignments {
		if cluster < 0 || cluster >= len(centroids) {
			continue
		}
		members[cluster] = append(members[cluster], index)
	}
	clusters := make([]topicCluster, 0, len(centroids))
	for cluster, indexes := range members {
		if len(indexes) < minSize {
			continue
		}
		sort.SliceStable(indexes, func(i, j int) bool {
			return dot(points[indexes[i]], centroids[cluster]) > dot(points[indexes[j]], centroids[cluster])
		})
		out := topicCluster{centroid: centroids[cluster]}
		for position, index := range indexes {
			out.memberIDs = append(out.memberIDs, vectors[index].logID)
			if position < topicsLabelSamples {
				out.sampleIDs = append(out.sampleIDs, vectors[index].logID)
			}
		}
		clusters = append(clusters, out)
	}
	sort.SliceStable(clusters, func(i, j int) bool { return len(clusters[i].memberIDs) > len(clusters[j].memberIDs) })
	return clusters
}

// topicSampleTexts hydrates sample logs and returns what the users asked.
// Content-hidden rows are skipped: a label must never be derived from text
// the operator asked not to keep.
func (s *Service) topicSampleTexts(ctx context.Context, ids []string) []string {
	if s.logs == nil || len(ids) == 0 {
		return nil
	}
	logs, err := s.logs.GetLogsByIDs(ctx, ids)
	if err != nil {
		return nil
	}
	texts := make([]string, 0, len(logs))
	for index := range logs {
		if text := logUserText(&logs[index]); text != "" {
			texts = append(texts, text)
		}
	}
	return texts
}

// logUserText is the user's side of a conversation, or nothing for a row
// whose content is hidden.
func logUserText(entry *logstore.Log) string {
	if entry == nil || entry.ContentHidden {
		return ""
	}
	text := strings.Join(strings.Fields(entry.BuildInputContentSummary()), " ")
	if text == "" {
		text = strings.Join(strings.Fields(entry.ContentSummary), " ")
	}
	return truncateText(text, 400)
}

const topicLabelInstructions = "You name topics for a dashboard. Given a few user messages from one cluster of conversations, " +
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
	response, bifrostErr := chat(ctx, &schemas.BifrostResponsesRequest{
		Provider: transportProvider(),
		Model:    modelForRequest(config),
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
// what the conversations contain.
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
		"size":            {DataType: vectorstore.VectorStorePropertyTypeInteger, Description: "Conversations in the topic"},
		"total_clustered": {DataType: vectorstore.VectorStorePropertyTypeInteger, Description: "Conversations clustered in the same run"},
		"sample_log_ids":  {DataType: vectorstore.VectorStorePropertyTypeString, Description: "Comma-separated sample log IDs"},
		"window_start":    {DataType: vectorstore.VectorStorePropertyTypeInteger, Description: "Window start, Unix seconds"},
		"window_end":      {DataType: vectorstore.VectorStorePropertyTypeInteger, Description: "Window end, Unix seconds"},
		"computed_at":     {DataType: vectorstore.VectorStorePropertyTypeInteger, Description: "Computed at, Unix seconds"},
	}
	return vectors.CreateNamespace(ctx, namespace, dimension, properties)
}

// topicClusterCount picks k from the corpus size: roughly the square root of
// half the count, which is the usual rule of thumb, bounded on both ends.
func topicClusterCount(n int) int {
	if n <= 0 {
		return 0
	}
	k := int(math.Round(math.Sqrt(float64(n) / 2)))
	return min(max(k, 1), min(n, topicsMaxClusters))
}

// kMeans clusters unit vectors by cosine similarity. Centroids are seeded with
// k-means++ so two seeds rarely land in the same group, then refined for a
// bounded number of iterations. Returns each point's cluster and the unit
// centroids; an empty cluster keeps its last centroid.
func kMeans(points [][]float32, k, iterations int, seed int64) ([]int, [][]float32) {
	n := len(points)
	if n == 0 || k <= 0 {
		return nil, nil
	}
	k = min(k, n)
	rng := rand.New(rand.NewSource(seed))
	dimension := len(points[0])

	centroids := make([][]float32, 0, k)
	centroids = append(centroids, append([]float32(nil), points[rng.Intn(n)]...))
	distances := make([]float64, n)
	for len(centroids) < k {
		total := 0.0
		for index, point := range points {
			best := 0.0
			for _, centroid := range centroids {
				best = math.Max(best, float64(dot(point, centroid)))
			}
			// Distance in cosine terms: 1 - similarity, squared as k-means++ does.
			distances[index] = (1 - best) * (1 - best)
			total += distances[index]
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
	for iteration := 0; iteration < iterations; iteration++ {
		changed := false
		for index, point := range points {
			best, bestScore := 0, float32(math.Inf(-1))
			for cluster, centroid := range centroids {
				if score := dot(point, centroid); score > bestScore {
					best, bestScore = cluster, score
				}
			}
			if assignments[index] != best {
				assignments[index] = best
				changed = true
			}
		}
		sums := make([][]float32, k)
		counts := make([]int, k)
		for cluster := range sums {
			sums[cluster] = make([]float32, dimension)
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
		if !changed && iteration > 0 {
			break
		}
	}
	return assignments, centroids
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
