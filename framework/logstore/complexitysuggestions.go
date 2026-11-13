package logstore

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Phrase suggestions for the complexity router live here, beside the logs
// they were drawn from, as Warp's topics do.
//
// model_routing_suggestions holds one row per traffic cluster a suggestion run
// proposed phrases for, for one model router: every row, run and decision
// belongs to the router whose traffic it came from (router_id), and records
// the router version it was computed against, so a suggestion made for an
// older phrase bank can be told apart. A row outlives the run that wrote it: the next run
// matches its clusters to existing rows by centroid, so a suggestion the user
// dismissed stays dismissed and an open one has its counts refreshed instead
// of turning up twice. Only log IDs are kept for the examples, never their
// text: a reader resolves them through the logs, so a deleted log or a
// content-hidden one shows nothing here either.
//
// model_routing_suggestion_runs records each run, and model_routing_vector_deletes
// is the retry list for captured request vectors whose delete failed when
// their log was deleted (the same role warp_vector_deletes plays for Warp).

const (
	// ComplexitySuggestionStatusOpen suggestions wait for the user.
	ComplexitySuggestionStatusOpen = "open"
	// ComplexitySuggestionStatusAccepted suggestions had their phrases staged
	// into the phrase bank. A later run that finds the same cluster leaves the
	// row as it is.
	ComplexitySuggestionStatusAccepted = "accepted"
	// ComplexitySuggestionStatusDismissed suggestions were turned down. A later
	// run that finds the same cluster leaves the row as it is, so it does not
	// come back.
	ComplexitySuggestionStatusDismissed = "dismissed"
	// ComplexitySuggestionStatusSuperseded suggestions were open when a later
	// run no longer proposed them: their traffic stopped, or the phrase bank
	// now covers it.
	ComplexitySuggestionStatusSuperseded = "superseded"

	// ComplexitySuggestionRunQueued runs were taken from the schedule or asked
	// for, and wait for a job runner (or this node) to start them.
	ComplexitySuggestionRunQueued = "queued"
	// ComplexitySuggestionRunSkipped runs were due but could not start, for
	// the reason in Error; the schedule moved on to its next run.
	ComplexitySuggestionRunSkipped = "skipped"
	// ComplexitySuggestionRunRunning runs are in progress, or died without
	// recording an outcome.
	ComplexitySuggestionRunRunning = "running"
	// ComplexitySuggestionRunSucceeded runs finished.
	ComplexitySuggestionRunSucceeded = "succeeded"
	// ComplexitySuggestionRunFailed runs stopped on an error, in Error.
	ComplexitySuggestionRunFailed = "failed"
	// ComplexitySuggestionRunCancelled runs were stopped by a user before they
	// finished; what they had written so far is kept.
	ComplexitySuggestionRunCancelled = "cancelled"

	// The stages a running run reports, in order, so a reader can show how
	// far it got.
	ComplexitySuggestionRunStageReading    = "reading"    // sampling the window's requests
	ComplexitySuggestionRunStageClustering = "clustering" // grouping them and counting the rest in
	ComplexitySuggestionRunStageSuggesting = "suggesting" // asking the models about each group
	ComplexitySuggestionRunStageSaving     = "saving"     // reconciling with the stored suggestions

	// ComplexitySuggestionUnmatchedTier keys the requests of a suggestion's
	// CurrentRouting that matched no phrase above the floor.
	ComplexitySuggestionUnmatchedTier = "none"

	// ComplexitySuggestionTriggerSchedule runs were started by the schedule.
	ComplexitySuggestionTriggerSchedule = "schedule"
	// ComplexitySuggestionTriggerManual runs were started by a user.
	ComplexitySuggestionTriggerManual = "manual"
)

// complexitySuggestionWriteBatch bounds one multi-row statement. The widest
// row here has 35 columns, far below the bound-parameter limits.
const complexitySuggestionWriteBatch = 500

