package logstore

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Warp's topics live here, beside the logs they describe, rather than in the
// vector store where the first version kept them.
//
// A topic list that only says "these are the subjects" can be a snapshot of
// centroids. One that has to answer "what was asked this week", "by which
// team", "what did that topic cost" needs a row per request that can be
// counted, filtered by time and owner, and joined to nothing. That is a table,
// and it is this one: warp_topic_assignments holds one narrow row per request,
// carrying the topic it matched and the handful of log columns every such
// question groups or filters on. The row is copied from the log instead of
// joined to it because ClickHouse cannot afford the join, and because the
// access scope that restricts log reads names these same columns, so it applies
// to this table unchanged.
//
// warp_topics holds the topics themselves. warp_topic_unmatched is the pool of
// requests that matched no topic, kept with their vectors so the next discovery
// run can cluster them into new topics without reading the vector store back.

const (
	// WarpTopicKindTopic is a subject requests are assigned to directly.
	WarpTopicKindTopic = "topic"
	// WarpTopicKindTheme groups related topics. Nothing is assigned to a theme;
	// a topic names its theme in ParentID.
	WarpTopicKindTheme = "theme"

	// WarpTopicStatusActive topics take new assignments.
	WarpTopicStatusActive = "active"
	// WarpTopicStatusMerged topics were folded into MergedInto. The row stays
	// so assignments written before the merge still resolve.
	WarpTopicStatusMerged = "merged"
	// WarpTopicStatusRetired topics take nothing new: their traffic stopped, or
	// the embedding model they were built with was replaced.
	WarpTopicStatusRetired = "retired"
)

// warpTopicWriteBatch bounds one multi-row statement. The widest row here has
// 19 columns, so a batch stays far below the Postgres (65,535) and SQLite
// (32,766) bound-parameter limits.
const warpTopicWriteBatch = 500

// WarpTopic is one topic or theme.
//
// Centroid is the unit direction requests are compared against, encoded by
// EncodeWarpVector. AssignFloor is the similarity below which a request does
// not belong to the topic, measured when the topic was built. Both are only
// meaningful for vectors from the model named by EmbeddingSignature, which is
// why the signature is stored on the row: a changed embedding model must not
// compare its vectors against centroids from the old one.
type WarpTopic struct {
	ID                 string    `gorm:"type:varchar(36);primaryKey" json:"id"`
	Kind               string    `gorm:"type:varchar(16);not null" json:"kind"`
	ParentID           string    `gorm:"type:varchar(36);not null" json:"parent_id,omitempty"` // theme this topic belongs to
	Label              string    `gorm:"type:varchar(255);not null" json:"label"`
	Centroid           []byte    `json:"-"`
	AssignFloor        float64   `gorm:"not null" json:"assign_floor"`
	Cohesion           float64   `gorm:"not null" json:"cohesion"`
	ExampleLogIDsJSON  string    `gorm:"column:example_log_ids;type:text" json:"-"` // JSON array; the requests the label was written from
	EmbeddingSignature string    `gorm:"type:varchar(255);not null" json:"embedding_signature"`
	Status             string    `gorm:"type:varchar(16);not null;index:idx_warp_topics_status" json:"status"`
	MergedInto         string    `gorm:"type:varchar(36);not null" json:"merged_into,omitempty"`
	CreatedAt          time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt          time.Time `gorm:"not null" json:"updated_at"`
}

// TableName sets the table name for the Warp topic model.
func (WarpTopic) TableName() string { return "warp_topics" }

// ExampleLogIDs returns the requests the topic's label was written from.
func (t *WarpTopic) ExampleLogIDs() []string {
	if t.ExampleLogIDsJSON == "" {
		return nil
	}
	var ids []string
	if err := sonic.UnmarshalString(t.ExampleLogIDsJSON, &ids); err != nil {
		return nil
	}
	return ids
}

// SetExampleLogIDs records the requests the topic's label was written from.
func (t *WarpTopic) SetExampleLogIDs(ids []string) error {
	if len(ids) == 0 {
		t.ExampleLogIDsJSON = ""
		return nil
	}
	encoded, err := sonic.MarshalString(ids)
	if err != nil {
		return err
	}
	t.ExampleLogIDsJSON = encoded
	return nil
}

