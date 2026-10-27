package warp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/queryscope"
	"github.com/maximhq/bifrost/framework/sidekiq"
)

// Discovery and upkeep.
//
// A run that clusters a window finds the topics of that window. Traffic moves
// on: a subject nobody asked about then is asked about now, and its requests
// match nothing, so they are pooled with their vectors. Discovery clusters
// that pool, and what clusters large enough becomes a topic - a new one, or
// an existing one it turns out to belong to. The rest stay pooled for next
// time.
//
// Upkeep is the other direction. A topic whose traffic stopped is retired so
// it takes nothing new and the live comparison stays small, and two topics
// that have come to mean the same thing are merged, the smaller into the
// larger, with the requests filed under the smaller still counting through
// merged_into.
//
// Both are started on a timer and run as Sidekiq jobs in TopicsJobPartition,
// beside the runs that seed topics. That partition runs its jobs one at a
// time across every instance, and every write to a topic row is made by one
// of its jobs, so no two instances rewrite a topic at once: a whole-row write
// cannot put back what another instance's job changed while it was working.

const (
	// TopicDiscoveryJobKind is the Sidekiq job that clusters the pool of
	// requests that matched no topic.
	TopicDiscoveryJobKind = "warp_topic_discovery"
	// TopicUpkeepJobKind is the Sidekiq job that retires and merges topics.
	TopicUpkeepJobKind = "warp_topic_upkeep"
	// TopicsJobPartition serialises every job that writes topics: a run and a
	// discovery must not interleave their writes.
	TopicsJobPartition = "warp_log_topics"

	// topicsDiscoveryInterval is how often upkeep runs and discovery is
	// considered.
	topicsDiscoveryInterval = time.Hour
	// topicsDiscoveryMinSize is how many requests a discovered cluster needs
	// to be a topic. Below it the requests stay pooled: a cluster of three is
	// three requests, not a pattern, and naming it would spend a model call
	// on a topic nobody will see.
	topicsDiscoveryMinSize = 10
	// topicsRetireAfter is how long a topic may go without a request before it
	// is retired. A topic younger than this is never retired for silence.
	topicsRetireAfter   = 30 * 24 * time.Hour
	topicsUpkeepTimeout = 5 * time.Minute
	// topicsRenameBudget bounds the naming upkeep does after a merge, all of
	// it: a model that is slow or down leaves the survivors their names
	// rather than taking the pass's time.
	topicsRenameBudget = 90 * time.Second
)

// TopicJobQueue is what discovery is started through. *sidekiq.Runner is one.
type TopicJobQueue interface {
	EnqueuePartitioned(ctx context.Context, id, kind, partitioningKey, metadata, createdBy string) error
}

// BuildDiscoveryJobMeta freezes the embedding space for one discovery. There
// is no window: discovery reads the pool, whatever it holds.
func (s *Service) BuildDiscoveryJobMeta(ctx context.Context) (string, error) {
	if s.logs == nil || s.store == nil {
		return "", ErrUnavailable
	}
	if s.topicStore() == nil {
		return "", fmt.Errorf("%w: the log store cannot keep topics", ErrUnavailable)
	}
	config, err := s.Config(ctx)
	if err != nil {
		return "", err
	}
	if !config.IsConfigured() || config.EmbeddingProvider == "" || config.EmbeddingModel == "" {
		return "", fmt.Errorf("%w: configure an embedding provider and model before discovering topics", ErrInvalidConfig)
	}
	return marshalBackfillMeta(BackfillJobMeta{
		ConfigSignature: embeddingConfigSignature(config),
		Namespace:       config.EffectiveLogVectorStoreNamespace(),
	})
}