// ComplexitySuggestion is one cluster's suggested phrases.
//
// Centroid is the cluster's unit direction, encoded by EncodeWarpVector, and
// is only comparable with vectors from the embedding space named by
// EmbeddingSignature (the capture namespace), which is why both are stored.
type ComplexitySuggestion struct {
	ID                 string     `gorm:"type:varchar(36);primaryKey" json:"id"`
	RouterID           string     `gorm:"type:varchar(64);not null;index:idx_model_routing_suggestions_router_status,priority:1" json:"router_id"`
	RouterVersion      string     `gorm:"type:varchar(64);not null" json:"router_version"` // the router version it was computed against
	Status             string     `gorm:"type:varchar(16);not null;index:idx_model_routing_suggestions_router_status,priority:2" json:"status"`
	Tier               string     `gorm:"type:varchar(16);not null" json:"tier"`
	PhrasesJSON        string     `gorm:"column:phrases;type:text" json:"-"` // JSON array of phrase texts
	Category           string     `gorm:"type:varchar(32);not null" json:"category"`
	Reason             string     `gorm:"type:text" json:"reason,omitempty"`
	Confidence         string     `gorm:"type:varchar(16);not null" json:"confidence,omitempty"`
	RequestCount       int64      `gorm:"not null" json:"request_count"`
	Coverage           float64    `gorm:"not null" json:"coverage"`
	NearestPhrase      string     `gorm:"type:text" json:"nearest_phrase,omitempty"`
	NearestTier        string     `gorm:"type:varchar(16);not null" json:"nearest_tier,omitempty"`
	NearestSimilarity  float64    `gorm:"not null" json:"nearest_similarity"`
	Centroid           []byte     `json:"-"`
	ExampleLogIDsJSON  string     `gorm:"column:example_log_ids;type:text" json:"-"` // JSON array; the medoid first
	CurrentRoutingJSON string     `gorm:"column:current_routing;type:text" json:"-"` // JSON object: requests per tier they route to today
	EmbeddingSignature string     `gorm:"type:varchar(255);not null" json:"embedding_signature"`
	RunID              string     `gorm:"type:varchar(36);not null" json:"run_id"` // the last run that proposed it
	LastSeenAt         time.Time  `gorm:"not null" json:"last_seen_at"`
	ResolvedAt         *time.Time `json:"resolved_at,omitempty"` // when it was accepted, dismissed or superseded
	CreatedAt          time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt          time.Time  `gorm:"not null" json:"updated_at"`
}

// TableName sets the table name for the complexity suggestion model.
func (ComplexitySuggestion) TableName() string { return "model_routing_suggestions" }

// Phrases returns the suggested phrase texts.
func (s *ComplexitySuggestion) Phrases() []string { return decodeStringList(s.PhrasesJSON) }

// SetPhrases records the suggested phrase texts.
func (s *ComplexitySuggestion) SetPhrases(phrases []string) error {
	encoded, err := encodeStringList(phrases)
	if err != nil {
		return err
	}
	s.PhrasesJSON = encoded
	return nil
}

// ExampleLogIDs returns the requests the suggestion was written from.
func (s *ComplexitySuggestion) ExampleLogIDs() []string {
	return decodeStringList(s.ExampleLogIDsJSON)
}

// SetExampleLogIDs records the requests the suggestion was written from.
func (s *ComplexitySuggestion) SetExampleLogIDs(ids []string) error {
	encoded, err := encodeStringList(ids)
	if err != nil {
		return err
	}
	s.ExampleLogIDsJSON = encoded
	return nil
}

// CurrentRouting returns how many of the cluster's requests route to each
// tier today, ComplexitySuggestionUnmatchedTier counting those that match no
// phrase. Nil for a row written before it was recorded.
func (s *ComplexitySuggestion) CurrentRouting() map[string]int64 {
	if s.CurrentRoutingJSON == "" {
		return nil
	}
	var routing map[string]int64
	if err := sonic.UnmarshalString(s.CurrentRoutingJSON, &routing); err != nil {
		return nil
	}
	return routing
}