// WarpTopicAssignment is one request's place among the topics.
//
// TopicID is empty for a request that matched nothing: it is still a row, so
// "how much traffic has no topic" is a count like any other. Everything below
// Similarity is copied from the log when the row is written. The five
// ownership columns keep the log's names and meanings exactly, because the
// access scope on a restricted caller's reads is a predicate over those names.
//
// LogCreatedAt is the log's created_at, not the time this row was written. It
// is what retention keys on, so an assignment expires with the log it points
// at rather than some days after it.
type WarpTopicAssignment struct {
	LogID          string    `gorm:"type:varchar(255);primaryKey" json:"log_id"`
	Timestamp      time.Time `gorm:"not null;index:idx_warp_topic_assignments_ts;index:idx_warp_topic_assignments_topic_ts,priority:2" json:"timestamp"`
	TopicID        string    `gorm:"type:varchar(36);not null;index:idx_warp_topic_assignments_topic_ts,priority:1" json:"topic_id"`
	Similarity     float64   `gorm:"not null" json:"similarity"`
	SessionID      string    `gorm:"type:varchar(255);not null" json:"session_id,omitempty"`
	UserID         string    `gorm:"type:varchar(255);not null" json:"user_id,omitempty"`
	TeamID         string    `gorm:"type:varchar(255);not null" json:"team_id,omitempty"`
	VirtualKeyID   string    `gorm:"type:varchar(255);not null" json:"virtual_key_id,omitempty"`
	CustomerID     string    `gorm:"type:varchar(255);not null" json:"customer_id,omitempty"`
	BusinessUnitID string    `gorm:"type:varchar(255);not null" json:"business_unit_id,omitempty"`
	Provider       string    `gorm:"type:varchar(255);not null" json:"provider,omitempty"`
	Model          string    `gorm:"type:varchar(255);not null" json:"model,omitempty"`
	App            string    `gorm:"type:varchar(128);not null" json:"app,omitempty"`
	Status         string    `gorm:"type:varchar(50);not null" json:"status,omitempty"`
	Cost           float64   `gorm:"not null" json:"cost"`
	Latency        float64   `gorm:"not null" json:"latency"` // ms; 0 = unmeasured
	TotalTokens    int       `gorm:"not null" json:"total_tokens"`
	LogCreatedAt   time.Time `gorm:"column:created_at;not null;index:idx_warp_topic_assignments_created_at" json:"-"`
	AssignedAt     time.Time `gorm:"not null" json:"assigned_at"`
}

// TableName sets the table name for the Warp topic assignment model.
func (WarpTopicAssignment) TableName() string { return "warp_topic_assignments" }

// WarpTopicUnmatched is one request waiting for a topic that does not exist
// yet. Vector is its embedding, encoded by EncodeWarpVector.
type WarpTopicUnmatched struct {
	LogID              string    `gorm:"type:varchar(255);primaryKey" json:"log_id"`
	Timestamp          time.Time `gorm:"not null;index:idx_warp_topic_unmatched_ts" json:"timestamp"`
	Vector             []byte    `json:"-"`
	EmbeddingSignature string    `gorm:"type:varchar(255);not null" json:"embedding_signature"`
	LogCreatedAt       time.Time `gorm:"column:created_at;not null;index:idx_warp_topic_unmatched_created_at" json:"-"`
}

// TableName sets the table name for the Warp unmatched-request model.
func (WarpTopicUnmatched) TableName() string { return "warp_topic_unmatched" }

// WarpTopicFilter selects topics. A zero filter selects all of them.
type WarpTopicFilter struct {
	IDs                []string
	Kinds              []string
	Statuses           []string
	EmbeddingSignature string
	// OmitCentroids leaves Centroid unread, for callers that only list topics.
	OmitCentroids bool
}

// WarpTopicAssignmentFilter selects assignments. Each populated list narrows
// the selection; a zero filter selects every row the caller may see.
type WarpTopicAssignmentFilter struct {
	StartTime *time.Time
	EndTime   *time.Time
	// TopicIDs may include "" to select requests that matched no topic.
	TopicIDs        []string
	LogIDs          []string
	SessionIDs      []string
	UserIDs         []string
	TeamIDs         []string
	VirtualKeyIDs   []string
	CustomerIDs     []string
	BusinessUnitIDs []string
	Providers       []string
	Models          []string
	Apps            []string
	Statuses        []string
}