// RunDiscoveryJob is the Sidekiq handler: read the pool, cluster it, give each
// cluster large enough to an existing topic it belongs to or name it as a new
// one, and file its requests.
func (s *Service) RunDiscoveryJob(ctx context.Context, job tables.TableSidekiqJob, progress sidekiq.ProgressFunc) (string, error) {
	var meta BackfillJobMeta
	if err := sonic.Unmarshal([]byte(job.Metadata), &meta); err != nil {
		return job.Metadata, fmt.Errorf("parse Warp discovery metadata: %w", err)
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
		return snapshot(), fmt.Errorf("Warp embedding configuration changed while topics were being discovered")
	}
	topicStore := s.topicStore()
	if topicStore == nil {
		return snapshot(), fmt.Errorf("%w: the log store cannot keep topics", ErrUnavailable)
	}

	// 1. Read the pool.
	pool, err := topicStore.ListWarpTopicUnmatched(ctx, meta.ConfigSignature, topicsMaxVectors)
	if err != nil {
		meta.LastError = err.Error()
		return snapshot(), fmt.Errorf("read pooled requests: %w", err)
	}
	meta.Scanned, meta.Total = len(pool), int64(len(pool))
	// How many have to be waiting is the operator's setting
	// (topic_discovery_min_requests): enough to be worth a run's model calls.
	if minPool := config.EffectiveTopicDiscoveryMinRequests(); len(pool) < minPool {
		meta.Message = fmt.Sprintf("%d request(s) are waiting for a topic; discovery runs once there are %d.", len(pool), minPool)
		return snapshot(), progress(snapshot())
	}
	if err := checkpoint(fmt.Sprintf("Clustering %d request(s) that matched no topic.", len(pool))); err != nil {
		return snapshot(), err
	}

	// 2. Cluster it, as a run clusters a window.
	limits := s.topicLimits
	if limits.batch == 0 {
		limits = defaultTopicLimits()
	}
	run := newTopicRun(limits, topicsSeed(meta))
	for _, entry := range pool {
		vector, err := logstore.DecodeWarpVector(entry.Vector)
		if err != nil || len(vector) == 0 {
			continue
		}
		if err := run.add(ctx, topicVector{logID: entry.LogID, vector: unitVector(vector)}); err != nil {
			return snapshot(), err
		}
	}
	if err := run.finish(ctx); err != nil {
		if ctx.Err() != nil {
			meta.Message = "Stopped while clustering."
			_ = progress(snapshot())
			return snapshot(), ctx.Err()
		}
		meta.LastError = err.Error()
		return snapshot(), err
	}
	meta.Clustered = run.clustered

	// 3. Sort the clusters: large enough to be topics, or back to the pool.
	existing, err := topicStore.ListWarpTopics(ctx, logstore.WarpTopicFilter{
		Kinds: []string{logstore.WarpTopicKindTopic}, Statuses: []string{logstore.WarpTopicStatusActive}, EmbeddingSignature: meta.ConfigSignature,
	})
	if err != nil {
		meta.LastError = err.Error()
		return snapshot(), fmt.Errorf("read topics: %w", err)
	}
	var candidates []topicCluster
	for _, cluster := range run.clusters {
		if cluster.size < topicsDiscoveryMinSize {
			meta.Unassigned += cluster.size
			continue
		}
		candidates = append(candidates, cluster)
	}
	meta.Unassigned += run.dropped + run.unmatched
	if len(candidates) == 0 {
		meta.Message = fmt.Sprintf("Clustered %d pooled request(s); no cluster was large enough to be a topic.", len(pool))
		return snapshot(), progress(snapshot())
	}
	// 4. Give each cluster that is an existing topic by its centre to that
	// topic. Only the rest need a name, so only the rest cost a model call.
	placed, unplaced := placeByCentre(candidates, existing, run.threshold)
	if len(unplaced) > 0 {
		if err := checkpoint(fmt.Sprintf("Found %d cluster(s) large enough to be topics; labelling %d.", len(candidates), len(unplaced))); err != nil {
			return snapshot(), err
		}
		labelCtx := withBackgroundWork(ctx)
		failed, lastError := labelTopics(labelCtx, s.chatFuncFor(labelCtx, config, ""), config, unplaced, s.topicSampleTexts(ctx, unplaced))
		if ctx.Err() != nil {
			meta.Message = "Stopped while labelling."
			_ = progress(snapshot())
			return snapshot(), ctx.Err()
		}
		if failed > 0 {
			meta.Failed += failed
			meta.LastError = lastError
		}
		// 5. Join what is the same subject by name, then give a cluster named
		// like an existing topic to it, and make a topic of the rest.
		unplaced, meta.MergedByName = joinTopicsByName(unplaced)
		placed = append(placed, placeByName(unplaced, existing)...)
	}
	now := time.Now().UTC()
	var created []logstore.WarpTopic
	for index := range placed {
		if placed[index].topic != "" {
			meta.Merged++
			continue
		}
		cluster := placed[index].cluster
		topic := logstore.WarpTopic{
			ID: uuid.NewString(), Kind: logstore.WarpTopicKindTopic, Label: cluster.label,
			Centroid: logstore.EncodeWarpVector(cluster.centroid()), AssignFloor: float64(run.floor), Cohesion: cluster.cohesion(),
			EmbeddingSignature: meta.ConfigSignature, Status: logstore.WarpTopicStatusActive, CreatedAt: now,
		}
		if err := topic.SetExampleLogIDs(cluster.exampleIDs()); err != nil {
			return snapshot(), fmt.Errorf("encode topic examples: %w", err)
		}
		created = append(created, topic)
		placed[index].topic = topic.ID
		meta.Topics = append(meta.Topics, TopicSummary{Label: cluster.label, Size: cluster.size, Cohesion: roundTo(cluster.cohesion(), 3)})
	}
	folded, err := foldIntoExistingTopics(ctx, topicStore, existing, placed)
	if err != nil {
		meta.LastError = err.Error()
		return snapshot(), err
	}
	if err := topicStore.UpsertWarpTopics(ctx, append(created, folded...)); err != nil {
		meta.LastError = err.Error()
		return snapshot(), fmt.Errorf("write topics: %w", err)
	}
	meta.Indexed = len(created)
	filed := 0
	for _, placement := range placed {
		n, err := fileTopicMembers(ctx, topicStore, placement.topic, placement.cluster.members, now)
		filed += n
		if err != nil {
			meta.LastError = err.Error()
			return snapshot(), fmt.Errorf("file requests: %w", err)
		}
	}
	meta.Assigned = filed
	if s.indexer != nil {
		s.indexer.InvalidateTopics()
	}
	if len(created) > 0 {
		if themes, err := s.GroupTopics(ctx); err != nil {
			meta.LastError = fmt.Sprintf("group topics into themes: %v", err)
		} else {
			meta.Themes = themes.Themes
		}
	}
	meta.Message = fmt.Sprintf("Clustered %d pooled request(s): %d new topic(s), %d cluster(s) folded into existing topics, %d request(s) filed.", len(pool), meta.Indexed, meta.Merged, filed)
	if meta.Unassigned > 0 {
		meta.Message += fmt.Sprintf(" %d stay pooled for next time.", meta.Unassigned)
	}
	return snapshot(), progress(snapshot())
}

// topicPlacement is one discovered cluster and the topic it goes to: an
// existing one, or "" for a topic of its own.
type topicPlacement struct {
	cluster topicCluster
	topic   string
}

// placeByCentre gives each cluster at least threshold like an existing topic
// to that topic, whatever it was named: the same test the run applies between
// its own clusters. It returns those placements and the clusters left over.
// threshold is nil when the run derived none, and then every cluster is left
// over for placeByName.
func placeByCentre(clusters []topicCluster, existing []logstore.WarpTopic, threshold *float64) ([]topicPlacement, []topicCluster) {
	if threshold == nil {
		return nil, clusters
	}
	type centre struct {
		id     string
		vector []float32
	}
	centres := make([]centre, 0, len(existing))
	for _, topic := range existing {
		if vector, err := logstore.DecodeWarpVector(topic.Centroid); err == nil && len(vector) > 0 {
			centres = append(centres, centre{id: topic.ID, vector: vector})
		}
	}
	var placed []topicPlacement
	var rest []topicCluster
	for _, cluster := range clusters {
		own := cluster.centroid()
		best, bestScore := "", float32(-2)
		for _, candidate := range centres {
			if len(candidate.vector) != len(own) {
				continue
			}
			if score := dot(own, candidate.vector); score > bestScore {
				best, bestScore = candidate.id, score
			}
		}
		if best != "" && float64(bestScore) >= *threshold {
			placed = append(placed, topicPlacement{cluster: cluster, topic: best})
			continue
		}
		rest = append(rest, cluster)
	}
	return placed, rest
}

// placeByName gives each named cluster to the existing topic with the same
// name, for the reason joinTopicsByName gives, and leaves the rest for topics
// of their own.
func placeByName(clusters []topicCluster, existing []logstore.WarpTopic) []topicPlacement {
	byName := make(map[string]string, len(existing))
	for _, topic := range existing {
		byName[topicNameKey(topic.Label)] = topic.ID
	}
	placed := make([]topicPlacement, 0, len(clusters))
	for _, cluster := range clusters {
		placement := topicPlacement{cluster: cluster}
		if key := topicNameKey(cluster.label); key != "" {
			placement.topic = byName[key]
		}
		placed = append(placed, placement)
	}
	return placed
}