// SetCurrentRouting records how the cluster's requests route today.
func (s *ComplexitySuggestion) SetCurrentRouting(routing map[string]int64) error {
	if len(routing) == 0 {
		s.CurrentRoutingJSON = ""
		return nil
	}
	encoded, err := sonic.ConfigStd.MarshalToString(routing) // sorted keys, so a rewrite of the same split is the same bytes
	if err != nil {
		return err
	}
	s.CurrentRoutingJSON = encoded
	return nil
}

func decodeStringList(encoded string) []string {
	if encoded == "" {
		return nil
	}
	var values []string
	if err := sonic.UnmarshalString(encoded, &values); err != nil {
		return nil
	}
	return values
}

func encodeStringList(values []string) (string, error) {
	if len(values) == 0 {
		return "", nil
	}
	return sonic.MarshalString(values)
}

// ComplexitySuggestionRun is one suggestion run.
type ComplexitySuggestionRun struct {
	ID                 string     `gorm:"type:varchar(36);primaryKey" json:"id"`
	RouterID           string     `gorm:"type:varchar(64);not null;index:idx_model_routing_suggestion_runs_router_started_at,priority:1" json:"router_id"`
	RouterVersion      string     `gorm:"type:varchar(64);not null" json:"router_version"`
	TriggeredBy        string     `gorm:"type:varchar(16);not null" json:"triggered_by"`
	Status             string     `gorm:"type:varchar(16);not null" json:"status"`
	EmbeddingSignature string     `gorm:"type:varchar(255);not null" json:"embedding_signature"`
	Model              string     `gorm:"type:varchar(255);not null" json:"model,omitempty"`           // provider/model the suggestions were written with
	PromptHash         string     `gorm:"type:varchar(64);not null" json:"prompt_hash,omitempty"`      // of the instructions and guidance in force
	Stage              string     `gorm:"type:varchar(16);not null;default:''" json:"stage,omitempty"` // how far a running run got; empty once it ends
	RequestsRead       int64      `gorm:"not null" json:"requests_read"`
	RequestsClustered  int64      `gorm:"not null;default:0" json:"requests_clustered"` // the sample the groups were formed from
	RequestsAssigned   int64      `gorm:"not null;default:0" json:"requests_assigned"`  // counted into the groups after the sample
	RequestsSkipped    int64      `gorm:"not null;default:0" json:"requests_skipped"`   // past the counting bound, not read into any group
	RequestsGrouped    int64      `gorm:"not null;default:0" json:"requests_grouped"`   // in a group big enough to keep
	Clusters           int64      `gorm:"not null" json:"clusters"`
	ClustersDecided    int64      `gorm:"not null;default:0" json:"clusters_decided"`    // matched an accepted or dismissed suggestion, not asked again
	ClustersOverLimit  int64      `gorm:"not null;default:0" json:"clusters_over_limit"` // past the per-run bound on groups asked
	ClustersAsked      int64      `gorm:"not null;default:0" json:"clusters_asked"`
	ClustersDone       int64      `gorm:"not null;default:0" json:"clusters_done"` // asked so far, for a running run's progress
	ClustersFailed     int64      `gorm:"not null;default:0" json:"clusters_failed"`
	ClustersCovered    int64      `gorm:"not null;default:0" json:"clusters_covered"`    // the bank already routes them well
	ClustersIncoherent int64      `gorm:"not null;default:0" json:"clusters_incoherent"` // several request types, or no usable answer
	ClustersUnreadable int64      `gorm:"not null;default:0" json:"clusters_unreadable"` // no example could be read back
	Created            int64      `gorm:"not null" json:"created"`                       // suggestions new in this run
	Refreshed          int64      `gorm:"not null" json:"refreshed"`                     // open suggestions this run proposed again
	Superseded         int64      `gorm:"not null" json:"superseded"`
	Cost               float64    `gorm:"not null" json:"cost"`
	Error              string     `gorm:"type:text" json:"error,omitempty"`
	WindowStart        time.Time  `gorm:"not null" json:"window_start"` // the window read ends at StartedAt
	StartedAt          time.Time  `gorm:"not null;index:idx_model_routing_suggestion_runs_started_at;index:idx_model_routing_suggestion_runs_router_started_at,priority:2" json:"started_at"`
	FinishedAt         *time.Time `json:"finished_at,omitempty"`
}