// WarpTopicUsage is what one topic's requests add up to over a selection.
type WarpTopicUsage struct {
	TopicID    string  `gorm:"column:topic_id" json:"topic_id"`
	Requests   int64   `gorm:"column:requests" json:"requests"`
	Sessions   int64   `gorm:"column:sessions" json:"sessions"` // distinct sessions; requests without one are not counted
	Errors     int64   `gorm:"column:errors" json:"errors"`
	Cost       float64 `gorm:"column:total_cost" json:"cost"`
	Tokens     int64   `gorm:"column:tokens" json:"tokens"`
	AvgLatency float64 `gorm:"column:avg_latency" json:"avg_latency"` // ms, over requests that measured one
}

// WarpTopicStore is the topic surface. It is embedded in LogStore for the
// reason WarpConversationStore is: a store that forgot a method should fail
// the build, not start and serve no topics.
type WarpTopicStore interface {
	// UpsertWarpTopics writes topics whole, replacing any row with the same ID.
	// A caller changing one field reads the topic, changes it and writes it
	// back; there is no partial update, because ClickHouse has none.
	UpsertWarpTopics(ctx context.Context, topics []WarpTopic) error
	// ListWarpTopics returns the selected topics, oldest first.
	//
	// It is not narrowed by the caller's access scope: a topic has no owner.
	// Whether a restricted caller may see a topic's label is decided by the
	// caller of this method from the assignments that caller can see.
	ListWarpTopics(ctx context.Context, filter WarpTopicFilter) ([]WarpTopic, error)

	// UpsertWarpTopicAssignments writes one row per request, replacing any row
	// already written for the same request.
	UpsertWarpTopicAssignments(ctx context.Context, assignments []WarpTopicAssignment) error
	// SummarizeWarpTopicAssignments adds up the selected assignments per topic,
	// largest first, within the caller's access scope. Requests that matched
	// no topic are reported under the empty topic ID.
	SummarizeWarpTopicAssignments(ctx context.Context, filter WarpTopicAssignmentFilter) ([]WarpTopicUsage, error)
	// ListWarpTopicAssignments returns the selected assignments newest first,
	// within the caller's access scope.
	ListWarpTopicAssignments(ctx context.Context, filter WarpTopicAssignmentFilter, limit int) ([]WarpTopicAssignment, error)
	// DeleteWarpTopicAssignmentsBatch deletes up to batchSize assignments whose
	// log was created before cutoff, for the retention cleaner.
	DeleteWarpTopicAssignmentsBatch(ctx context.Context, cutoff time.Time, batchSize int) (deletedCount int64, err error)

	// AddWarpTopicUnmatched puts requests in the unmatched pool, replacing any
	// row already there for the same request.
	AddWarpTopicUnmatched(ctx context.Context, rows []WarpTopicUnmatched) error
	// ListWarpTopicUnmatched returns the pool for one embedding model, newest
	// first.
	ListWarpTopicUnmatched(ctx context.Context, embeddingSignature string, limit int) ([]WarpTopicUnmatched, error)
	// CountWarpTopicUnmatched reports the pool's size for one embedding model
	// without reading its vectors.
	CountWarpTopicUnmatched(ctx context.Context, embeddingSignature string) (int64, error)
	// DeleteWarpTopicUnmatched removes requests from the pool, once they have
	// a topic.
	DeleteWarpTopicUnmatched(ctx context.Context, logIDs []string) error
	// PruneWarpTopicUnmatched bounds the pool: it drops requests older than
	// olderThan, then the oldest beyond the newest keep. A zero olderThan or a
	// keep below one skips that bound.
	PruneWarpTopicUnmatched(ctx context.Context, olderThan time.Time, keep int) (deletedCount int64, err error)
	// DeleteWarpTopicUnmatchedBatch deletes up to batchSize pool rows whose log
	// was created before cutoff, for the retention cleaner.
	DeleteWarpTopicUnmatchedBatch(ctx context.Context, cutoff time.Time, batchSize int) (deletedCount int64, err error)

	// ListLogsForWarpTopics returns the assignment columns of the given logs:
	// everything an assignment copies from its log, with TopicID, Similarity
	// and AssignedAt left for the caller to fill. from and to bound the logs'
	// timestamps when non-zero, which lets a time-ordered store skip the rest.
	//
	// It is not narrowed by any access scope. It exists for the background job
	// that assigns every request, and must not be reachable from a request
	// made on a user's behalf.
	ListLogsForWarpTopics(ctx context.Context, logIDs []string, from, to time.Time) ([]WarpTopicAssignment, error)
}