// foldIntoExistingTopics works out again the centre and cohesion of each
// existing topic clusters were given to, as mergeStoredTopics does for two
// topics: the topic is rebuilt as the cluster its row keeps and absorbs what
// was found. Without it a topic's row would describe fewer requests than are
// filed under it, and the sum upkeep recovers from the row would be wrong.
// It reads the topics' sizes, so it runs before their new requests are filed.
func foldIntoExistingTopics(ctx context.Context, store logstore.WarpTopicStore, existing []logstore.WarpTopic, placed []topicPlacement) ([]logstore.WarpTopic, error) {
	byID := make(map[string]logstore.WarpTopic, len(existing))
	for _, topic := range existing {
		byID[topic.ID] = topic
	}
	var ids []string
	for _, placement := range placed {
		if _, ok := byID[placement.topic]; ok && !slices.Contains(ids, placement.topic) {
			ids = append(ids, placement.topic)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	counted, into, err := withMergedTopics(ctx, store, ids)
	if err != nil {
		return nil, err
	}
	sizes, err := countTopicRequests(ctx, store, counted, into, nil)
	if err != nil {
		return nil, fmt.Errorf("count requests: %w", err)
	}
	folded := make([]logstore.WarpTopic, 0, len(ids))
	for _, id := range ids {
		topic := byID[id]
		cluster, ok := storedTopicCluster(topic, sizes[id])
		if !ok {
			// No usable centre to move; the requests are still filed.
			continue
		}
		for _, placement := range placed {
			if placement.topic == id && len(placement.cluster.sum) == len(cluster.sum) {
				cluster.absorb(placement.cluster)
			}
		}
		topic.Centroid = logstore.EncodeWarpVector(cluster.centroid())
		topic.Cohesion = cluster.cohesion()
		folded = append(folded, topic)
	}
	return folded, nil
}

// topicNameKey is a label as names are compared: case and spacing aside.
func topicNameKey(label string) string {
	return strings.ToLower(strings.Join(strings.Fields(label), " "))
}

// fileTopicMembers files requests under a topic and takes them out of the
// pool. Their rows are read back first so the columns a topic question is
// asked by are kept; a request with no row yet is projected from its log.
func fileTopicMembers(ctx context.Context, store logstore.WarpTopicStore, topic string, members []topicMember, now time.Time) (int, error) {
	filed := 0
	for start := 0; start < len(members); start += topicsWriteBatch {
		batch := members[start:min(start+topicsWriteBatch, len(members))]
		ids := make([]string, 0, len(batch))
		similarity := make(map[string]float32, len(batch))
		for _, member := range batch {
			ids = append(ids, member.logID)
			similarity[member.logID] = member.similarity
		}
		rows, err := store.ListWarpTopicAssignments(ctx, logstore.WarpTopicAssignmentFilter{LogIDs: ids}, len(ids))
		if err != nil {
			return filed, err
		}
		seen := make(map[string]struct{}, len(rows))
		for _, row := range rows {
			seen[row.LogID] = struct{}{}
		}
		var missing []string
		for _, id := range ids {
			if _, ok := seen[id]; !ok {
				missing = append(missing, id)
			}
		}
		if len(missing) > 0 {
			projected, err := store.ListLogsForWarpTopics(ctx, missing, time.Time{}, time.Time{})
			if err != nil {
				return filed, err
			}
			rows = append(rows, projected...)
		}
		for index := range rows {
			rows[index].TopicID = topic
			rows[index].Similarity = float64(similarity[rows[index].LogID])
			rows[index].AssignedAt = now
		}
		if err := store.UpsertWarpTopicAssignments(ctx, rows); err != nil {
			return filed, err
		}
		if err := store.DeleteWarpTopicUnmatched(ctx, ids); err != nil {
			return filed, err
		}
		filed += len(rows)
	}
	return filed, nil
}

// TopicUpkeep is what one pass of MaintainTopics did.
type TopicUpkeep struct {
	Retired int
	Merged  int
	// Themes is how many themes the topics are grouped into after a pass
	// that changed them; zero when nothing changed.
	Themes int
	// Pruned is how many topic and theme rows, retired for longer than the
	// grace period, were deleted; Dropped how many pooled requests of
	// another embedding model were.
	Pruned  int
	Dropped int64
}

// MaintainTopics retires the topics whose traffic has stopped and merges the
// ones that have come to mean the same thing.
//
// Retirement is by silence: a topic old enough to have had its chance with
// no request in topicsRetireAfter takes nothing new. Topics built with an
// embedding model other than the configured one are retired outright, since
// their centres mean nothing against today's vectors.
//
// Merging reuses the run's own test. Each topic is rebuilt as a cluster from
// what its row keeps - the centre is the direction of the members' sum and
// the cohesion is that sum's length over their number, so the sum is exactly
// recoverable - and the clusters are merged at the threshold the seed run
// that made them merged at, then joined by name. The smaller topic is marked merged into the
// larger, whose centre and cohesion are worked out again from both. Nothing
// filed under the smaller is rewritten: the reader follows merged_into.
func (s *Service) MaintainTopics(ctx context.Context) (TopicUpkeep, error) {
	var done TopicUpkeep
	store := s.topicStore()
	if store == nil {
		return done, ErrUnavailable
	}
	config, err := s.Config(ctx)
	if err != nil {
		return done, err
	}
	signature := embeddingConfigSignature(config)
	active, err := store.ListWarpTopics(ctx, logstore.WarpTopicFilter{Kinds: []string{logstore.WarpTopicKindTopic}, Statuses: []string{logstore.WarpTopicStatusActive}})
	if err != nil {
		return done, fmt.Errorf("read topics: %w", err)
	}
	if len(active) > 0 {
		if err := s.retireAndMergeTopics(ctx, store, config, active, &done); err != nil {
			return done, err
		}
	}
	// Then the tidying nobody reads the results of, on every pass whether or
	// not one changed anything: merged topics whose survivor has gone are
	// gone too, rows retired long enough ago are deleted, and pooled
	// requests of another embedding model are dropped.
	grace := s.topicLimits.pruneGrace
	if grace == 0 {
		grace = topicsRetireAfter
	}
	pruned, err := pruneRetiredTopics(ctx, store, time.Now().UTC(), grace)
	if err != nil {
		return done, err
	}
	done.Pruned = pruned
	dropped, err := store.DeleteWarpTopicUnmatchedExcept(ctx, signature)
	if err != nil {
		return done, fmt.Errorf("drop pooled requests of other models: %w", err)
	}
	done.Dropped = dropped
	return done, nil
}

// retireAndMergeTopics is the part of upkeep that needs active topics: it
// retires the silent and the foreign, merges the alike, and regroups the
// themes when anything changed.
func (s *Service) retireAndMergeTopics(ctx context.Context, store logstore.WarpTopicStore, config *schemas.WarpConfig, active []logstore.WarpTopic, done *TopicUpkeep) error {
	signature := embeddingConfigSignature(config)
	ids := make([]string, 0, len(active))
	for _, topic := range active {
		ids = append(ids, topic.ID)
	}
	now := time.Now().UTC()
	since := now.Add(-topicsRetireAfter)
	counted, into, err := withMergedTopics(ctx, store, ids)
	if err != nil {
		return err
	}
	recentCount, err := countTopicRequests(ctx, store, counted, into, &since)
	if err != nil {
		return fmt.Errorf("count recent requests: %w", err)
	}
	totalCount, err := countTopicRequests(ctx, store, counted, into, nil)
	if err != nil {
		return fmt.Errorf("count requests: %w", err)
	}

	var changed []logstore.WarpTopic
	var current []logstore.WarpTopic
	for _, topic := range active {
		silent := topic.CreatedAt.Before(since) && recentCount[topic.ID] == 0
		if topic.EmbeddingSignature != signature || silent {
			topic.Status = logstore.WarpTopicStatusRetired
			changed = append(changed, topic)
			done.Retired++
			continue
		}
		current = append(current, topic)
	}

	merged, survivors := mergeStoredTopics(current, totalCount, s.seedMergeThreshold(ctx, signature))
	changed = append(changed, merged...)
	changed = append(changed, survivors...)
	done.Merged = len(merged)
	// Every pass, not only one that merged: a chain left deeper than one level
	// by an earlier pass whose write failed partway is flattened here too.
	live := make(map[string]struct{}, len(current))
	for _, topic := range current {
		live[topic.ID] = struct{}{}
	}
	for _, topic := range merged {
		delete(live, topic.ID)
	}
	repointed, err := repointMergedTopics(ctx, store, live, merged)
	if err != nil {
		return err
	}
	changed = append(changed, repointed...)
	if len(changed) == 0 {
		return nil
	}
	if err := store.UpsertWarpTopics(ctx, changed); err != nil {
		return fmt.Errorf("write topics: %w", err)
	}
	// Named again only once the merges are written, so a model that is slow
	// or down costs the survivors their new names and nothing else.
	if renames := s.renameMergedTopics(ctx, config, current, merged, survivors, totalCount); len(renames) > 0 {
		if err := saveTopicRenames(ctx, store, renames); err != nil {
			s.warnf("warp: write renamed topics: %v", err)
		}
	}
	if done.Retired > 0 || done.Merged > 0 {
		if s.indexer != nil {
			s.indexer.InvalidateTopics()
		}
		// The themes follow the topics. A model that cannot group them
		// leaves the themes as they were, tidied; upkeep has done its job.
		themes, err := s.GroupTopics(ctx)
		if err != nil {
			s.warnf("warp: group topics into themes: %v", err)
		}
		done.Themes = themes.Themes
	}
	return nil
}

// pruneRetiredTopics retires the merged topics whose survivor is no longer
// active, then deletes every row retired for longer than grace, and reports
// how many it deleted.
//
// Retired rows are kept for a while rather than deleted at once so a run or
// a reader in flight still finds the row an id names; after that nothing
// reads them, since every read selects on active topics and follows
// merged_into only to one. A seed run that failed and could not retire what
// it wrote leaves those rows seeding; they are deleted on the same schedule.
func pruneRetiredTopics(ctx context.Context, store logstore.WarpTopicStore, now time.Time, grace time.Duration) (int, error) {
	rows, err := store.ListWarpTopics(ctx, logstore.WarpTopicFilter{Statuses: []string{logstore.WarpTopicStatusActive, logstore.WarpTopicStatusMerged, logstore.WarpTopicStatusRetired, logstore.WarpTopicStatusSeeding}, OmitCentroids: true})
	if err != nil {
		return 0, fmt.Errorf("read topics for pruning: %w", err)
	}
	active := make(map[string]struct{})
	for _, row := range rows {
		if row.Status == logstore.WarpTopicStatusActive {
			active[row.ID] = struct{}{}
		}
	}
	var orphaned []string
	var expired []string
	for _, row := range rows {
		switch row.Status {
		case logstore.WarpTopicStatusMerged:
			if _, ok := active[row.MergedInto]; !ok {
				orphaned = append(orphaned, row.ID)
			}
		case logstore.WarpTopicStatusRetired, logstore.WarpTopicStatusSeeding:
			if row.UpdatedAt.Before(now.Add(-grace)) {
				expired = append(expired, row.ID)
			}
		}
	}
	if len(orphaned) > 0 {
		whole, err := store.ListWarpTopics(ctx, logstore.WarpTopicFilter{IDs: orphaned})
		if err != nil {
			return 0, fmt.Errorf("read orphaned merged topics: %w", err)
		}
		for index := range whole {
			whole[index].Status = logstore.WarpTopicStatusRetired
		}
		if err := store.UpsertWarpTopics(ctx, whole); err != nil {
			return 0, fmt.Errorf("retire orphaned merged topics: %w", err)
		}
	}
	if len(expired) == 0 {
		return 0, nil
	}
	if err := store.DeleteWarpTopics(ctx, expired); err != nil {
		return 0, fmt.Errorf("delete retired topics: %w", err)
	}
	return len(expired), nil
}

// TopicHealth is how live filing is doing: what there is to file under, how
// much waits for a topic, and how much of the last day's traffic matched
// nothing. A rising unmatched share is the sign the topics no longer fit
// the traffic.
type TopicHealth struct {
	ActiveTopics int   `json:"active_topics"`
	Themes       int   `json:"themes"`
	Pooled       int64 `json:"pooled"`
	// RequestsDay and UnmatchedDay count the requests filed in the last 24
	// hours and those of them that matched no topic; UnmatchedShare is the
	// second over the first, or zero when nothing was filed.
	RequestsDay    int64   `json:"requests_24h"`
	UnmatchedDay   int64   `json:"unmatched_24h"`
	UnmatchedShare float64 `json:"unmatched_share_24h"`
}

// TopicHealth reads the live filing's state for the embedding model in use.
//
// It is the deployment's state: the topic and pool counts cover every caller,
// and the pool has no owner to scope by. A caller whose reads are narrowed is
// told none of it - ErrUnavailable - rather than their own day's requests
// beside the deployment's topics.
func (s *Service) TopicHealth(ctx context.Context) (TopicHealth, error) {
	var health TopicHealth
	store := s.topicStore()
	if store == nil {
		return health, ErrUnavailable
	}
	if !withCallerRestriction(ctx, ScopeFromContext(ctx), s.callerRestriction).Unrestricted || queryscope.DimensionFromContext(ctx) != nil {
		return health, ErrUnavailable
	}
	config, err := s.Config(ctx)
	if err != nil {
		return health, err
	}
	signature := embeddingConfigSignature(config)
	topics, err := store.ListWarpTopics(ctx, logstore.WarpTopicFilter{Statuses: []string{logstore.WarpTopicStatusActive}, EmbeddingSignature: signature, OmitCentroids: true})
	if err != nil {
		return health, fmt.Errorf("read topics: %w", err)
	}
	for _, topic := range topics {
		if topic.Kind == logstore.WarpTopicKindTheme {
			health.Themes++
		} else {
			health.ActiveTopics++
		}
	}
	if health.Pooled, err = store.CountWarpTopicUnmatched(ctx, signature); err != nil {
		return health, fmt.Errorf("count pooled requests: %w", err)
	}
	since := time.Now().UTC().Add(-24 * time.Hour)
	usage, err := store.SummarizeWarpTopicAssignments(ctx, logstore.WarpTopicAssignmentFilter{StartTime: &since})
	if err != nil {
		return health, fmt.Errorf("count the day's requests: %w", err)
	}
	for _, entry := range usage {
		health.RequestsDay += entry.Requests
		if entry.TopicID == "" {
			health.UnmatchedDay = entry.Requests
		}
	}
	if health.RequestsDay > 0 {
		health.UnmatchedShare = float64(health.UnmatchedDay) / float64(health.RequestsDay)
	}
	return health, nil
}

// repointMergedTopics returns the topics already merged whose merged_into is
// not the live topic their chain ends in, pointed at that topic instead. live
// is the topics active once this pass is written, and merged the topics this
// pass merges.
//
// Every merged topic then points straight at a live topic, so "the requests
// of topic X" is X and the topics merged into X, one level deep: the logs
// filter (applyTopicFilter in logstore) follows exactly one, and the reader's
// chain walk (resolveMergedTopics) finds the same answer either way. A chain
// that ends nowhere live is left as it is.
func repointMergedTopics(ctx context.Context, store logstore.WarpTopicStore, live map[string]struct{}, merged []logstore.WarpTopic) ([]logstore.WarpTopic, error) {
	earlier, err := store.ListWarpTopics(ctx, logstore.WarpTopicFilter{Kinds: []string{logstore.WarpTopicKindTopic}, Statuses: []string{logstore.WarpTopicStatusMerged}, OmitCentroids: true})
	if err != nil {
		return nil, fmt.Errorf("read merged topics: %w", err)
	}
	if len(earlier) == 0 {
		return nil, nil
	}
	survivorOf := resolveMergedTopics(live, append(slices.Clone(earlier), merged...))
	var repointed []logstore.WarpTopic
	for _, topic := range earlier {
		survivor, ok := survivorOf[topic.ID]
		if !ok || survivor == topic.MergedInto {
			continue
		}
		// Omitted on read; written back whole, so it has to be read in full.
		whole, err := store.ListWarpTopics(ctx, logstore.WarpTopicFilter{IDs: []string{topic.ID}})
		if err != nil {
			return nil, fmt.Errorf("read merged topic %s: %w", topic.ID, err)
		}
		if len(whole) != 1 {
			return nil, fmt.Errorf("read merged topic %s: want 1 row, got %d", topic.ID, len(whole))
		}
		whole[0].MergedInto = survivor
		repointed = append(repointed, whole[0])
	}
	return repointed, nil
}

// mergeStoredTopics finds the topics that have come to mean the same thing
// and returns the ones to mark merged, with MergedInto set, and the survivors
// whose centre and cohesion changed.
//
// Topics are merged on similarity only at threshold, the one the seed run that
// made them merged its clusters at; nil merges them by name alone. Deriving a
// threshold from the stored topics instead, as a run derives one from its
// clusters, gave a lower one: stored topics are already merged, larger and
// looser than the clusters a run measures, and the largest weigh most. So
// upkeep merged within the hour topics the seed had just kept apart - four
// about different coding work became one - and every merge, by loosening the
// largest topic, lowered the next pass's threshold further.
func mergeStoredTopics(topics []logstore.WarpTopic, sizes map[string]int64, threshold *float64) (merged, survivors []logstore.WarpTopic) {
	if len(topics) < 2 {
		return nil, nil
	}
	byID := make(map[string]logstore.WarpTopic, len(topics))
	clusters := make([]topicCluster, 0, len(topics))
	for _, topic := range topics {
		cluster, ok := storedTopicCluster(topic, sizes[topic.ID])
		if !ok {
			continue
		}
		byID[topic.ID] = topic
		clusters = append(clusters, cluster)
	}
	sortTopicClusters(clusters)
	if threshold != nil {
		clusters, _ = mergeTopicClusters(clusters, *threshold)
	}
	clusters, _ = joinTopicsByName(clusters)
	for _, cluster := range clusters {
		if len(cluster.members) < 2 {
			continue
		}
		// The largest topic is the survivor: absorb keeps the first of a pair
		// and the clusters were sorted by size.
		survivor := byID[cluster.members[0].logID]
		survivor.Centroid = logstore.EncodeWarpVector(cluster.centroid())
		survivor.Cohesion = cluster.cohesion()
		survivors = append(survivors, survivor)
		for _, member := range cluster.members[1:] {
			topic := byID[member.logID]
			topic.Status = logstore.WarpTopicStatusMerged
			topic.MergedInto = survivor.ID
			merged = append(merged, topic)
		}
	}
	return merged, survivors
}

// seedMergeThreshold is the threshold the latest seed run merged its clusters
// at, for upkeep to merge the topics it made at the same one. Every pair of
// topics a seed leaves is below it, so upkeep merges none of them for being
// alike until their centres move; a topic discovery made that duplicates one
// of them is merged as the seed would have merged it.
//
// It is nil, and upkeep merges by name alone, when the latest run did not
// finish, was over another embedding model, or derived no threshold, or when
// it cannot be read: a pass that does not merge loses nothing the next pass
// cannot do, and one that merges at a guessed threshold cannot be undone.
func (s *Service) seedMergeThreshold(ctx context.Context, signature string) *float64 {
	s.mu.RLock()
	jobs := s.backfillJobs
	s.mu.RUnlock()
	if jobs == nil {
		return nil
	}
	job, err := jobs.GetLatestSidekiqJobByKind(ctx, TopicsJobKind)
	if err != nil {
		s.warnf("warp: read the latest topics run for its merge threshold: %v", err)
		return nil
	}
	if job == nil || job.Status != tables.SidekiqStatusCompleted {
		return nil
	}
	var meta BackfillJobMeta
	if err := sonic.UnmarshalString(job.Metadata, &meta); err != nil {
		s.warnf("warp: decode topics run %s for its merge threshold: %v", job.ID, err)
		return nil
	}
	if meta.ConfigSignature != signature {
		return nil
	}
	return meta.MergeThreshold
}

// topicRename is a new name for a survivor, and the row as this pass wrote it,
// for the rename to be saved only over that row.
type topicRename struct {
	written  logstore.WarpTopic
	label    string
	examples []string
}

// saveTopicRenames writes each new name and its examples over the survivor's
// row as it is now, and only while that row is still the one the pass wrote.
//
// Upkeep runs as a job in TopicsJobPartition, so no other topics job writes
// while it waits on the model. The check is for the one way two can overlap:
// a job whose heartbeat lapsed is claimed again elsewhere before its first
// owner notices. Writing the copy taken before naming would then put back
// what the newer job changed - a merged topic would come back active - so the
// row is read again, compared on what this pass wrote, and only its name and
// examples are changed.
func saveTopicRenames(ctx context.Context, store logstore.WarpTopicStore, renames []topicRename) error {
	ids := make([]string, 0, len(renames))
	byID := make(map[string]topicRename, len(renames))
	for _, rename := range renames {
		ids = append(ids, rename.written.ID)
		byID[rename.written.ID] = rename
	}
	rows, err := store.ListWarpTopics(ctx, logstore.WarpTopicFilter{IDs: ids})
	if err != nil {
		return fmt.Errorf("read renamed topics: %w", err)
	}
	var updated []logstore.WarpTopic
	for _, row := range rows {
		rename, ok := byID[row.ID]
		if !ok || !sameTopicVersion(row, rename.written) {
			continue
		}
		if err := row.SetExampleLogIDs(rename.examples); err != nil {
			return fmt.Errorf("encode examples of topic %s: %w", row.ID, err)
		}
		row.Label = rename.label
		updated = append(updated, row)
	}
	if len(updated) == 0 {
		return nil
	}
	return store.UpsertWarpTopics(ctx, updated)
}

// sameTopicVersion reports whether a topic's row is still the one written:
// as active, merged into the same topic, named and centred the same.
// UpdatedAt is the store's to stamp, so it is not what is compared.
func sameTopicVersion(row, written logstore.WarpTopic) bool {
	return row.Status == written.Status && row.MergedInto == written.MergedInto && row.Label == written.Label &&
		row.Cohesion == written.Cohesion && bytes.Equal(row.Centroid, written.Centroid)
}

// renameMergedTopics names each survivor again when the topics merged into it
// had other names, and returns the new names for the survivors it named.
//
// A survivor's name was written from its own examples when it was made, and
// a merge can turn it into a small corner of what the topic now holds: three
// topics about documentation, releases and a desktop app went on being
// listed under the name of the fourth. So the new name is written from
// examples of every topic in the merge, in proportion to how many requests
// each holds, with the old names offered as hints. A survivor that cannot be
// named again - no model, no text, a failed call, no time left - keeps its
// name: it is still a name for part of the topic, which a fallback made of
// common words may not be.
//
// The names are asked for a few at a time within topicsRenameBudget, and
// never past the point that leaves upkeep topicsGroupTimeout for the themes,
// which are grouped from these names next.
func (s *Service) renameMergedTopics(ctx context.Context, config *schemas.WarpConfig, current, merged, survivors []logstore.WarpTopic, sizes map[string]int64) []topicRename {
	byID := make(map[string]logstore.WarpTopic, len(current))
	for _, topic := range current {
		byID[topic.ID] = topic
	}
	groups := map[string][]logstore.WarpTopic{}
	for _, topic := range merged {
		groups[topic.MergedInto] = append(groups[topic.MergedInto], byID[topic.ID])
	}
	type rename struct {
		survivor logstore.WarpTopic
		names    []string
		ids      []string
	}
	var renames []rename
	var ids []string
	for _, survivor := range survivors {
		group := append([]logstore.WarpTopic{byID[survivor.ID]}, groups[survivor.ID]...)
		names := distinctTopicNames(group)
		if len(names) < 2 {
			// Joined by name, or merged with topics of its own name: the name
			// still says what the topic holds.
			continue
		}
		picked := mergedTopicExamples(group, sizes, topicsLabelSamples+topicsLabelDrawn)
		renames = append(renames, rename{survivor: survivor, names: names, ids: picked})
		ids = append(ids, picked...)
	}
	if len(renames) == 0 {
		return nil
	}
	deadline := time.Now().Add(topicsRenameBudget)
	if upkeepDeadline, ok := ctx.Deadline(); ok && upkeepDeadline.Add(-topicsGroupTimeout).Before(deadline) {
		deadline = upkeepDeadline.Add(-topicsGroupTimeout)
	}
	if !deadline.After(time.Now()) {
		s.warnf("warp: no time left to rename %d merged topic(s); they keep their names", len(renames))
		return nil
	}
	renameCtx, cancel := context.WithDeadline(withBackgroundWork(ctx), deadline)
	defer cancel()
	texts := s.logTexts(renameCtx, ids)
	chat := s.chatFuncFor(renameCtx, config, "")
	if chat == nil {
		return nil
	}

	var (
		mu      sync.Mutex
		group   sync.WaitGroup
		renamed []topicRename
	)
	slots := make(chan struct{}, topicsLabelWorkers)
	for _, pending := range renames {
		samples := labelTexts(texts, pending.ids)
		if len(samples) == 0 {
			continue
		}
		select {
		case slots <- struct{}{}:
		case <-renameCtx.Done():
		}
		if renameCtx.Err() != nil {
			break
		}
		group.Add(1)
		go func() {
			defer func() {
				<-slots
				group.Done()
			}()
			// Asked, not labelled: an answer with no name keeps the survivor's,
			// where labelTopic would put one made of common words in its place.
			label, err := askTopicLabel(renameCtx, chat, config, samples, pending.names)
			if err != nil || label == "" {
				if err != nil {
					s.warnf("warp: rename merged topic %s: %v", pending.survivor.ID, err)
				}
				return
			}
			var examples []string
			for _, id := range pending.ids {
				if texts[id] != "" {
					examples = append(examples, id)
				}
			}
			mu.Lock()
			renamed = append(renamed, topicRename{written: pending.survivor, label: label, examples: examples})
			mu.Unlock()
		}()
	}
	group.Wait()
	return renamed
}

// distinctTopicNames is the topics' names, each once however it is spaced or
// cased, in the order given.
func distinctTopicNames(topics []logstore.WarpTopic) []string {
	var names []string
	seen := map[string]struct{}{}
	for _, topic := range topics {
		key := strings.ToLower(strings.Join(strings.Fields(topic.Label), " "))
		if _, repeat := seen[key]; key == "" || repeat {
			continue
		}
		seen[key] = struct{}{}
		names = append(names, strings.TrimSpace(topic.Label))
	}
	return names
}

// mergedTopicExamples picks up to limit of the topics' examples, each topic's
// share in proportion to the requests filed under it and at least one, and
// interleaves them so the first few already span the larger topics rather
// than all coming from one.
func mergedTopicExamples(topics []logstore.WarpTopic, sizes map[string]int64, limit int) []string {
	examples := make([][]string, len(topics))
	var total int64
	for index, topic := range topics {
		examples[index] = topic.ExampleLogIDs()
		total += max(sizes[topic.ID], 1)
	}
	order := make([]int, len(topics))
	for index := range order {
		order[index] = index
	}
	sort.SliceStable(order, func(i, j int) bool { return sizes[topics[order[i]].ID] > sizes[topics[order[j]].ID] })
	quota := make([]int, len(topics))
	for _, index := range order {
		share := int(math.Round(float64(limit) * float64(max(sizes[topics[index].ID], 1)) / float64(total)))
		quota[index] = min(max(share, 1), len(examples[index]))
	}
	var picked []string
	for round := 0; len(picked) < limit; round++ {
		added := false
		for _, index := range order {
			if round < quota[index] && len(picked) < limit {
				picked = append(picked, examples[index][round])
				added = true
			}
		}
		if !added {
			break
		}
	}
	return picked
}

// storedTopicCluster rebuilds a topic as the cluster its row keeps, from its
// centre, its cohesion and how many requests are filed under it. It reports
// false for a topic with no usable centre.
func storedTopicCluster(topic logstore.WarpTopic, requests int64) (topicCluster, bool) {
	centre, err := logstore.DecodeWarpVector(topic.Centroid)
	if err != nil || len(centre) == 0 {
		return topicCluster{}, false
	}
	size := int(max(requests, 1))
	// sum = centre * |sum|, and |sum| = cohesion * size.
	scale := float32(topic.Cohesion * float64(size))
	if topic.Cohesion <= 0 {
		scale = float32(size)
	}
	sum := make([]float32, len(centre))
	for d, value := range centre {
		sum[d] = value * scale
	}
	return topicCluster{
		sum: sum, size: size, label: topic.Label,
		// The cluster stands for its topic; absorb carries the ids along.
		members: []topicMember{{logID: topic.ID}},
	}, true
}

// resolveMergedTopics maps every merged topic to the active topic its chain
// ends in, for a reader counting requests filed under topics since merged. A
// chain that ends nowhere active is left out.
func resolveMergedTopics(active map[string]struct{}, merged []logstore.WarpTopic) map[string]string {
	into := make(map[string]string, len(merged))
	for _, topic := range merged {
		into[topic.ID] = topic.MergedInto
	}
	resolved := make(map[string]string, len(merged))
	for id := range into {
		target := id
		for hops := 0; hops < len(into)+1; hops++ {
			next, ok := into[target]
			if !ok {
				break
			}
			target = next
		}
		if _, ok := active[target]; ok && target != id {
			resolved[id] = target
		}
	}
	return resolved
}

// withMergedTopics returns ids with every topic merged into one of them, and
// what each merged topic resolves to, so a count over them is the count the
// reader shows: a topic's own requests and those of the topics merged into it.
func withMergedTopics(ctx context.Context, store logstore.WarpTopicStore, ids []string) ([]string, map[string]string, error) {
	merged, err := store.ListWarpTopics(ctx, logstore.WarpTopicFilter{
		Kinds: []string{logstore.WarpTopicKindTopic}, Statuses: []string{logstore.WarpTopicStatusMerged}, OmitCentroids: true,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("read merged topics: %w", err)
	}
	if len(merged) == 0 {
		return ids, nil, nil
	}
	active := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		active[id] = struct{}{}
	}
	into := resolveMergedTopics(active, merged)
	counted := make([]string, 0, len(ids)+len(into))
	counted = append(counted, ids...)
	for id := range into {
		counted = append(counted, id)
	}
	return counted, into, nil
}

// countTopicRequests counts the requests filed under ids, since a time when
// one is given, adding a merged topic's to the topic it resolves to.
func countTopicRequests(ctx context.Context, store logstore.WarpTopicStore, ids []string, into map[string]string, since *time.Time) (map[string]int64, error) {
	usage, err := store.SummarizeWarpTopicAssignments(ctx, logstore.WarpTopicAssignmentFilter{TopicIDs: ids, StartTime: since})
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int64, len(usage))
	for _, entry := range usage {
		id := entry.TopicID
		if target, ok := into[id]; ok {
			id = target
		}
		counts[id] += entry.Requests
	}
	return counts, nil
}

// StartTopicUpkeep starts upkeep on a timer until Shutdown, and starts a
// discovery whenever enough requests are pooled and no topics job is in
// flight. Calling it twice is a no-op.
func (s *Service) StartTopicUpkeep() {
	if s.store == nil {
		return
	}
	s.upkeepOnce.Do(func() {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return
		}
		stop := make(chan struct{})
		s.stopUpkeep = stop
		s.mu.Unlock()
		go s.runTopicUpkeep(stop, topicsDiscoveryInterval)
	})
}

