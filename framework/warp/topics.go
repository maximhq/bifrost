package warp

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand"
	"runtime"
	"slices"
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
	"github.com/maximhq/bifrost/framework/vectorstore"
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
	// topicsMaxVectors bounds how many requests one run clusters, which keeps
	// the vectors it holds flat on a large deployment. The rest of the window
	// is read and assigned to the topics those found.
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
	// topicsLabelDrawn is how many members drawn at random a topic is named
	// from beside its samples. The samples sit at the centre, and the centre
	// of a broad topic is its most generic request: named from those alone,
	// a topic of fifteen thousand coding sessions took the name of one
	// session summary that happened to sit in the middle of them.
	topicsLabelDrawn = 6
	// topicsDuplicateSimilarity is how alike two requests' vectors are when
	// they are the same text: one prompt fanned out to several providers.
	topicsDuplicateSimilarity = 0.98
	// topicsLabelWorkers is how many naming calls run at once. Enough to stop
	// naming from being the slowest part of a run, few enough not to look
	// like a burst to the provider's rate limiter.
	topicsLabelWorkers     = 4
	topicsKMeansIterations = 15
	topicsMaxLabelChars    = 60
	topicsLabelTextChars   = 400
	topicsCleanupTimeout   = 30 * time.Second
	// topicsGroupTimeout bounds the grouping a cancelled run still finishes:
	// one model call over the topic names, and the writes after it.
	topicsGroupTimeout = 2 * time.Minute
	// topicsHydrationBatch is how many sample logs are asked for at once. The
	// gateway's reader answers for at most this many ids and drops the rest
	// silently, which is also why the backfill pages at the same size.
	topicsHydrationBatch = backfillBatchSize
	topicsMaxListed      = 200
	// topicsWriteBatch is how many assignments are written in one call, which
	// is also how often a run that is writing reports progress.
	topicsWriteBatch = 5000
)

// Topic is one topic as the tool reports it, counted over the window asked.
type Topic struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// ThemeID and Theme name the theme the topic is in, when it is in one.
	ThemeID string `json:"theme_id,omitempty"`
	Theme   string `json:"theme,omitempty"`
	// Requests is how many of the window's requests are filed under the topic.
	Requests int64 `json:"requests"`
	// Share is Requests over every request of the window that has a place
	// among the topics, those that matched none included, so the model can
	// say "a third of requests" honestly.
	Share float64 `json:"share"`
	// Sessions is how many distinct sessions those requests came from. A
	// request without a session is not one.
	Sessions int64   `json:"sessions"`
	Errors   int64   `json:"errors"`
	Cost     float64 `json:"cost"`
	Tokens   int64   `json:"tokens"`
	// AvgLatency is the mean latency, in milliseconds, of the requests that
	// measured one.
	AvgLatency float64 `json:"avg_latency_ms"`
	// Cohesion is how alike the topic's requests were when it was made: their
	// average similarity to its centre. A low figure beside a large size is a
	// topic that is probably several.
	Cohesion float64 `json:"cohesion,omitempty"`
	// FirstSeen is when the topic was made. One made inside the window is
	// new, and says so.
	FirstSeen time.Time `json:"first_seen"`
	New       bool      `json:"new_in_window,omitempty"`
	// Trend compares the topic with the period of equal length before the
	// window.
	Trend TopicTrend `json:"trend"`
	// Series is the window bucket by bucket, when the caller asked for one.
	Series       []TopicPoint `json:"series,omitempty"`
	SampleLogIDs []string     `json:"example_log_ids,omitempty"`
	// SampleLinks each open one of the requests nearest the topic's
	// centre. Named as examples on the wire, because a link on a row is
	// otherwise read as "the requests behind this row".
	SampleLinks []string `json:"example_links,omitempty"`
	// Link opens the topic's requests in the Logs view, with the window and
	// filters the count ran under. Set by the tool, which has the filters.
	Link string `json:"link,omitempty"`
}

// TopicTrend is a topic against the period of equal length before the
// window, in the shape the ranking tools report theirs.
type TopicTrend struct {
	// HasPreviousPeriod is false when the topic had no request in the
	// previous period, in which case RequestsTrend is zero rather than a
	// percentage of nothing.
	HasPreviousPeriod bool    `json:"has_previous_period"`
	PreviousRequests  int64   `json:"previous_requests"`
	RequestsTrend     float64 `json:"requests_trend"`
	// TokensTrend and CostTrend are nil when the previous period had none and
	// this one has some: there is no percentage, and 0 would read as
	// unchanged.
	TokensTrend *float64 `json:"tokens_trend"`
	CostTrend   *float64 `json:"cost_trend"`
}

// TopicPoint is one bucket of a topic's series.
type TopicPoint struct {
	Bucket   time.Time `json:"bucket"`
	Requests int64     `json:"requests"`
	Errors   int64     `json:"errors"`
	Cost     float64   `json:"cost"`
	Tokens   int64     `json:"tokens"`
}

// TopicTheme is a theme as the tool reports it: its topics' counts added
// up, and the topics themselves.
type TopicTheme struct {
	ID         string       `json:"id"`
	Label      string       `json:"label"`
	Requests   int64        `json:"requests"`
	Share      float64      `json:"share"`
	Sessions   int64        `json:"sessions"` // an upper bound: one session can span its topics
	Errors     int64        `json:"errors"`
	Cost       float64      `json:"cost"`
	Tokens     int64        `json:"tokens"`
	AvgLatency float64      `json:"avg_latency_ms"`
	Trend      TopicTrend   `json:"trend"`
	Series     []TopicPoint `json:"series,omitempty"`
	Topics     []Topic      `json:"topics"`
	// Link opens the theme's requests in the Logs view. Set by the tool.
	Link string `json:"link,omitempty"`
}

// TopicQuery is what list_topics asks the reader for.
type TopicQuery struct {
	// Filter selects the requests counted. StartTime and EndTime are required:
	// the trend needs a window to shift.
	Filter logstore.WarpTopicAssignmentFilter
	// Limit bounds the topics returned, largest first. Out of range means
	// topicsMaxListed.
	Limit int
	// BucketSeconds, when positive, gives each returned topic its series.
	BucketSeconds int64
	// NameContains keeps only topics whose name contains it, ignoring case
	// and surrounding space, before Limit applies. Empty keeps every topic.
	NameContains string
	// ByTheme answers with themes, each holding its topics, Limit applying
	// to the themes; topics in no theme come back on their own.
	ByTheme bool
}

// TopicList is the reader's answer.
type TopicList struct {
	Topics []Topic `json:"topics"`
	// Themes and Ungrouped are the answer to a ByTheme query: the themes,
	// largest first, and the topics in none. Topics is then empty.
	Themes    []TopicTheme `json:"themes,omitempty"`
	Ungrouped []Topic      `json:"ungrouped,omitempty"`
	// MatchedThemes is how many themes had a request in the window, before
	// Limit.
	MatchedThemes int `json:"-"`
	// Known is how many topics the deployment has for the embedding model in
	// use, whether or not the window reached any. Zero means none have been
	// built.
	Known int `json:"-"`
	// Matched is how many topics had a request in the window, before Limit.
	Matched int `json:"-"`
	// TotalRequests is every request in the window with a place among the
	// topics, matched or not, and Unmatched how many of those matched none.
	TotalRequests int64 `json:"-"`
	Unmatched     int64 `json:"-"`
	// PreviousStart and PreviousEnd are the period the trends compare with.
	PreviousStart time.Time `json:"-"`
	PreviousEnd   time.Time `json:"-"`
}