// TableName sets the table name for the complexity suggestion run model.
func (ComplexitySuggestionRun) TableName() string { return "model_routing_suggestion_runs" }

// ComplexityVectorDelete is a deleted log whose captured request vector could
// not be removed from one capture namespace when the log was. It is written
// only when that delete fails, and removed once a retry succeeds. A row is
// its (log, namespace) pair: one log can be pending in several namespaces.
type ComplexityVectorDelete struct {
	LogID     string    `gorm:"type:varchar(255);primaryKey" json:"log_id"`
	Namespace string    `gorm:"type:varchar(255);primaryKey" json:"namespace"` // empty: retried in every capture namespace
	CreatedAt time.Time `gorm:"not null;index:idx_model_routing_vector_deletes_created_at" json:"created_at"`
}

// TableName sets the table name for the complexity pending vector delete
// model.
func (ComplexityVectorDelete) TableName() string { return "model_routing_vector_deletes" }

// ComplexitySuggestionWrite is one row a suggestion run writes: a new
// suggestion (From empty), or an existing one written only while it is still
// at status From.
type ComplexitySuggestionWrite struct {
	Suggestion ComplexitySuggestion
	From       string
}

// ComplexitySuggestionFilter selects suggestions. A zero filter selects all.
type ComplexitySuggestionFilter struct {
	// RouterID narrows the selection to one router's suggestions.
	RouterID           string
	IDs                []string
	Statuses           []string
	EmbeddingSignature string
	// OmitCentroids leaves Centroid unread, for callers that only list.
	OmitCentroids bool
	// Limit and Offset page the selection, in its order; a Limit of 0 reads
	// all of it.
	Limit, Offset int
}

// ComplexitySuggestionRunFilter selects runs. A zero filter selects all.
type ComplexitySuggestionRunFilter struct {
	RouterID string
	IDs      []string
}

// ComplexitySuggestionStore is the phrase-suggestion surface. It is embedded
// in LogStore for the reason WarpTopicStore is: a store that forgot a method
// should fail the build.
type ComplexitySuggestionStore interface {
	// UpsertComplexitySuggestions writes suggestions whole, replacing any row
	// with the same ID. A caller changing one field (a dismiss) reads the row,
	// changes it and writes it back: ClickHouse has no partial update.
	UpsertComplexitySuggestions(ctx context.Context, suggestions []ComplexitySuggestion) error
	// ReconcileComplexitySuggestions writes a run's outcome: each existing
	// row only while its status is still the one the run read it at (From),
	// so a decision a user made while the run was going (an accept, a
	// dismiss) is never overwritten; a write with no From is a new row. It
	// returns the IDs of the existing rows left alone for having moved.
	ReconcileComplexitySuggestions(ctx context.Context, writes []ComplexitySuggestionWrite) (skipped []string, err error)
	// ListComplexitySuggestions returns the selected suggestions, largest
	// cluster first. Suggestions have no owner, so the access scope does not
	// apply; the examples are resolved through the scoped log reads.
	ListComplexitySuggestions(ctx context.Context, filter ComplexitySuggestionFilter) ([]ComplexitySuggestion, error)
	// CountComplexitySuggestionsByStatus counts the suggestions the filter
	// selects, per status; its Statuses, Limit and Offset are ignored.
	CountComplexitySuggestionsByStatus(ctx context.Context, filter ComplexitySuggestionFilter) (map[string]int, error)
	// UpdateComplexitySuggestionStatus moves one suggestion from status from
	// to status to, stamping resolvedAt, only while it is still at from. It
	// reports whether it did: false means the suggestion is gone or another
	// write moved it first.
	UpdateComplexitySuggestionStatus(ctx context.Context, id, from, to string, resolvedAt *time.Time) (bool, error)
	// DeleteComplexitySuggestions removes suggestions outright.
	DeleteComplexitySuggestions(ctx context.Context, ids []string) error

	// UpsertComplexitySuggestionRun writes a run whole, replacing any row with
	// the same ID: once when it starts and again when it ends.
	UpsertComplexitySuggestionRun(ctx context.Context, run *ComplexitySuggestionRun) error
	// InsertComplexitySuggestionRun writes a run only when no run has its ID,
	// and reports whether it did: recording a run as queued never replaces
	// what a run with the same ID already recorded, such as its outcome.
	InsertComplexitySuggestionRun(ctx context.Context, run *ComplexitySuggestionRun) (bool, error)
	// ListComplexitySuggestionRuns returns up to limit of the selected runs,
	// newest first.
	ListComplexitySuggestionRuns(ctx context.Context, filter ComplexitySuggestionRunFilter, limit int) ([]ComplexitySuggestionRun, error)
	// PruneComplexitySuggestionRuns keeps one router's newest keep runs and
	// drops the rest of its runs; other routers' are untouched.
	PruneComplexitySuggestionRuns(ctx context.Context, routerID string, keep int) (deletedCount int64, err error)
	// DeleteComplexityRouterSuggestions removes everything a router's
	// suggestions left: its suggestions and its runs, for a deleted router.
	DeleteComplexityRouterSuggestions(ctx context.Context, routerID string) error

	// AddComplexityVectorDeletes records deleted logs whose captured vector
	// delete failed, replacing any row already there for the same log and
	// namespace.
	AddComplexityVectorDeletes(ctx context.Context, rows []ComplexityVectorDelete) error
	// ListComplexityVectorDeletes returns up to limit of those, oldest first.
	ListComplexityVectorDeletes(ctx context.Context, limit int) ([]ComplexityVectorDelete, error)
	// DeleteComplexityVectorDeletes removes exactly the given (log, namespace)
	// rows; the same log's row for another namespace stays.
	DeleteComplexityVectorDeletes(ctx context.Context, rows []ComplexityVectorDelete) error
}