// runTopicUpkeep runs a pass every interval until stop closes. Each pass's
// context comes from one that stop cancels, so Shutdown interrupts a pass in
// flight rather than waiting out its timeout.
func (s *Service) runTopicUpkeep(stop <-chan struct{}, interval time.Duration) {
	base, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()
	go func() {
		select {
		case <-stop:
			cancelBase()
		case <-base.Done():
		}
	}()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(base, topicsUpkeepTimeout)
			s.topicUpkeepTick(ctx)
			cancel()
		case <-stop:
			return
		}
	}
}

// stopTopicUpkeep ends the upkeep loop. Safe without a matching start, and
// safe twice.
func (s *Service) stopTopicUpkeep() {
	s.upkeepStopOnce.Do(func() {
		if s.stopUpkeep != nil {
			close(s.stopUpkeep)
		}
	})
}

// topicUpkeepTick is one pass of the timer: the vector housekeeping that
// touches no topic row, upkeep, and discovery if it is due.
func (s *Service) topicUpkeepTick(ctx context.Context) {
	if s.topicStore() == nil {
		return
	}
	// A retried vector delete and an expired vector touch no topic row, so a
	// long topics job is no reason to leave either.
	if retried, err := s.retryVectorDeletes(ctx); err != nil {
		s.warnf("failed to remove the vectors of deleted logs on retry (%d removed this pass): %v", retried, err)
	} else if retried > 0 {
		s.infof("warp: removed %d vector(s) of deleted logs on retry", retried)
	}
	// One mode for every node that shares a job store: upkeep goes through the
	// queue, where TopicsJobPartition runs one pass at a time. Inline upkeep
	// holds no lock - another node could start a queued pass the moment after
	// it looked - so a node that can see the job store but has no runner leaves
	// upkeep to the nodes that do. Only a node with no job store, which has
	// nothing to coordinate through, keeps topics in this process.
	s.mu.RLock()
	queue, jobs := s.topicJobs, s.backfillJobs
	s.mu.RUnlock()
	switch {
	case queue != nil:
		if err := s.enqueueTopicUpkeep(ctx, queue, time.Now().UTC()); err != nil {
			s.warnf("failed to queue Warp topic upkeep: %v", err)
		}
	case jobs != nil:
		s.warnf("Warp topic upkeep skipped on this node: it shares a job store but has no job runner, so upkeep is left to the nodes that queue it")
	default:
		s.maintainTopicsInline(ctx)
	}
	if swept, err := s.sweepExpiredVectors(ctx, time.Now().UTC()); err != nil {
		s.warnf("failed to sweep expired Warp vectors: %v", err)
	} else if swept > 0 {
		s.infof("warp: swept %d vector(s) of expired logs", swept)
	}
	started, err := s.StartDiscoveryIfDue(ctx)
	if err != nil {
		s.warnf("failed to start Warp topic discovery: %v", err)
	} else if started {
		s.infof("warp topics: started discovery over the pooled requests")
	}
}