// EncodeWarpVector packs a vector as little-endian float32s, four bytes a
// dimension. Stored this way instead of as JSON because a 1536-dimension
// vector is 6 KB packed and about three times that as text, and the unmatched
// pool holds thousands.
func EncodeWarpVector(vector []float32) []byte {
	if len(vector) == 0 {
		return nil
	}
	encoded := make([]byte, 4*len(vector))
	for i, value := range vector {
		binary.LittleEndian.PutUint32(encoded[4*i:], math.Float32bits(value))
	}
	return encoded
}

// DecodeWarpVector reverses EncodeWarpVector.
func DecodeWarpVector(encoded []byte) ([]float32, error) {
	if len(encoded)%4 != 0 {
		return nil, fmt.Errorf("encoded vector is %d bytes, not a multiple of 4", len(encoded))
	}
	if len(encoded) == 0 {
		return nil, nil
	}
	vector := make([]float32, len(encoded)/4)
	for i := range vector {
		vector[i] = math.Float32frombits(binary.LittleEndian.Uint32(encoded[4*i:]))
	}
	return vector, nil
}

// warpTopicTimeArg returns the placeholder and argument that compare a
// timestamp column against t.
//
// ClickHouse needs its own form: the driver formats a time.Time argument as
// toDateTime('...'), at whole seconds, so an exact comparison against a
// DateTime64(3) column would be off by the sub-second part. Binding epoch
// milliseconds keeps the precision the column stores. Elsewhere the time is
// bound in UTC, the zone every row is written in, because SQLite compares
// timestamps as text.
func warpTopicTimeArg(db *gorm.DB, t time.Time) (string, any) {
	if db.Dialector.Name() == "clickhouse" {
		return "fromUnixTimestamp64Milli(?)", t.UnixMilli()
	}
	return "?", t.UTC()
}

// prepareWarpTopics validates topics for a write and stamps their times.
func prepareWarpTopics(topics []WarpTopic, now time.Time) error {
	for i := range topics {
		topic := &topics[i]
		if topic.ID == "" {
			return fmt.Errorf("warp topic has no id")
		}
		if topic.Kind == "" {
			topic.Kind = WarpTopicKindTopic
		}
		if topic.Status == "" {
			topic.Status = WarpTopicStatusActive
		}
		if topic.CreatedAt.IsZero() {
			topic.CreatedAt = now
		}
		topic.CreatedAt = topic.CreatedAt.UTC()
		topic.UpdatedAt = now
	}
	return nil
}

// prepareWarpTopicAssignments validates assignments for a write and puts their
// times in UTC.
func prepareWarpTopicAssignments(assignments []WarpTopicAssignment, now time.Time) error {
	for i := range assignments {
		assignment := &assignments[i]
		if assignment.LogID == "" {
			return fmt.Errorf("warp topic assignment has no log id")
		}
		if assignment.Timestamp.IsZero() {
			return fmt.Errorf("warp topic assignment for log %s has no timestamp", assignment.LogID)
		}
		assignment.Timestamp = assignment.Timestamp.UTC()
		if assignment.LogCreatedAt.IsZero() {
			assignment.LogCreatedAt = assignment.Timestamp
		}
		assignment.LogCreatedAt = assignment.LogCreatedAt.UTC()
		if assignment.AssignedAt.IsZero() {
			assignment.AssignedAt = now
		}
		assignment.AssignedAt = assignment.AssignedAt.UTC()
	}
	return nil
}

// prepareWarpTopicUnmatched validates pool rows for a write and puts their
// times in UTC.
func prepareWarpTopicUnmatched(rows []WarpTopicUnmatched) error {
	for i := range rows {
		row := &rows[i]
		if row.LogID == "" {
			return fmt.Errorf("warp unmatched request has no log id")
		}
		if len(row.Vector) == 0 {
			return fmt.Errorf("warp unmatched request for log %s has no vector", row.LogID)
		}
		if row.Timestamp.IsZero() {
			return fmt.Errorf("warp unmatched request for log %s has no timestamp", row.LogID)
		}
		row.Timestamp = row.Timestamp.UTC()
		if row.LogCreatedAt.IsZero() {
			row.LogCreatedAt = row.Timestamp
		}
		row.LogCreatedAt = row.LogCreatedAt.UTC()
	}
	return nil
}