// TopicSummary is one topic as the job status reports it.
type TopicSummary struct {
	Label    string  `json:"label"`
	Size     int     `json:"size"`
	Cohesion float64 `json:"cohesion"`
}

// TopicLister is what the list_topics tool needs.
type TopicLister interface {
	ListTopics(ctx context.Context, query TopicQuery) (*TopicList, error)
}

// TopicReader reads the topics for the tool.
type TopicReader struct {
	store  configstore.WarpStore
	topics logstore.WarpTopicStore
}

// CallerScopedTopicStore is a topic store that applies the caller's row scope
// to its assignment reads itself, resolved per call from who is asking rather
// than read from a queryscope on the context. The enterprise log store does.
// list_topics serves a restricted caller only when the scope reaches the topic
// reads one of those two ways.
type CallerScopedTopicStore interface {
	ScopesWarpTopicReadsPerCall() bool
}

// scopesCallerReadsPerCall reports whether the store under the reader applies
// the caller's scope to topic reads on its own.
func (r *TopicReader) scopesCallerReadsPerCall() bool {
	if r == nil {
		return false
	}
	scoped, ok := r.topics.(CallerScopedTopicStore)
	return ok && scoped.ScopesWarpTopicReadsPerCall()
}

// topicReadsScopedPerCall reports whether a lister's reads are scoped by its
// store whatever the context carries.
func topicReadsScopedPerCall(lister TopicLister) bool {
	scoped, ok := lister.(interface{ scopesCallerReadsPerCall() bool })
	return ok && scoped.scopesCallerReadsPerCall()
}

// NewTopicReader builds a reader over the log store's topic tables.
func NewTopicReader(store configstore.WarpStore, topics logstore.WarpTopicStore) *TopicReader {
	return &TopicReader{store: store, topics: topics}
}

// ListTopics returns the topics the window's requests were filed under,
// largest first, with what those requests add up to.
//
// Only the topics built with the embedding model Warp is configured with are
// listed: a topic's centre means nothing against vectors from another model,
// and a changed configuration is read as a deployment with no topics yet
// rather than one with stale ones.
//
// Every count comes from the assignments table through the caller's access
// scope, so a restricted caller is answered from the requests they may see:
// a topic none of those fall under is not listed, one some do is listed with
// its name, and the examples offered are the ones they may open.
func (r *TopicReader) ListTopics(ctx context.Context, query TopicQuery) (*TopicList, error) {
	if r == nil || r.store == nil || r.topics == nil {
		return nil, ErrUnavailable
	}
	if query.Filter.StartTime == nil || query.Filter.EndTime == nil {
		return nil, fmt.Errorf("list topics: the window needs both ends")
	}
	row, err := r.store.GetWarpConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("read Warp configuration: %w", err)
	}
	config := configFromRow(row)
	if query.Limit < 1 || query.Limit > topicsMaxListed {
		query.Limit = topicsMaxListed
	}
	rows, err := r.topics.ListWarpTopics(ctx, logstore.WarpTopicFilter{
		Kinds: []string{logstore.WarpTopicKindTopic}, Statuses: []string{logstore.WarpTopicStatusActive},
		EmbeddingSignature: embeddingConfigSignature(config), OmitCentroids: true,
	})
	if err != nil {
		return nil, fmt.Errorf("list topics: %w", err)
	}
	span := query.Filter.EndTime.Sub(*query.Filter.StartTime)
	list := &TopicList{Known: len(rows), PreviousStart: query.Filter.StartTime.Add(-span), PreviousEnd: *query.Filter.StartTime}
	if len(rows) == 0 {
		return list, nil
	}
	// Counted over the active topics, the topics merged into them, and the
	// requests that matched none. Requests still filed under a topic that
	// has since been retired are none of those: they belong to a set of
	// topics that is no longer the answer.
	active := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		active[row.ID] = struct{}{}
	}
	merged, err := r.topics.ListWarpTopics(ctx, logstore.WarpTopicFilter{
		Kinds: []string{logstore.WarpTopicKindTopic}, Statuses: []string{logstore.WarpTopicStatusMerged},
		EmbeddingSignature: embeddingConfigSignature(config), OmitCentroids: true,
	})
	if err != nil {
		return nil, fmt.Errorf("list merged topics: %w", err)
	}
	into := resolveMergedTopics(active, merged)
	themeRows, err := r.topics.ListWarpTopics(ctx, logstore.WarpTopicFilter{
		Kinds: []string{logstore.WarpTopicKindTheme}, Statuses: []string{logstore.WarpTopicStatusActive},
		EmbeddingSignature: embeddingConfigSignature(config), OmitCentroids: true,
	})
	if err != nil {
		return nil, fmt.Errorf("list themes: %w", err)
	}
	themeLabel := make(map[string]string, len(themeRows))
	for _, theme := range themeRows {
		themeLabel[theme.ID] = theme.Label
	}
	ids := make([]string, 0, len(rows)+len(into)+1)
	for id := range active {
		ids = append(ids, id)
	}
	for id := range into {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	filter := query.Filter
	filter.TopicIDs = append(ids, "")
	current, err := r.topics.SummarizeWarpTopicAssignments(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("count topic requests: %w", err)
	}
	// The period before the window, ending just short of its start. The
	// store's bounds are inclusive at both ends, and a millisecond is the
	// finest tick every backend keeps.
	previousFilter := filter
	previousStart, previousEnd := list.PreviousStart, list.PreviousEnd.Add(-time.Millisecond)
	previousFilter.StartTime, previousFilter.EndTime = &previousStart, &previousEnd
	previous, err := r.topics.SummarizeWarpTopicAssignments(ctx, previousFilter)
	if err != nil {
		return nil, fmt.Errorf("count previous topic requests: %w", err)
	}
	usage := foldTopicUsage(current, into)
	before := foldTopicUsage(previous, into)
	for id, entry := range usage {
		list.TotalRequests += entry.Requests
		if id == "" {
			list.Unmatched = entry.Requests
		}
	}

	needle := strings.ToLower(strings.TrimSpace(query.NameContains))
	topics := make([]Topic, 0, len(rows))
	for _, row := range rows {
		entry, ok := usage[row.ID]
		if !ok {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(row.Label), needle) {
			continue
		}
		topic := Topic{
			ID: row.ID, Label: row.Label, Requests: entry.Requests, Sessions: entry.Sessions, Errors: entry.Errors,
			Cost: entry.Cost, Tokens: entry.Tokens, AvgLatency: entry.AvgLatency, Cohesion: row.Cohesion,
			FirstSeen: row.CreatedAt.UTC(), Trend: topicTrend(before[row.ID], entry),
		}
		topic.New = !topic.FirstSeen.Before(*query.Filter.StartTime) && !topic.FirstSeen.After(*query.Filter.EndTime)
		if list.TotalRequests > 0 {
			topic.Share = float64(entry.Requests) / float64(list.TotalRequests)
		}
		if label, ok := themeLabel[row.ParentID]; ok {
			topic.ThemeID, topic.Theme = row.ParentID, label
		}
		// Linked: the first few, which mix the centre with the members drawn.
		examples := row.ExampleLogIDs()
		topic.SampleLogIDs = examples[:min(len(examples), topicsLabelSamples)]
		topics = append(topics, topic)
	}
	sortTopics(topics)
	if query.ByTheme {
		return r.listByTheme(ctx, query, list, topics, usage, before, into)
	}
	list.Matched = len(topics)
	if len(topics) > query.Limit {
		topics = topics[:query.Limit]
	}
	if err := r.attachTopicExamples(ctx, topics); err != nil {
		return nil, err
	}
	if query.BucketSeconds > 0 {
		if err := r.attachTopicSeries(ctx, query, topics, into); err != nil {
			return nil, err
		}
	}
	list.Topics = topics
	return list, nil
}