// enqueueTopicUpkeep queues an upkeep job in TopicsJobPartition, behind any
// topics job already there, unless one is waiting or running.
//
// Every instance's timer calls it. The job's id is the hour it was queued
// in, so two instances queuing in the same hour make one job: the second is
// refused as a duplicate, which is the mechanism working, not a failure.
// Without a job store to ask, that id is the only guard: the job is still
// queued, since a service with a queue never runs upkeep inline.
func (s *Service) enqueueTopicUpkeep(ctx context.Context, queue TopicJobQueue, now time.Time) error {
	if busy, err := s.jobInFlight(ctx, TopicUpkeepJobKind); err != nil || busy {
		return err
	}
	id := fmt.Sprintf("%s-%d", TopicUpkeepJobKind, now.Truncate(topicsDiscoveryInterval).Unix())
	if err := queue.EnqueuePartitioned(ctx, id, TopicUpkeepJobKind, TopicsJobPartition, "{}", ""); err != nil && !sidekiq.IsDuplicateJobError(err) {
		return err
	}
	return nil
}

// topicUpkeepJobMeta is what an upkeep job reports when it ends.
type topicUpkeepJobMeta struct {
	Retired int    `json:"retired"`
	Merged  int    `json:"merged"`
	Themes  int    `json:"themes"`
	Pruned  int    `json:"pruned"`
	Dropped int64  `json:"dropped"`
	Message string `json:"message,omitempty"`
}