// UpsertWarpTopics writes topics whole, replacing any row with the same ID.
func (s *RDBLogStore) UpsertWarpTopics(ctx context.Context, topics []WarpTopic) error {
	if len(topics) == 0 {
		return nil
	}
	if err := prepareWarpTopics(topics, time.Now().UTC()); err != nil {
		return err
	}
	return s.db.WithContext(ctx).
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, UpdateAll: true}).
		CreateInBatches(topics, warpTopicWriteBatch).Error
}

// ListWarpTopics returns the selected topics, oldest first.
//
// This reads through s.db, not ScopedDB: the access scope is a predicate over
// ownership columns this table does not have.
func (s *RDBLogStore) ListWarpTopics(ctx context.Context, filter WarpTopicFilter) ([]WarpTopic, error) {
	query := s.db.WithContext(ctx).Model(&WarpTopic{})
	if filter.OmitCentroids {
		query = query.Omit("centroid")
	}
	if len(filter.IDs) > 0 {
		query = query.Where("id IN ?", filter.IDs)
	}
	if len(filter.Kinds) > 0 {
		query = query.Where("kind IN ?", filter.Kinds)
	}
	if len(filter.Statuses) > 0 {
		query = query.Where("status IN ?", filter.Statuses)
	}
	if filter.EmbeddingSignature != "" {
		query = query.Where("embedding_signature = ?", filter.EmbeddingSignature)
	}
	var topics []WarpTopic
	err := query.Order("created_at ASC, id ASC").Find(&topics).Error
	return topics, err
}

// UpsertWarpTopicAssignments writes one row per request, replacing any row
// already written for the same request.
func (s *RDBLogStore) UpsertWarpTopicAssignments(ctx context.Context, assignments []WarpTopicAssignment) error {
	if len(assignments) == 0 {
		return nil
	}
	if err := prepareWarpTopicAssignments(assignments, time.Now().UTC()); err != nil {
		return err
	}
	return s.db.WithContext(ctx).
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "log_id"}}, UpdateAll: true}).
		CreateInBatches(assignments, warpTopicWriteBatch).Error
}

// warpTopicAssignments returns a query over the assignments the caller may see
// that match filter.
//
// The scope applies as written for logs. It is a predicate over user_id,
// team_id, virtual_key_id, customer_id and business_unit_id, and this table
// carries those columns with the values the log had.
func (s *RDBLogStore) warpTopicAssignments(ctx context.Context, filter WarpTopicAssignmentFilter) *gorm.DB {
	query := s.ScopedDB(ctx).Model(&WarpTopicAssignment{})
	if filter.StartTime != nil {
		placeholder, arg := warpTopicTimeArg(s.db, *filter.StartTime)
		query = query.Where("timestamp >= "+placeholder, arg)
	}
	if filter.EndTime != nil {
		placeholder, arg := warpTopicTimeArg(s.db, *filter.EndTime)
		query = query.Where("timestamp <= "+placeholder, arg)
	}
	for _, in := range []struct {
		column string
		values []string
	}{
		{"topic_id", filter.TopicIDs},
		{"log_id", filter.LogIDs},
		{"session_id", filter.SessionIDs},
		{"user_id", filter.UserIDs},
		{"team_id", filter.TeamIDs},
		{"virtual_key_id", filter.VirtualKeyIDs},
		{"customer_id", filter.CustomerIDs},
		{"business_unit_id", filter.BusinessUnitIDs},
		{"provider", filter.Providers},
		{"model", filter.Models},
		{"app", filter.Apps},
		{"status", filter.Statuses},
	} {
		if len(in.values) > 0 {
			query = query.Where(in.column+" IN ?", in.values)
		}
	}
	return query
}