// prepareComplexitySuggestions validates suggestions for a write and stamps
// their times.
func prepareComplexitySuggestions(suggestions []ComplexitySuggestion, now time.Time) error {
	for i := range suggestions {
		s := &suggestions[i]
		if s.ID == "" {
			return fmt.Errorf("complexity suggestion has no id")
		}
		if s.Status == "" {
			s.Status = ComplexitySuggestionStatusOpen
		}
		if s.CreatedAt.IsZero() {
			s.CreatedAt = now
		}
		s.CreatedAt = s.CreatedAt.UTC()
		if s.LastSeenAt.IsZero() {
			s.LastSeenAt = s.CreatedAt
		}
		s.LastSeenAt = s.LastSeenAt.UTC()
		if s.ResolvedAt != nil {
			resolved := s.ResolvedAt.UTC()
			s.ResolvedAt = &resolved
		}
		s.UpdatedAt = now
	}
	return nil
}

// prepareComplexitySuggestionRun validates a run for a write and puts its
// times in UTC.
func prepareComplexitySuggestionRun(run *ComplexitySuggestionRun, now time.Time) error {
	if run == nil || run.ID == "" {
		return fmt.Errorf("complexity suggestion run has no id")
	}
	if run.Status == "" {
		run.Status = ComplexitySuggestionRunRunning
	}
	if run.TriggeredBy == "" {
		run.TriggeredBy = ComplexitySuggestionTriggerSchedule
	}
	if run.StartedAt.IsZero() {
		run.StartedAt = now
	}
	run.StartedAt = run.StartedAt.UTC()
	if run.WindowStart.IsZero() {
		run.WindowStart = run.StartedAt
	}
	run.WindowStart = run.WindowStart.UTC()
	if run.FinishedAt != nil {
		finished := run.FinishedAt.UTC()
		run.FinishedAt = &finished
	}
	return nil
}