// RunTopicUpkeepJob is the Sidekiq handler for one upkeep pass. Warp being
// off or unset is not a failure: there is nothing to keep.
func (s *Service) RunTopicUpkeepJob(ctx context.Context, job tables.TableSidekiqJob, _ sidekiq.ProgressFunc) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, topicsUpkeepTimeout)
	defer cancel()
	done, err := s.MaintainTopics(ctx)
	meta := topicUpkeepJobMeta{Retired: done.Retired, Merged: done.Merged, Themes: done.Themes, Pruned: done.Pruned, Dropped: done.Dropped}
	switch {
	case errors.Is(err, ErrUnavailable):
		meta.Message, err = "Warp topics are not available; nothing to keep.", nil
	case err != nil:
		meta.Message = "Topic upkeep failed; the topics are as the last pass left them."
	default:
		meta.Message = fmt.Sprintf("Retired %d topic(s), merged %d, pruned %d row(s), dropped %d pooled request(s).", done.Retired, done.Merged, done.Pruned, done.Dropped)
		if done.Retired > 0 || done.Merged > 0 || done.Pruned > 0 || done.Dropped > 0 {
			s.infof("warp topics: retired %d, merged %d, pruned %d, dropped %d pooled", done.Retired, done.Merged, done.Pruned, done.Dropped)
		}
	}
	encoded, encodeErr := sonic.MarshalString(meta)
	if encodeErr != nil {
		encoded = job.Metadata
	}
	return encoded, err
}