// warpTopicUsageColumns is the per-topic aggregate. No alias repeats a column
// name: ClickHouse resolves a name to the alias first, and sum(cost) AS cost
// would then be an aggregate of an aggregate anywhere else it appeared.
const warpTopicUsageColumns = "topic_id, " +
	"count(*) AS requests, " +
	"count(DISTINCT NULLIF(session_id, '')) AS sessions, " +
	"COALESCE(sum(CASE WHEN status = 'error' THEN 1 ELSE 0 END), 0) AS errors, " +
	"COALESCE(sum(cost), 0) AS total_cost, " +
	"COALESCE(sum(total_tokens), 0) AS tokens, " +
	"COALESCE(avg(CASE WHEN latency > 0 THEN latency END), 0) AS avg_latency"

// SummarizeWarpTopicAssignments adds up the selected assignments per topic,
// largest first, within the caller's access scope.
func (s *RDBLogStore) SummarizeWarpTopicAssignments(ctx context.Context, filter WarpTopicAssignmentFilter) ([]WarpTopicUsage, error) {
	var usage []WarpTopicUsage
	err := s.warpTopicAssignments(ctx, filter).
		Select(warpTopicUsageColumns).
		Group("topic_id").
		Order("requests DESC, topic_id ASC").
		Scan(&usage).Error
	return usage, err
}

// ListWarpTopicAssignments returns the selected assignments newest first,
// within the caller's access scope.
func (s *RDBLogStore) ListWarpTopicAssignments(ctx context.Context, filter WarpTopicAssignmentFilter, limit int) ([]WarpTopicAssignment, error) {
	if limit <= 0 {
		limit = 100
	}
	var assignments []WarpTopicAssignment
	err := s.warpTopicAssignments(ctx, filter).
		Order("timestamp DESC, log_id DESC").
		Limit(limit).
		Find(&assignments).Error
	return assignments, err
}

// DeleteWarpTopicAssignmentsBatch deletes up to batchSize assignments whose log
// was created before cutoff, oldest first.
func (s *RDBLogStore) DeleteWarpTopicAssignmentsBatch(ctx context.Context, cutoff time.Time, batchSize int) (int64, error) {
	return s.deleteExpiredBatchByKey(ctx, "warp_topic_assignments", "log_id", "created_at", cutoff, batchSize)
}

// AddWarpTopicUnmatched puts requests in the unmatched pool.
func (s *RDBLogStore) AddWarpTopicUnmatched(ctx context.Context, rows []WarpTopicUnmatched) error {
	if len(rows) == 0 {
		return nil
	}
	if err := prepareWarpTopicUnmatched(rows); err != nil {
		return err
	}
	return s.db.WithContext(ctx).
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "log_id"}}, UpdateAll: true}).
		CreateInBatches(rows, warpTopicWriteBatch).Error
}

// ListWarpTopicUnmatched returns the pool for one embedding model, newest
// first.
func (s *RDBLogStore) ListWarpTopicUnmatched(ctx context.Context, embeddingSignature string, limit int) ([]WarpTopicUnmatched, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("limit must be positive")
	}
	var rows []WarpTopicUnmatched
	err := s.db.WithContext(ctx).
		Where("embedding_signature = ?", embeddingSignature).
		Order("timestamp DESC, log_id DESC").
		Limit(limit).
		Find(&rows).Error
	return rows, err
}

// CountWarpTopicUnmatched reports the pool's size for one embedding model.
func (s *RDBLogStore) CountWarpTopicUnmatched(ctx context.Context, embeddingSignature string) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).
		Model(&WarpTopicUnmatched{}).
		Where("embedding_signature = ?", embeddingSignature).
		Count(&count).Error
	return count, err
}

// DeleteWarpTopicUnmatched removes requests from the pool.
func (s *RDBLogStore) DeleteWarpTopicUnmatched(ctx context.Context, logIDs []string) error {
	for chunk := range slices.Chunk(logIDs, warpTopicWriteBatch) {
		if err := s.db.WithContext(ctx).Where("log_id IN ?", chunk).Delete(&WarpTopicUnmatched{}).Error; err != nil {
			return err
		}
	}
	return nil
}