// complexityVectorDeleteWhere matches exactly the given (log, namespace)
// rows. An OR of pairs rather than a tuple IN, which not every backend
// accepts with bound arguments.
func complexityVectorDeleteWhere(rows []ComplexityVectorDelete) (string, []any) {
	var b strings.Builder
	args := make([]any, 0, 2*len(rows))
	for i, row := range rows {
		if i > 0 {
			b.WriteString(" OR ")
		}
		b.WriteString("(log_id = ? AND namespace = ?)")
		args = append(args, row.LogID, row.Namespace)
	}
	return b.String(), args
}

// prepareComplexityVectorDeletes checks rows and stamps when each was
// recorded.
func prepareComplexityVectorDeletes(rows []ComplexityVectorDelete, now time.Time) error {
	for i := range rows {
		if rows[i].LogID == "" {
			return fmt.Errorf("complexity vector delete has no log id")
		}
		if rows[i].CreatedAt.IsZero() {
			rows[i].CreatedAt = now
		}
		rows[i].CreatedAt = rows[i].CreatedAt.UTC()
	}
	return nil
}

// complexitySuggestionRunPruneWhere builds the predicate selecting the runs
// beyond the newest keep: at or before the first run past them, in the same
// (started_at, id) order the list uses. Empty when there is nothing to drop.
// One predicate rather than a list of ids, for the reason
// warpTopicUnmatchedPruneWhere gives.
func complexitySuggestionRunPruneWhere(ctx context.Context, db *gorm.DB, routerID string, keep int) (string, []any, error) {
	var surplus []ComplexitySuggestionRun
	if err := db.WithContext(ctx).
		Where("router_id = ?", routerID).
		Select("id", "started_at").
		Order("started_at DESC, id DESC").
		Offset(keep).
		Limit(1).
		Find(&surplus).Error; err != nil {
		return "", nil, err
	}
	if len(surplus) == 0 {
		return "", nil, nil
	}
	placeholder, arg := warpTopicTimeArg(db, surplus[0].StartedAt)
	return fmt.Sprintf("router_id = ? AND (started_at < %s OR (started_at = %s AND id <= ?))", placeholder, placeholder),
		[]any{routerID, arg, arg, surplus[0].ID}, nil
}

// UpsertComplexitySuggestions writes suggestions whole, replacing any row with
// the same ID.
func (s *RDBLogStore) UpsertComplexitySuggestions(ctx context.Context, suggestions []ComplexitySuggestion) error {
	if len(suggestions) == 0 {
		return nil
	}
	if err := prepareComplexitySuggestions(suggestions, time.Now().UTC()); err != nil {
		return err
	}
	return s.db.WithContext(ctx).
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, UpdateAll: true}).
		CreateInBatches(suggestions, complexitySuggestionWriteBatch).Error
}

// ReconcileComplexitySuggestions writes a run's outcome, each existing row
// with one conditional update on the status it was read at.
func (s *RDBLogStore) ReconcileComplexitySuggestions(ctx context.Context, writes []ComplexitySuggestionWrite) ([]string, error) {
	inserts, updates, err := splitComplexitySuggestionWrites(writes)
	if err != nil || (len(inserts) == 0 && len(updates) == 0) {
		return nil, err
	}
	var skipped []string
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, w := range updates {
			row := w.Suggestion
			result := tx.Model(&ComplexitySuggestion{}).
				Where("id = ? AND status = ?", row.ID, w.From).
				Select("*").Omit("id", "created_at").
				Updates(&row)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				skipped = append(skipped, row.ID)
			}
		}
		if len(inserts) == 0 {
			return nil
		}
		return tx.CreateInBatches(inserts, complexitySuggestionWriteBatch).Error
	})
	if err != nil {
		return nil, err
	}
	return skipped, nil
}

// splitComplexitySuggestionWrites prepares a run's writes and splits them into
// new rows and conditional writes to existing ones.
func splitComplexitySuggestionWrites(writes []ComplexitySuggestionWrite) ([]ComplexitySuggestion, []ComplexitySuggestionWrite, error) {
	rows := make([]ComplexitySuggestion, len(writes))
	for i := range writes {
		rows[i] = writes[i].Suggestion
	}
	if err := prepareComplexitySuggestions(rows, time.Now().UTC()); err != nil {
		return nil, nil, err
	}
	var inserts []ComplexitySuggestion
	var updates []ComplexitySuggestionWrite
	for i, w := range writes {
		if w.From == "" {
			inserts = append(inserts, rows[i])
		} else {
			updates = append(updates, ComplexitySuggestionWrite{Suggestion: rows[i], From: w.From})
		}
	}
	return inserts, updates, nil
}