// SummarizeTopicUpkeepMeta reports an upkeep job's outcome for generic job
// views.
func SummarizeTopicUpkeepMeta(metadata string) sidekiq.JobSummary {
	var meta topicUpkeepJobMeta
	if sonic.UnmarshalString(metadata, &meta) != nil {
		return sidekiq.JobSummary{}
	}
	return sidekiq.JobSummary{Message: meta.Message}
}

// maintainTopicsInline runs upkeep in this process, for a node with no job
// store: with none, there are no topics jobs it could stand aside for, and no
// node it could coordinate with.
func (s *Service) maintainTopicsInline(ctx context.Context) {
	done, err := s.MaintainTopics(ctx)
	switch {
	case errors.Is(err, ErrUnavailable):
		// Warp is off or unset: no topics to keep.
	case err != nil:
		s.warnf("failed to maintain Warp topics: %v", err)
	case done.Retired > 0 || done.Merged > 0 || done.Pruned > 0 || done.Dropped > 0:
		s.infof("warp topics: retired %d, merged %d, pruned %d, dropped %d pooled", done.Retired, done.Merged, done.Pruned, done.Dropped)
	}
}

// TopicsJobKinds are the jobs in TopicsJobPartition that hold one another
// back: a run or discovery waiting or running refuses another. Upkeep runs in
// the same partition but holds nothing back - it takes its turn - so it is not
// one of them.
var TopicsJobKinds = []string{TopicsJobKind, TopicDiscoveryJobKind}