// sortTopics orders topics largest first, then by name.
func sortTopics(topics []Topic) {
	sort.SliceStable(topics, func(i, j int) bool {
		if topics[i].Requests != topics[j].Requests {
			return topics[i].Requests > topics[j].Requests
		}
		return topics[i].Label < topics[j].Label
	})
}

// listByTheme groups the topics by theme and adds each theme's topics up,
// largest theme first. A topic in no theme is returned on its own.
func (r *TopicReader) listByTheme(ctx context.Context, query TopicQuery, list *TopicList, topics []Topic, usage, before map[string]logstore.WarpTopicUsage, into map[string]string) (*TopicList, error) {
	byTheme := make(map[string]*TopicTheme)
	var order []string
	for _, topic := range topics {
		if topic.ThemeID == "" {
			list.Ungrouped = append(list.Ungrouped, topic)
			continue
		}
		theme, ok := byTheme[topic.ThemeID]
		if !ok {
			theme = &TopicTheme{ID: topic.ThemeID, Label: topic.Theme}
			byTheme[topic.ThemeID] = theme
			order = append(order, topic.ThemeID)
		}
		theme.Topics = append(theme.Topics, topic)
	}
	themes := make([]TopicTheme, 0, len(order))
	for _, id := range order {
		theme := byTheme[id]
		var current, previous logstore.WarpTopicUsage
		for _, topic := range theme.Topics {
			current = addTopicUsage(current, usage[topic.ID])
			previous = addTopicUsage(previous, before[topic.ID])
		}
		theme.Requests, theme.Sessions, theme.Errors = current.Requests, current.Sessions, current.Errors
		theme.Cost, theme.Tokens, theme.AvgLatency = current.Cost, current.Tokens, current.AvgLatency
		theme.Trend = topicTrend(previous, current)
		if list.TotalRequests > 0 {
			theme.Share = float64(current.Requests) / float64(list.TotalRequests)
		}
		themes = append(themes, *theme)
	}
	sort.SliceStable(themes, func(i, j int) bool {
		if themes[i].Requests != themes[j].Requests {
			return themes[i].Requests > themes[j].Requests
		}
		return themes[i].Label < themes[j].Label
	})
	list.MatchedThemes = len(themes)
	if len(themes) > query.Limit {
		themes = themes[:query.Limit]
	}
	// Examples and series for every topic shown, then each theme's series
	// is its topics' added up.
	shown := make([]Topic, 0, len(topics))
	for _, theme := range themes {
		shown = append(shown, theme.Topics...)
	}
	shown = append(shown, list.Ungrouped...)
	if err := r.attachTopicExamples(ctx, shown); err != nil {
		return nil, err
	}
	if query.BucketSeconds > 0 {
		if err := r.attachTopicSeries(ctx, query, shown, into); err != nil {
			return nil, err
		}
	}
	index := 0
	for t := range themes {
		theme := &themes[t]
		var series []TopicPoint
		for m := range theme.Topics {
			theme.Topics[m] = shown[index]
			index++
			series = addTopicSeries(series, theme.Topics[m].Series)
		}
		theme.Series = series
	}
	list.Ungrouped = shown[index:]
	list.Themes = themes
	return list, nil
}

// addTopicUsage adds one topic's usage into a theme's running total, the
// latency as a request-weighted mean.
func addTopicUsage(sum, entry logstore.WarpTopicUsage) logstore.WarpTopicUsage {
	if sum.Requests+entry.Requests > 0 {
		sum.AvgLatency = (sum.AvgLatency*float64(sum.Requests) + entry.AvgLatency*float64(entry.Requests)) / float64(sum.Requests+entry.Requests)
	}
	sum.Requests += entry.Requests
	sum.Sessions += entry.Sessions
	sum.Errors += entry.Errors
	sum.Cost += entry.Cost
	sum.Tokens += entry.Tokens
	return sum
}

// addTopicSeries adds one series into another, bucket by bucket, keeping
// the buckets in order.
func addTopicSeries(sum, series []TopicPoint) []TopicPoint {
	merged := make([]TopicPoint, 0, len(sum)+len(series))
	i, j := 0, 0
	for i < len(sum) || j < len(series) {
		switch {
		case j == len(series) || (i < len(sum) && sum[i].Bucket.Before(series[j].Bucket)):
			merged = append(merged, sum[i])
			i++
		case i == len(sum) || series[j].Bucket.Before(sum[i].Bucket):
			merged = append(merged, series[j])
			j++
		default:
			point := sum[i]
			point.Requests += series[j].Requests
			point.Errors += series[j].Errors
			point.Cost += series[j].Cost
			point.Tokens += series[j].Tokens
			merged = append(merged, point)
			i++
			j++
		}
	}
	return merged
}

// foldTopicUsage adds each topic's usage into the topic it was merged into,
// keyed by the surviving id. Sums add; the session count is a count of
// distinct sessions and cannot be added across topics, so it is the sum as
// an upper bound, which is what a topic that absorbed another reports.
func foldTopicUsage(usage []logstore.WarpTopicUsage, into map[string]string) map[string]logstore.WarpTopicUsage {
	folded := make(map[string]logstore.WarpTopicUsage, len(usage))
	for _, entry := range usage {
		id := entry.TopicID
		if target, ok := into[id]; ok {
			id = target
		}
		sum := addTopicUsage(folded[id], entry)
		sum.TopicID = id
		folded[id] = sum
	}
	return folded
}

// topicTrend compares a topic's window with the period before it, in the
// terms the ranking tools use (see trendBlock in flows.go).
func topicTrend(previous, current logstore.WarpTopicUsage) TopicTrend {
	trend := TopicTrend{
		HasPreviousPeriod: previous.Requests > 0,
		PreviousRequests:  previous.Requests,
		TokensTrend:       topicMetricTrend(float64(previous.Tokens), float64(current.Tokens)),
		CostTrend:         topicMetricTrend(previous.Cost, current.Cost),
	}
	if previous.Requests > 0 {
		trend.RequestsTrend = pctChange(float64(previous.Requests), float64(current.Requests))
	}
	return trend
}

// topicMetricTrend is metricTrend with a typed result: nil for a zero
// baseline that is nonzero now, the percentage otherwise.
func topicMetricTrend(old, new float64) *float64 {
	if old == 0 && new != 0 {
		return nil
	}
	change := pctChange(old, new)
	return &change
}

// attachTopicExamples turns each topic's example ids into links, keeping
// only the examples the caller may open.
//
// Examples are chosen from the whole deployment when a topic is made. The
// assignments table says which of them the caller's access scope reaches,
// and an example outside it would be a link to a request they cannot see.
func (r *TopicReader) attachTopicExamples(ctx context.Context, topics []Topic) error {
	var ids []string
	for _, topic := range topics {
		ids = append(ids, topic.SampleLogIDs...)
	}
	if len(ids) == 0 {
		return nil
	}
	visible, err := r.topics.ListWarpTopicAssignments(ctx, logstore.WarpTopicAssignmentFilter{LogIDs: ids}, len(ids))
	if err != nil {
		return fmt.Errorf("check topic examples: %w", err)
	}
	reachable := make(map[string]struct{}, len(visible))
	for _, assignment := range visible {
		reachable[assignment.LogID] = struct{}{}
	}
	for index := range topics {
		topic := &topics[index]
		kept := topic.SampleLogIDs[:0]
		for _, logID := range topic.SampleLogIDs {
			if _, ok := reachable[logID]; ok {
				kept = append(kept, logID)
				topic.SampleLinks = append(topic.SampleLinks, logDetailLink(logID))
			}
		}
		topic.SampleLogIDs = kept
	}
	return nil
}