// ListComplexitySuggestions returns the selected suggestions, largest cluster
// first.
//
// This reads through s.db, not ScopedDB: the access scope is a predicate over
// ownership columns this table does not have.
func (s *RDBLogStore) ListComplexitySuggestions(ctx context.Context, filter ComplexitySuggestionFilter) ([]ComplexitySuggestion, error) {
	query := s.db.WithContext(ctx).Model(&ComplexitySuggestion{})
	if filter.OmitCentroids {
		query = query.Omit("centroid")
	}
	if filter.RouterID != "" {
		query = query.Where("router_id = ?", filter.RouterID)
	}
	if len(filter.IDs) > 0 {
		query = query.Where("id IN ?", filter.IDs)
	}
	if len(filter.Statuses) > 0 {
		query = query.Where("status IN ?", filter.Statuses)
	}
	if filter.EmbeddingSignature != "" {
		query = query.Where("embedding_signature = ?", filter.EmbeddingSignature)
	}
	query = query.Order("request_count DESC, id ASC")
	if filter.Limit > 0 {
		query = query.Limit(filter.Limit)
	}
	if filter.Offset > 0 {
		query = query.Offset(filter.Offset)
	}
	var suggestions []ComplexitySuggestion
	err := query.Find(&suggestions).Error
	return suggestions, err
}

// CountComplexitySuggestionsByStatus counts the selected suggestions per
// status.
func (s *RDBLogStore) CountComplexitySuggestionsByStatus(ctx context.Context, filter ComplexitySuggestionFilter) (map[string]int, error) {
	query := s.db.WithContext(ctx).Model(&ComplexitySuggestion{})
	if filter.RouterID != "" {
		query = query.Where("router_id = ?", filter.RouterID)
	}
	if len(filter.IDs) > 0 {
		query = query.Where("id IN ?", filter.IDs)
	}
	if filter.EmbeddingSignature != "" {
		query = query.Where("embedding_signature = ?", filter.EmbeddingSignature)
	}
	var rows []struct {
		Status string
		Count  int64
	}
	if err := query.Select("status, COUNT(*) AS count").Group("status").Scan(&rows).Error; err != nil {
		return nil, err
	}
	counts := make(map[string]int, len(rows))
	for _, row := range rows {
		counts[row.Status] = int(row.Count)
	}
	return counts, nil
}

// UpdateComplexitySuggestionStatus moves a suggestion between statuses with
// one conditional update, so of two decisions read from the same status only
// the first lands.
func (s *RDBLogStore) UpdateComplexitySuggestionStatus(ctx context.Context, id, from, to string, resolvedAt *time.Time) (bool, error) {
	if resolvedAt != nil {
		resolved := resolvedAt.UTC()
		resolvedAt = &resolved
	}
	result := s.db.WithContext(ctx).Model(&ComplexitySuggestion{}).
		Where("id = ? AND status = ?", id, from).
		Updates(map[string]any{"status": to, "resolved_at": resolvedAt, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

// DeleteComplexitySuggestions removes suggestions outright.
func (s *RDBLogStore) DeleteComplexitySuggestions(ctx context.Context, ids []string) error {
	for chunk := range slices.Chunk(ids, complexitySuggestionWriteBatch) {
		if err := s.db.WithContext(ctx).Where("id IN ?", chunk).Delete(&ComplexitySuggestion{}).Error; err != nil {
			return err
		}
	}
	return nil
}

// UpsertComplexitySuggestionRun writes a run whole, replacing any row with the
// same ID.
func (s *RDBLogStore) UpsertComplexitySuggestionRun(ctx context.Context, run *ComplexitySuggestionRun) error {
	if err := prepareComplexitySuggestionRun(run, time.Now().UTC()); err != nil {
		return err
	}
	return s.db.WithContext(ctx).
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, UpdateAll: true}).
		Create(run).Error
}

// InsertComplexitySuggestionRun writes a run unless one with its ID exists,
// in one statement that does nothing on a conflict.
func (s *RDBLogStore) InsertComplexitySuggestionRun(ctx context.Context, run *ComplexitySuggestionRun) (bool, error) {
	if err := prepareComplexitySuggestionRun(run, time.Now().UTC()); err != nil {
		return false, err
	}
	result := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, DoNothing: true}).
		Create(run)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