// LockTopicsJobAdmission takes the lock a job is admitted to
// TopicsJobPartition under, and returns its release. A caller holds it from
// the in-flight lookup to the enqueue: the partition only runs its jobs one
// after another, so two starts that both found nothing in flight would each
// queue one. The lock is this process's; instances sharing a job store can
// still each admit one, so the jobs check again when they start: a run finds
// the topics already seeded and a discovery finds the pool already drained.
func (s *Service) LockTopicsJobAdmission() (unlock func()) {
	s.topicsAdmitMu.Lock()
	return s.topicsAdmitMu.Unlock
}

// topicsJobInFlight reports whether a topics run or a discovery is pending or
// running. Without a job store there are no jobs to wait for.
func (s *Service) topicsJobInFlight(ctx context.Context) (bool, error) {
	s.mu.RLock()
	jobs := s.backfillJobs
	s.mu.RUnlock()
	if jobs == nil {
		return false, nil
	}
	for _, kind := range TopicsJobKinds {
		if busy, err := s.jobInFlight(ctx, kind); err != nil || busy {
			return busy, err
		}
	}
	return false, nil
}

// jobInFlight reports whether a job of kind is pending or running. Without a
// job store there are no jobs to wait for.
func (s *Service) jobInFlight(ctx context.Context, kind string) (bool, error) {
	s.mu.RLock()
	jobs := s.backfillJobs
	s.mu.RUnlock()
	if jobs == nil {
		return false, nil
	}
	inFlight, err := jobs.GetInFlightSidekiqJobByKind(ctx, kind)
	if err != nil {
		return false, fmt.Errorf("check for a running %s job: %w", kind, err)
	}
	return inFlight != nil, nil
}

// StartDiscoveryIfDue enqueues a discovery when the pool holds enough
// requests and no run or discovery is in flight, and reports whether it did.
func (s *Service) StartDiscoveryIfDue(ctx context.Context) (bool, error) {
	s.mu.RLock()
	queue, jobs := s.topicJobs, s.backfillJobs
	s.mu.RUnlock()
	store := s.topicStore()
	if queue == nil || jobs == nil || store == nil {
		return false, nil
	}
	config, err := s.Config(ctx)
	if err != nil || !config.IsConfigured() || config.EmbeddingProvider == "" || config.EmbeddingModel == "" {
		return false, nil
	}
	pooled, err := store.CountWarpTopicUnmatched(ctx, embeddingConfigSignature(config))
	if err != nil {
		return false, fmt.Errorf("count pooled requests: %w", err)
	}
	if pooled < int64(config.EffectiveTopicDiscoveryMinRequests()) {
		return false, nil
	}
	defer s.LockTopicsJobAdmission()()
	if busy, err := s.topicsJobInFlight(ctx); err != nil || busy {
		return false, err
	}
	metadata, err := s.BuildDiscoveryJobMeta(ctx)
	if err != nil {
		return false, err
	}
	if err := queue.EnqueuePartitioned(ctx, uuid.NewString(), TopicDiscoveryJobKind, TopicsJobPartition, metadata, ""); err != nil {
		return false, err
	}
	return true, nil
}