// warpTopicUnmatchedPruneWhere builds the predicate selecting the pool rows
// PruneWarpTopicUnmatched removes. It returns an empty predicate when neither
// bound removes anything.
//
// The count bound is expressed as "at or before the first row past the newest
// keep" and deleted in one statement, not as a list of the surplus ids. On
// ClickHouse every delete is a mutation whose cost does not depend on how many
// rows it matches, so one statement is the only shape worth issuing; the SQL
// stores take the same predicate to keep one behaviour to test.
func warpTopicUnmatchedPruneWhere(ctx context.Context, db *gorm.DB, olderThan time.Time, keep int) (string, []any, error) {
	var clauses []string
	var args []any
	if !olderThan.IsZero() {
		placeholder, arg := warpTopicTimeArg(db, olderThan)
		clauses = append(clauses, "timestamp < "+placeholder)
		args = append(args, arg)
	}
	if keep > 0 {
		var surplus []WarpTopicUnmatched
		if err := db.WithContext(ctx).
			Select("log_id", "timestamp").
			Order("timestamp DESC, log_id DESC").
			Offset(keep).
			Limit(1).
			Find(&surplus).Error; err != nil {
			return "", nil, err
		}
		if len(surplus) == 1 {
			placeholder, arg := warpTopicTimeArg(db, surplus[0].Timestamp)
			clauses = append(clauses, fmt.Sprintf("timestamp < %s OR (timestamp = %s AND log_id <= ?)", placeholder, placeholder))
			args = append(args, arg, arg, surplus[0].LogID)
		}
	}
	return strings.Join(clauses, " OR "), args, nil
}

// PruneWarpTopicUnmatched bounds the pool by age and by count.
func (s *RDBLogStore) PruneWarpTopicUnmatched(ctx context.Context, olderThan time.Time, keep int) (int64, error) {
	where, args, err := warpTopicUnmatchedPruneWhere(ctx, s.db, olderThan, keep)
	if err != nil || where == "" {
		return 0, err
	}
	result := s.db.WithContext(ctx).Where(where, args...).Delete(&WarpTopicUnmatched{})
	return result.RowsAffected, result.Error
}

// DeleteWarpTopicUnmatchedBatch deletes up to batchSize pool rows whose log was
// created before cutoff, oldest first.
func (s *RDBLogStore) DeleteWarpTopicUnmatchedBatch(ctx context.Context, cutoff time.Time, batchSize int) (int64, error) {
	return s.deleteExpiredBatchByKey(ctx, "warp_topic_unmatched", "log_id", "created_at", cutoff, batchSize)
}

// warpTopicLogColumns projects a log onto the assignment columns. The NULLs a
// log allows become the zero values an assignment stores.
const warpTopicLogColumns = "id AS log_id, timestamp, created_at, " +
	"COALESCE(session_id, '') AS session_id, " +
	"COALESCE(user_id, '') AS user_id, " +
	"COALESCE(team_id, '') AS team_id, " +
	"COALESCE(virtual_key_id, '') AS virtual_key_id, " +
	"COALESCE(customer_id, '') AS customer_id, " +
	"COALESCE(business_unit_id, '') AS business_unit_id, " +
	"provider, model, " +
	"COALESCE(app, '') AS app, " +
	"status, " +
	"COALESCE(cost, 0) AS cost, " +
	"COALESCE(latency, 0) AS latency, " +
	"COALESCE(total_tokens, 0) AS total_tokens"

// ListLogsForWarpTopics returns the assignment columns of the given logs.
//
// This reads through s.db, not ScopedDB, on purpose: the job that calls it
// assigns every request, whoever owns it. See WarpTopicStore.
func (s *RDBLogStore) ListLogsForWarpTopics(ctx context.Context, logIDs []string, from, to time.Time) ([]WarpTopicAssignment, error) {
	rows := make([]WarpTopicAssignment, 0, len(logIDs))
	for chunk := range slices.Chunk(logIDs, warpTopicWriteBatch) {
		query := s.db.WithContext(ctx).Table("logs").Select(warpTopicLogColumns).Where("id IN ?", chunk)
		if !from.IsZero() {
			placeholder, arg := warpTopicTimeArg(s.db, from)
			query = query.Where("timestamp >= "+placeholder, arg)
		}
		if !to.IsZero() {
			placeholder, arg := warpTopicTimeArg(s.db, to)
			query = query.Where("timestamp <= "+placeholder, arg)
		}
		var page []WarpTopicAssignment
		if err := query.Scan(&page).Error; err != nil {
			return nil, err
		}
		rows = append(rows, page...)
	}
	return rows, nil
}