// ListComplexitySuggestionRuns returns up to limit runs, newest first.
func (s *RDBLogStore) ListComplexitySuggestionRuns(ctx context.Context, filter ComplexitySuggestionRunFilter, limit int) ([]ComplexitySuggestionRun, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("limit must be positive")
	}
	query := s.db.WithContext(ctx).Model(&ComplexitySuggestionRun{})
	if filter.RouterID != "" {
		query = query.Where("router_id = ?", filter.RouterID)
	}
	if len(filter.IDs) > 0 {
		query = query.Where("id IN ?", filter.IDs)
	}
	var runs []ComplexitySuggestionRun
	err := query.Order("started_at DESC, id DESC").Limit(limit).Find(&runs).Error
	return runs, err
}

// PruneComplexitySuggestionRuns keeps one router's newest keep runs.
func (s *RDBLogStore) PruneComplexitySuggestionRuns(ctx context.Context, routerID string, keep int) (int64, error) {
	if keep <= 0 {
		return 0, fmt.Errorf("keep must be positive")
	}
	where, args, err := complexitySuggestionRunPruneWhere(ctx, s.db, routerID, keep)
	if err != nil || where == "" {
		return 0, err
	}
	result := s.db.WithContext(ctx).Where(where, args...).Delete(&ComplexitySuggestionRun{})
	return result.RowsAffected, result.Error
}

// DeleteComplexityRouterSuggestions removes a router's suggestions and runs.
func (s *RDBLogStore) DeleteComplexityRouterSuggestions(ctx context.Context, routerID string) error {
	if routerID == "" {
		return fmt.Errorf("router id is required")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("router_id = ?", routerID).Delete(&ComplexitySuggestion{}).Error; err != nil {
			return err
		}
		return tx.Where("router_id = ?", routerID).Delete(&ComplexitySuggestionRun{}).Error
	})
}

// AddComplexityVectorDeletes records deleted logs whose captured vector delete
// failed.
func (s *RDBLogStore) AddComplexityVectorDeletes(ctx context.Context, rows []ComplexityVectorDelete) error {
	if len(rows) == 0 {
		return nil
	}
	if err := prepareComplexityVectorDeletes(rows, time.Now()); err != nil {
		return err
	}
	return s.db.WithContext(ctx).
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "log_id"}, {Name: "namespace"}}, UpdateAll: true}).
		CreateInBatches(rows, complexitySuggestionWriteBatch).Error
}

// ListComplexityVectorDeletes returns up to limit pending vector deletes,
// oldest first.
func (s *RDBLogStore) ListComplexityVectorDeletes(ctx context.Context, limit int) ([]ComplexityVectorDelete, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("limit must be positive")
	}
	var rows []ComplexityVectorDelete
	err := s.db.WithContext(ctx).Order("created_at ASC, log_id ASC").Limit(limit).Find(&rows).Error
	return rows, err
}

// DeleteComplexityVectorDeletes removes exactly the given (log, namespace)
// rows.
func (s *RDBLogStore) DeleteComplexityVectorDeletes(ctx context.Context, rows []ComplexityVectorDelete) error {
	for chunk := range slices.Chunk(rows, complexitySuggestionWriteBatch) {
		where, args := complexityVectorDeleteWhere(chunk)
		if err := s.db.WithContext(ctx).Where(where, args...).Delete(&ComplexityVectorDelete{}).Error; err != nil {
			return err
		}
	}
	return nil
}