// attachTopicSeries gives each listed topic its window bucket by bucket,
// with requests filed under a merged topic counted toward its survivor.
func (r *TopicReader) attachTopicSeries(ctx context.Context, query TopicQuery, topics []Topic, into map[string]string) error {
	if len(topics) == 0 {
		return nil
	}
	listed := make(map[string]int, len(topics))
	filter := query.Filter
	filter.TopicIDs = nil
	for index, topic := range topics {
		listed[topic.ID] = index
		filter.TopicIDs = append(filter.TopicIDs, topic.ID)
	}
	for child, survivor := range into {
		if _, ok := listed[survivor]; ok {
			filter.TopicIDs = append(filter.TopicIDs, child)
		}
	}
	sort.Strings(filter.TopicIDs)
	buckets, err := r.topics.BucketWarpTopicAssignments(ctx, filter, query.BucketSeconds)
	if err != nil {
		return fmt.Errorf("bucket topic requests: %w", err)
	}
	for _, bucket := range buckets {
		id := bucket.TopicID
		if target, ok := into[id]; ok {
			id = target
		}
		index, ok := listed[id]
		if !ok {
			continue
		}
		series := topics[index].Series
		at := time.Unix(bucket.Bucket, 0).UTC()
		if n := len(series); n > 0 && series[n-1].Bucket.Equal(at) {
			point := &series[n-1]
			point.Requests += bucket.Requests
			point.Errors += bucket.Errors
			point.Cost += bucket.Cost
			point.Tokens += bucket.Tokens
			continue
		}
		topics[index].Series = append(series, TopicPoint{Bucket: at, Requests: bucket.Requests, Errors: bucket.Errors, Cost: bucket.Cost, Tokens: bucket.Tokens})
	}
	return nil
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

// topicFilterFields are the FilterSchema fields a topic count can take: the
// window and the columns the assignments table carries. The rest describe a
// request in ways a narrow assignment row does not record.
var topicFilterFields = []string{
	"start_time", "end_time", "providers", "models", "status", "virtual_key_ids", "team_ids",
	"customer_ids", "user_ids", "business_unit_ids", "apps", "session_id", "scope",
}

// topicsDefaultLookback is the window list_topics takes when the model names
// none. Longer than the other tools' day because a topic is a pattern, and a
// day of one is rarely enough requests to be one.
const topicsDefaultLookback = 7 * 24 * time.Hour

// TopicFilterSchema is FilterSchema cut down to topicFilterFields, so the
// model is offered only what the tool can honour. Built from FilterSchema
// rather than written out, so a field's description is the same on every
// tool that takes it.
var TopicFilterSchema = buildTopicFilterSchema()

func buildTopicFilterSchema() string {
	var schema struct {
		Type        string         `json:"type"`
		Description string         `json:"description"`
		Properties  map[string]any `json:"properties"`
	}
	if err := sonic.UnmarshalString(FilterSchema, &schema); err != nil {
		panic(fmt.Sprintf("FilterSchema is not the object it is declared as: %v", err))
	}
	kept := make(map[string]any, len(topicFilterFields))
	for _, field := range topicFilterFields {
		property, ok := schema.Properties[field]
		if !ok {
			panic(fmt.Sprintf("FilterSchema has no %q field for the topic filter to keep", field))
		}
		kept[field] = property
	}
	schema.Properties = kept
	schema.Description = "Narrows which requests are counted. Omit a field to leave that dimension unfiltered. If start_time is omitted the last 7 days are used."
	encoded, err := sonic.ConfigStd.MarshalToString(schema)
	if err != nil {
		panic(fmt.Sprintf("topic filter schema: %v", err))
	}
	return encoded
}

// topicFilterArg reads the filters argument the way filterArg does, over the
// fields a topic count can take and with the topics' own default window.
func topicFilterArg(args map[string]any, now time.Time, scope Scope) (*logstore.SearchFilters, error) {
	raw, err := filtersObject(args)
	if err != nil {
		return nil, err
	}
	var refused []string
	for key := range raw {
		if !slices.Contains(topicFilterFields, key) {
			refused = append(refused, key)
		}
	}
	if len(refused) > 0 {
		sort.Strings(refused)
		return nil, &unknownFilterFieldsError{fields: refused, supported: slices.Sorted(slices.Values(topicFilterFields))}
	}
	filters, err := parseFiltersWithLookback(raw, now, topicsDefaultLookback)
	if err != nil {
		return nil, err
	}
	all, err := scopeAllArg(raw)
	if err != nil {
		return nil, err
	}
	if err := refuseWarpApp(filters); err != nil {
		return nil, err
	}
	applyScope(filters, scope, all)
	return filters, nil
}

// topicAssignmentFilter is the store's selection for the parsed filters.
func topicAssignmentFilter(filters *logstore.SearchFilters) logstore.WarpTopicAssignmentFilter {
	selection := logstore.WarpTopicAssignmentFilter{
		StartTime: filters.StartTime, EndTime: filters.EndTime,
		UserIDs: filters.UserIDs, TeamIDs: filters.TeamIDs, VirtualKeyIDs: filters.VirtualKeyIDs,
		CustomerIDs: filters.CustomerIDs, BusinessUnitIDs: filters.BusinessUnitIDs,
		Providers: filters.Providers, Models: filters.Models, Apps: filters.Apps, Statuses: filters.Status,
	}
	if filters.SessionID != "" {
		selection.SessionIDs = []string{filters.SessionID}
	}
	return selection
}

// listTopicsTool answers "what do people ask about" from the topic each
// indexed request was filed under.
func listTopicsTool() Tool {
	return Tool{
		name: "list_topics",
		description: "List the topics people talk to the models about in a time window, largest first, counted from the topic each indexed request was filed under. " +
			"Use this first for questions about what users ask, what conversations are about, which topics are most common, whether a topic is growing or shrinking, what is new, and what a topic costs. " +
			"Each topic's id goes into the topic_ids filter of query_logs, count_logs and the other tools to reach the requests behind it. " +
			"The window defaults to the last 7 days; filters narrow it to a team, customer, user, virtual key, model, provider, app, status or session, like the other tools. " +
			"Each topic carries requests, share, distinct sessions, errors, cost, tokens, average latency, a trend block against the period of equal length before the window (has_previous_period, requests_trend, tokens_trend, cost_trend), when it was first seen (new_in_window marks one that appeared inside the window), " +
			"and example_links that each open one example request. With interval, each topic also carries its series, bucket by bucket. " +
			"With group_by theme, the answer is the themes the topics are grouped into - broad subjects such as Billing over the topics Refund Requests and Invoice Questions - each with the same counts added up over its topics, and the topics inside it; the call for 'what broad areas', 'what kinds of things', or a deployment with too many topics to list. " +
			"Requests, shares and sessions count requests and sessions, not people: every model call is one request, so a five-turn chat is five.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "filters": ` + TopicFilterSchema + `,
    "interval": {"type": "string", "enum": ["hour", "day"], "description": "Also return each topic's window bucket by bucket - one point per UTC hour or day. The call for 'per day' breakdowns and for how a topic moved inside the window; the trend block already answers 'up or down since the period before'. At most 200 buckets."},
    "group_by": {"type": "string", "enum": ["topic", "theme"], "description": "topic (default) lists topics. theme lists the themes the topics are grouped into, each with its topics inside, and the topics in no theme on their own; limit then counts themes."},
    "limit": {"type": "integer", "minimum": 1, "maximum": 50, "description": "Topics - or with group_by theme, themes - to return, largest first. Default 15."}
  }
}`,
		execute: func(ctx context.Context, deps *ToolDeps, args map[string]any) (any, error) {
			if deps.topics == nil {
				return nil, fmt.Errorf("topics are not available: connect a vector store and configure embeddings")
			}
			// The topic tables scope a caller's reads by a queryscope on the
			// context, or by the store itself when it resolves the caller per
			// call (CallerScopedTopicStore). A restricted caller reached by
			// neither would be answered from every request, and a named team or
			// customer would read theirs. Refuse rather than answer unscoped.
			if !deps.scope.Unrestricted && queryscope.FromContext(ctx) == nil && !topicReadsScopedPerCall(deps.topics) {
				return nil, fmt.Errorf("topics are not available to a caller whose access covers part of the deployment; " +
					"take one bounded sample with query_logs (include_content, limit 25) and summarise the themes, saying it is a sample of what they can see")
			}
			filters, err := topicFilterArg(args, deps.now(), deps.scope)
			if err != nil {
				return nil, err
			}
			limit, err := intArg(args, "limit", 15, 50)
			if err != nil {
				return nil, err
			}
			interval, err := enumArg(args, "interval", "", []string{"hour", "day"})
			if err != nil {
				return nil, err
			}
			groupBy, err := enumArg(args, "group_by", "topic", []string{"topic", "theme"})
			if err != nil {
				return nil, err
			}
			query := TopicQuery{Filter: topicAssignmentFilter(filters), Limit: limit, ByTheme: groupBy == "theme"}
			if interval != "" {
				query.BucketSeconds = 3600
				if interval == "day" {
					query.BucketSeconds = 86400
				}
				if histogramBucketCount(*filters.StartTime, *filters.EndTime, query.BucketSeconds) > MaxHistogramBuckets {
					return nil, fmt.Errorf(`interval %q over this range produces more than %d buckets; use interval "day" or a shorter range`, interval, MaxHistogramBuckets)
				}
			}
			list, err := deps.topics.ListTopics(ctx, query)
			if err != nil {
				return nil, err
			}
			linkTo := func(id string) string {
				narrowed := *filters
				narrowed.TopicIDs = []string{id}
				return logsViewLink(&narrowed)
			}
			linkTopics := func(topics []Topic) {
				for index := range topics {
					topics[index].Link = linkTo(topics[index].ID)
				}
			}
			linkTopics(list.Topics)
			linkTopics(list.Ungrouped)
			for index := range list.Themes {
				list.Themes[index].Link = linkTo(list.Themes[index].ID)
				linkTopics(list.Themes[index].Topics)
			}
			out := map[string]any{
				"total_requests":     list.TotalRequests,
				"unmatched_requests": list.Unmatched,
				"window":             resolvedWindow(filters),
				"previous_window":    formatWindow(list.PreviousStart, list.PreviousEnd),
				"scope":              scopeNote(filters, deps.scope),
			}
			if query.ByTheme {
				out["themes"] = list.Themes
				out["returned"] = len(list.Themes)
				out["total_themes"] = list.MatchedThemes
				out["ungrouped_topics"] = list.Ungrouped
				if len(list.Themes) == 0 && len(list.Ungrouped) > 0 {
					out["hint"] = "The topics are not grouped into themes - there are too few to group, or the grouping has not run since they changed. Answer from ungrouped_topics, which are the topics themselves."
				}
			} else {
				out["topics"] = list.Topics
				out["returned"] = len(list.Topics)
				out["total_topics"] = list.Matched
			}
			switch {
			case list.Known == 0:
				out["hint"] = "No topics have been built yet. An administrator can build them from Warp settings. " +
					"Until then, take one bounded sample with query_logs (include_content, limit 25) and summarise the themes, saying it is a sample."
			case len(list.Topics) == 0 && len(list.Themes) == 0 && len(list.Ungrouped) == 0:
				out["hint"] = "Topics exist, but no request in this window and scope was filed under one. Say so; a wider window or scope may hold some."
			default:
				out["counts"] = "requests, share, total_requests and unmatched_requests count requests, and sessions counts sessions. Every model call is one request, so a chat of five turns " +
					"or an agent making five calls is five. Say requests, not conversations or people. Share is of total_requests, which includes the unmatched_requests that fell under no topic."
				out["trend"] = "Each topic's trend compares it with previous_window, the period of equal length just before window. " +
					"A null tokens_trend or cost_trend means that metric was zero in the previous period and is not now: describe it as new rather than as a change."
				out["links"] = "Each topic's and theme's link opens its requests in the Logs view, over this window and these filters: link the name to it. " +
					"Each entry of example_links opens one example request from the topic; offer those as examples, after the name."
				out["requests"] = "For the requests behind a topic or theme - a list, a count, a breakdown, who sent them - call query_logs, count_logs or query_usage_by with topic_ids set to its id and the same window and filters."
				out["cannot_answer"] = "Not available yet: how many people asked about a topic (sessions are sessions, not people; a query_usage_by user ranking with topic_ids counts the users with a request in it). " +
					"Asked for that, answer what the topics do show, then say it is not available yet."
			}
			return out, nil
		},
	}
}

// RegisterTopics binds the clustering, discovery and upkeep jobs to the shared
// Sidekiq runner. Only clustering pages vectors out of the vector store;
// discovery and upkeep read the log store, so they are registered without one.
// An upkeep that finished is routine, and is kept out of the jobs listing
// unless it failed.
//
// They are bound whether or not there is a log reader yet: a deployment that
// turns logging on after startup gets its reader later, through SetLogReader,
// and without a queue by then its node would skip upkeep until restart. Each
// job finds the topic store when it runs and does nothing without one, and the
// timer queues nothing while there is none.
func (s *Service) RegisterTopics(runner *sidekiq.Runner) {
	if runner == nil || s.store == nil {
		return
	}
	if s.vectorStore != nil {
		runner.Register(TopicsJobKind, s.RunTopicsJob)
	}
	runner.Register(TopicDiscoveryJobKind, s.RunDiscoveryJob)
	runner.RegisterSummarizer(TopicDiscoveryJobKind, SummarizeBackfillMeta)
	runner.Register(TopicUpkeepJobKind, s.RunTopicUpkeepJob)
	runner.RegisterSummarizer(TopicUpkeepJobKind, SummarizeTopicUpkeepMeta)
	runner.RegisterQuietOnSuccess(TopicUpkeepJobKind)
	s.mu.Lock()
	s.topicJobs = runner
	s.mu.Unlock()
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
	// A run seeds the topics for an embedding model, once. After that they
	// are kept up to date as requests are indexed: each is filed under its
	// topic, what matches none is discovered from the pool, and upkeep retires
	// the stale and merges the alike. A second run would replace all of that
	// with one window's clustering. Should upkeep ever retire every topic, the
	// model is back to having none and can be seeded again.
	signature := embeddingConfigSignature(config)
	seeded, err := countSeededTopics(ctx, s.topicStore(), signature)
	if err != nil {
		return "", err
	}
	if seeded > 0 {
		return "", fmt.Errorf("%w: %d topic(s) are kept up to date as requests are indexed", ErrTopicsAlreadyBuilt, seeded)
	}
	return marshalBackfillMeta(BackfillJobMeta{
		StartTime:       start.UTC(),
		EndTime:         end.UTC(),
		ConfigSignature: signature,
		Namespace:       config.EffectiveLogVectorStoreNamespace(),
	})
}

// countSeededTopics counts the active topics an embedding model's requests are
// filed under, which is what makes a seed run for it a replacement.
func countSeededTopics(ctx context.Context, store logstore.WarpTopicStore, signature string) (int, error) {
	seeded, err := store.ListWarpTopics(ctx, logstore.WarpTopicFilter{
		Kinds: []string{logstore.WarpTopicKindTopic}, Statuses: []string{logstore.WarpTopicStatusActive}, EmbeddingSignature: signature, OmitCentroids: true,
	})
	if err != nil {
		return 0, fmt.Errorf("read topics: %w", err)
	}
	return len(seeded), nil
}

// topicsIndexedProbePage is how many vectors HasIndexedRequests reads per page.
const topicsIndexedProbePage = 100

// HasIndexedRequests reports whether the configured namespace holds any of
// Warp's log vectors, for the settings page to ask for a backfill before a
// seed run that would have nothing to cluster.
//
// Small pages are read until one holds a Warp log vector or the namespace runs
// out. A store that applies the filter answers on the first; one that does not
// can fill a page with other entries, which is why warp_log is checked on what
// comes back, as the topics run checks it, and the next page read. An error is
// returned as one, not as "nothing indexed": a store that cannot say must not
// lock the seed run away on a guess.
func (s *Service) HasIndexedRequests(ctx context.Context) (bool, error) {
	s.mu.RLock()
	vectors := s.vectorStore
	s.mu.RUnlock()
	if vectors == nil {
		return false, ErrNoVectorStore
	}
	config, err := s.Config(ctx)
	if err != nil {
		return false, err
	}
	namespace := config.EffectiveLogVectorStoreNamespace()
	isLog := []vectorstore.Query{{Field: "warp_log", Operator: vectorstore.QueryOperatorEqual, Value: true}}
	var cursor *string
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		results, next, err := vectors.GetAll(ctx, namespace, isLog, []string{"log_id", "warp_log"}, cursor, topicsIndexedProbePage)
		if err != nil {
			return false, fmt.Errorf("read log vectors: %w", err)
		}
		for _, result := range results {
			if propertyBool(result.Properties["warp_log"]) {
				return true, nil
			}
		}
		// A cursor that does not move would read the same page forever.
		if len(results) == 0 || next == nil || *next == "" || (cursor != nil && *next == *cursor) {
			return false, nil
		}
		cursor = next
	}
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
	// Admission refuses a seed run once topics exist, but its lock is one
	// process's: another instance sharing the job store can queue a run too.
	// The partition runs them one after another, so checking again here is
	// what holds, and a run queued behind one that seeded leaves its topics be.
	seeded, err := countSeededTopics(ctx, topicStore, meta.ConfigSignature)
	if err != nil {
		meta.LastError = err.Error()
		return snapshot(), err
	}
	if seeded > 0 {
		meta.Message = fmt.Sprintf("%d topic(s) were already built for this embedding model, so this run left them as they are.", seeded)
		return snapshot(), progress(snapshot())
	}

	// 1-3. Read the window, cluster it in batches, merge the batches' clusters,
	// and give whatever is read after that to the nearest topic. The topics
	// are written once the merge has settled them, and every request is filed
	// under its topic a batch at a time from then on, so a run holds a batch
	// of placements and not the window.
	limits := s.topicLimits
	if limits.batch == 0 {
		limits = defaultTopicLimits()
	}
	run := newTopicRun(limits, topicsSeed(meta))
	writer := newTopicWriter(topicStore, &meta, run, limits.write)
	// abandon takes back what the run wrote, on a context of its own: the run
	// may be failing because its context is gone. It reports a failure to.
	abandon := func() error {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), topicsCleanupTimeout)
		defer cancel()
		err := writer.abandon(cleanupCtx)
		// The indexer here may have read the written topics while they were
		// active; it reads them again rather than file under retired ones.
		if s.indexer != nil {
			s.indexer.InvalidateTopics()
		}
		return err
	}
	// unwritten fails the run after the writer has written, taking back what
	// it wrote first; err is what failed, and stays the run's error.
	unwritten := func(err error) (string, error) {
		if abandonErr := abandon(); abandonErr != nil {
			meta.LastError = fmt.Sprintf("%v; retire written topics: %v", err, abandonErr)
			return snapshot(), fmt.Errorf("%w; retire written topics: %v", err, abandonErr)
		}
		return snapshot(), err
	}
	stopped := func(stage string) (string, error) {
		meta.Message = "Stopped while " + stage + "."
		if err := abandon(); err != nil {
			meta.LastError = fmt.Sprintf("retire written topics: %v", err)
		}
		_ = progress(snapshot())
		return snapshot(), ctx.Err()
	}
	stage := "clustering"
	if err := checkpoint("Reading the requests in the window."); err != nil {
		return snapshot(), err
	}
	err = s.readTopicWindow(ctx, config, limits, meta.StartTime, meta.EndTime, func(entry topicVector) error {
		before := run.batches
		if err := run.add(ctx, entry); err != nil {
			return err
		}
		if run.merged {
			if err := writer.file(ctx); err != nil {
				return err
			}
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
	if err == nil && run.read() > 0 {
		err = writer.file(ctx)
		if err == nil {
			err = writer.finish(ctx)
		}
	}
	meta.Scanned, meta.Total = run.read(), int64(run.read())
	if err != nil {
		if ctx.Err() != nil {
			return stopped(stage)
		}
		meta.LastError = err.Error()
		if writer.topics != nil {
			meta.Message = "Could not write the new topics; the previous topics were kept."
			meta.Indexed, meta.Topics = 0, nil
			err = fmt.Errorf("write topics: %w", err)
		}
		if abandonErr := abandon(); abandonErr != nil {
			meta.LastError += fmt.Sprintf("; retire written topics: %v", abandonErr)
			return snapshot(), fmt.Errorf("%w; retire written topics: %v", err, abandonErr)
		}
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
		return unwritten(err)
	}

	// 4. Label. After the merge, so a subject that was split is named once.
	labelCtx := withBackgroundWork(ctx)
	failed, lastError := labelTopics(labelCtx, s.chatFuncFor(labelCtx, config, ""), config, clusters, s.topicSampleTexts(ctx, clusters))
	if ctx.Err() != nil {
		return stopped("labelling")
	}
	if failed > 0 {
		// A model that cannot be reached must not sink the whole run: the
		// clusters are still real, so they get a fallback name and the
		// failure is counted where the operator can see it.
		meta.Failed += failed
		meta.LastError = lastError
	}
	// Topics that came out with one name are one topic. Their requests are
	// already filed under each, so the ones folded in are kept as merged into
	// the one they joined, which is how every read counts them.
	clusters, meta.MergedByName = joinTopicsByName(clusters)

	// 5. Make the topics active, then retire the previous topics. In that
	// order, so there is no moment with no topics. The topics were written as
	// seeding, so what a failed run could not retire is never counted as a
	// finished seed.
	if err := checkpoint(fmt.Sprintf("Activating %d topic(s).", len(clusters))); err != nil {
		return unwritten(err)
	}
	// What this run replaces is what TopicReader serves: active topics under
	// this embedding model. Themes and other models' topics are not its to
	// retire. Centroids are read, because retiring rewrites the whole row.
	previous, err := topicStore.ListWarpTopics(ctx, logstore.WarpTopicFilter{
		Kinds: []string{logstore.WarpTopicKindTopic}, Statuses: []string{logstore.WarpTopicStatusActive},
		EmbeddingSignature: meta.ConfigSignature,
	})
	if err == nil {
		err = writer.activate(ctx, clusters)
	}
	if err != nil {
		meta.LastError = err.Error()
		meta.Message = "Could not write the new topics; the previous topics were kept."
		meta.Indexed, meta.Topics = 0, nil
		if retireErr := abandon(); retireErr != nil {
			// The topics it wrote may still be active, so the run says so.
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
	// Requests indexed from now on are filed under the new topics at once in
	// this process; other instances pick them up when their copy expires.
	if s.indexer != nil {
		s.indexer.InvalidateTopics()
	}
	// The themes are rebuilt over the new topics. A model that cannot group
	// them is reported, not a failed run: the topics are in place.
	// GroupTopics reports what it wrote even when the model failed, so the
	// count is kept either way.
	//
	// The new topics are active from here, so a cancel can no longer leave
	// the store as it was, and stopping before grouping would leave them
	// ungrouped with nothing to group them: a second seed run is refused, and
	// upkeep and discovery regroup only when they change a topic. So grouping
	// finishes on a context the cancel does not reach, bounded, and the run
	// reports the cancel after.
	groupCtx, cancelGroup := context.WithTimeout(context.WithoutCancel(ctx), topicsGroupTimeout)
	themes, err := s.GroupTopics(groupCtx)
	cancelGroup()
	meta.Themes = themes.Themes
	if err != nil {
		meta.LastError = fmt.Sprintf("group topics into themes: %v", err)
	}
	if ctx.Err() != nil {
		meta.Message = fmt.Sprintf("Stopped after writing %d topic(s), which were grouped into themes first.", meta.Indexed)
		if err != nil {
			meta.Message = fmt.Sprintf("Stopped after writing %d topic(s); grouping them into themes failed.", meta.Indexed)
		}
		_ = progress(snapshot())
		return snapshot(), ctx.Err()
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

// topicWriter writes a run's topics and files every request it read under
// one, a batch at a time as the run places them, so it holds one batch and
// not the window.
//
// Each request's row copies the columns the log store answers topic
// questions from, read back from the log rather than from the vector's
// metadata: the vector carries a lossy copy of them, and the log is what the
// access scope on a restricted caller's reads is written against. A request
// whose log has gone since it was indexed has no row to copy and is counted
// as skipped.
//
// A row a request already had is replaced and not kept to put back. A seed
// is admitted only while the model has no active topic, so every row it can
// replace is under a retired topic, another embedding model's, or none, and a
// row under a retired or another model's topic is counted by nothing. A run
// that fails retires the topics it wrote, which takes the rows it filed under
// them out of every count with them, and deletes the rows it filed under no
// topic, which would otherwise count as unmatched once the model has topics.
// Those are found in the store, as every row with no topic assigned since the
// run started: nothing else files a request under no topic while a seed runs,
// since live filing waits for active topics and discovery waits for the run.
// So the writer keeps no list of them, and nothing it holds grows with the
// window.
type topicWriter struct {
	store     logstore.WarpTopicStore
	meta      *BackfillJobMeta
	run       *topicRun
	batchSize int
	now       time.Time
	topics    []logstore.WarpTopic // by the run's cluster index; nil until written
	pending   []requestPlacement
	filed     int
	found     int
	pooled    []logstore.WarpTopicAssignment // rows of the unmatched requests the pool kept
}

func newTopicWriter(store logstore.WarpTopicStore, meta *BackfillJobMeta, run *topicRun, batchSize int) *topicWriter {
	if batchSize <= 0 {
		batchSize = topicsWriteBatch
	}
	// To the millisecond, the finest time every store keeps, so a row's
	// assigned_at compares exactly with the run's start when abandon looks
	// for the rows it filed under no topic.
	return &topicWriter{store: store, meta: meta, run: run, batchSize: batchSize, now: time.Now().UTC().Truncate(time.Millisecond)}
}

// file writes the topics the first time it is called, once the merge has
// settled them, and then files every request placed since the last call,
// keeping back less than a batch.
func (w *topicWriter) file(ctx context.Context) error {
	if w.topics == nil {
		if err := w.writeTopics(ctx); err != nil {
			return err
		}
		// The clustered requests are placed now that their topics have ids,
		// and the run lets go of them.
		for index := range w.run.clusters {
			for _, member := range w.run.clusters[index].members {
				w.pending = append(w.pending, requestPlacement{logID: member.logID, cluster: index, similarity: member.similarity})
			}
			w.run.clusters[index].members = nil
		}
	}
	w.pending = append(w.pending, w.run.takePlacements()...)
	for len(w.pending) >= w.batchSize {
		if err := w.flush(ctx, w.pending[:w.batchSize]); err != nil {
			return err
		}
		w.pending = append(w.pending[:0], w.pending[w.batchSize:]...)
	}
	return nil
}

// writeTopics writes a seeding row for each of the run's topics. Names,
// examples and the final centres come with activate.
func (w *topicWriter) writeTopics(ctx context.Context) error {
	topics := make([]logstore.WarpTopic, len(w.run.clusters))
	for index := range w.run.clusters {
		cluster := &w.run.clusters[index]
		topics[index] = logstore.WarpTopic{
			ID: uuid.NewString(), Kind: logstore.WarpTopicKindTopic,
			Centroid: logstore.EncodeWarpVector(cluster.centroid()), AssignFloor: float64(w.run.floor), Cohesion: cluster.cohesion(),
			EmbeddingSignature: w.meta.ConfigSignature, Status: logstore.WarpTopicStatusSeeding, CreatedAt: w.now,
		}
		cluster.id = topics[index].ID
	}
	if len(topics) > 0 {
		if err := w.store.UpsertWarpTopics(ctx, topics); err != nil {
			return err
		}
	}
	w.topics = topics
	return nil
}

// flush files one batch of placements.
func (w *topicWriter) flush(ctx context.Context, batch []requestPlacement) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ids := make([]string, len(batch))
	placed := make(map[string]requestPlacement, len(batch))
	for index, placement := range batch {
		ids[index] = placement.logID
		placed[placement.logID] = placement
	}
	rows, err := w.store.ListLogsForWarpTopics(ctx, ids, w.meta.StartTime, w.meta.EndTime)
	if err != nil {
		return fmt.Errorf("read requests: %w", err)
	}
	w.filed += len(batch)
	w.found += len(rows)
	inTopics := make([]string, 0, len(rows))
	for index := range rows {
		row := &rows[index]
		row.AssignedAt = w.now
		placement := placed[row.LogID]
		if placement.cluster >= 0 {
			row.TopicID, row.Similarity = w.topics[placement.cluster].ID, float64(placement.similarity)
			inTopics = append(inTopics, row.LogID)
			continue
		}
		if _, ok := w.run.pooledIDs[row.LogID]; ok {
			w.pooled = append(w.pooled, *row)
		}
	}
	// Every request in the batch is gone from the log store: nothing to write.
	if len(rows) == 0 {
		return nil
	}
	if err := w.store.UpsertWarpTopicAssignments(ctx, rows); err != nil {
		return fmt.Errorf("write assignments: %w", err)
	}
	// A request that is in a topic now is no longer waiting for one.
	if len(inTopics) > 0 {
		if err := w.store.DeleteWarpTopicUnmatched(ctx, inTopics); err != nil {
			return fmt.Errorf("clear pooled requests: %w", err)
		}
	}
	w.meta.Message = fmt.Sprintf("Wrote %d topic(s); filed %d request(s).", len(w.topics), w.filed)
	return nil
}

// finish files what is left and pools the unmatched requests the run kept
// vectors of, for the next discovery.
func (w *topicWriter) finish(ctx context.Context) error {
	if len(w.pending) > 0 {
		if err := w.flush(ctx, w.pending); err != nil {
			return err
		}
		w.pending = nil
	}
	vectors := make(map[string][]float32, len(w.run.pooled))
	for _, entry := range w.run.pooled {
		vectors[entry.logID] = entry.vector
	}
	for start := 0; start < len(w.pooled); start += w.batchSize {
		batch := w.pooled[start:min(start+w.batchSize, len(w.pooled))]
		rows := make([]logstore.WarpTopicUnmatched, 0, len(batch))
		for _, row := range batch {
			rows = append(rows, logstore.WarpTopicUnmatched{
				LogID: row.LogID, Timestamp: row.Timestamp, LogCreatedAt: row.LogCreatedAt,
				Vector: logstore.EncodeWarpVector(vectors[row.LogID]), EmbeddingSignature: w.meta.ConfigSignature,
			})
		}
		if err := w.store.AddWarpTopicUnmatched(ctx, rows); err != nil {
			return fmt.Errorf("pool unmatched requests: %w", err)
		}
	}
	w.meta.Skipped = w.filed - w.found
	return nil
}

// activate writes the topics as the run ends with them - named, centred and
// measured over everything filed under them - and makes them active. A topic
// joined into another by name is then written as merged into it, after the
// one it joined is active: upkeep retires a merged topic whose survivor is
// not.
func (w *topicWriter) activate(ctx context.Context, clusters []topicCluster) error {
	byID := make(map[string]logstore.WarpTopic, len(w.topics))
	for _, topic := range w.topics {
		byID[topic.ID] = topic
	}
	active := make([]logstore.WarpTopic, 0, len(clusters))
	summaries := make([]TopicSummary, 0, len(clusters))
	var merged []logstore.WarpTopic
	for _, cluster := range clusters {
		topic := byID[cluster.id]
		topic.Label, topic.Centroid, topic.Cohesion = cluster.label, logstore.EncodeWarpVector(cluster.centroid()), cluster.cohesion()
		topic.Status = logstore.WarpTopicStatusActive
		if err := topic.SetExampleLogIDs(cluster.exampleIDs()); err != nil {
			return fmt.Errorf("encode topic examples: %w", err)
		}
		active = append(active, topic)
		summaries = append(summaries, TopicSummary{Label: cluster.label, Size: cluster.size, Cohesion: roundTo(cluster.cohesion(), 3)})
		for _, id := range cluster.absorbed {
			folded := byID[id]
			folded.Label, folded.Status, folded.MergedInto = cluster.label, logstore.WarpTopicStatusMerged, topic.ID
			merged = append(merged, folded)
		}
	}
	if len(active) > 0 {
		if err := w.store.UpsertWarpTopics(ctx, active); err != nil {
			return err
		}
	}
	if len(merged) > 0 {
		if err := w.store.UpsertWarpTopics(ctx, merged); err != nil {
			return fmt.Errorf("write topics joined by name: %w", err)
		}
	}
	w.meta.Indexed, w.meta.Topics = len(active), summaries
	return nil
}

// abandon retires the topics a failed run wrote and deletes the rows it filed
// under no topic. The rows under its topics stay, and stop counting with them.
func (w *topicWriter) abandon(ctx context.Context) error {
	if w.topics == nil {
		return nil // nothing was written
	}
	if len(w.topics) > 0 {
		if err := retireTopics(ctx, w.store, w.topics); err != nil {
			return err
		}
	}
	return w.store.DeleteWarpTopicAssignmentsUnmatchedSince(ctx, w.now)
}

// retireTopics marks topics as taking nothing new. Their rows stay, so the
// assignments written against them still resolve.
func retireTopics(ctx context.Context, store logstore.WarpTopicStore, topics []logstore.WarpTopic) error {
	return setTopicsStatus(ctx, store, topics, logstore.WarpTopicStatusRetired)
}

// setTopicsStatus rewrites topics' rows with status.
func setTopicsStatus(ctx context.Context, store logstore.WarpTopicStore, topics []logstore.WarpTopic, status string) error {
	if len(topics) == 0 {
		return nil
	}
	for index := range topics {
		topics[index].Status = status
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
			samples := labelTexts(texts, cluster.sampleIDs, cluster.drawn)
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

// labelTexts is what a topic is named from: the texts of the given ids, in
// order, each text once. A prompt sent to several providers is several
// requests with one text, and showing it to the model more than once would
// weigh it as more than one request's worth of the topic.
func labelTexts(texts map[string]string, groups ...[]string) []string {
	var samples []string
	seen := map[string]struct{}{}
	for _, ids := range groups {
		for _, id := range ids {
			text := texts[id]
			if _, repeat := seen[text]; text == "" || repeat {
				continue
			}
			seen[text] = struct{}{}
			samples = append(samples, text)
		}
	}
	return samples
}

// topicSampleTexts hydrates every cluster's sample and drawn logs, a batch at
// a time, and returns what the users asked, by log id.
func (s *Service) topicSampleTexts(ctx context.Context, clusters []topicCluster) map[string]string {
	ids := make([]string, 0, len(clusters)*(topicsLabelSamples+topicsLabelDrawn))
	seen := make(map[string]struct{}, cap(ids))
	for _, cluster := range clusters {
		// A member can be both a sample and drawn; it is read once.
		for _, id := range slices.Concat(cluster.sampleIDs, cluster.drawn) {
			if _, repeat := seen[id]; !repeat {
				seen[id] = struct{}{}
				ids = append(ids, id)
			}
		}
	}
	return s.logTexts(ctx, ids)
}

// logTexts hydrates logs, a batch at a time, and returns what the users
// asked, by log id. Content-hidden rows are left out: a label must never be
// derived from text the operator asked not to keep.
func (s *Service) logTexts(ctx context.Context, ids []string) map[string]string {
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
	"with no punctuation, no quotes and no explanation. Name the subject, not the wording. " +
	"Name what most of the messages share; a subject only one message mentions is not the topic."

// labelTopic asks the Warp model for a name, and falls back to one made from
// the texts' own words when there is no model or its answer holds no name.
func labelTopic(ctx context.Context, chat ChatFunc, config *schemas.WarpConfig, texts []string) (string, error) {
	if chat == nil || len(texts) == 0 {
		return fallbackTopicLabel(texts), nil
	}
	label, err := askTopicLabel(ctx, chat, config, texts, nil)
	if err != nil {
		return "", err
	}
	if label == "" {
		return fallbackTopicLabel(texts), nil
	}
	return label, nil
}

// askTopicLabel asks the Warp model for a name and returns it cleaned, or ""
// when the answer holds no name. formerNames are the names of topics that
// were merged to make this one, for the model to name what they share; nil
// for a topic named the first time.
func askTopicLabel(ctx context.Context, chat ChatFunc, config *schemas.WarpConfig, texts, formerNames []string) (string, error) {
	var prompt strings.Builder
	if len(formerNames) > 0 {
		prompt.WriteString("These requests were filed under topics since merged into one, named: ")
		prompt.WriteString(strings.Join(formerNames, "; "))
		prompt.WriteString(". Name what they have in common, not any one of them.\n\n")
	}
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
	return cleanTopicLabel(responsesText(response.Output)), nil
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
