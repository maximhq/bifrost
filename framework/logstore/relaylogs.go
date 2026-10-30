package logstore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RelayLog records one call to a relay (/relay/{endpoint}/{path}),
// or the settlement of an async job one started. It is a table of its own, as MCP tool and Agent
// Gateway logs are, because a relay call has no provider or model: its cost is the
// endpoint's pricing formula, recorded here broken down.
type RelayLog struct {
	ID        string    `gorm:"primaryKey;type:varchar(255)" json:"id"`
	Timestamp time.Time `gorm:"index:idx_relay_logs_timestamp;not null" json:"timestamp"`
	CreatedAt time.Time `gorm:"index:idx_relay_logs_created_at;not null;autoCreateTime:false;default:CURRENT_TIMESTAMP" json:"created_at"`

	RelayName  string `gorm:"type:varchar(64);index:idx_relay_logs_relay_name;not null" json:"relay_name"`
	Kind       string `gorm:"type:varchar(16);index:idx_relay_logs_kind;not null;default:'call'" json:"kind"` // call | job
	Method     string `gorm:"type:varchar(10);index:idx_relay_logs_method" json:"method"`
	Path       string `gorm:"type:text" json:"path"`
	RoutePath  string `gorm:"type:text" json:"route_path,omitempty"`                               // matched route glob, "" for none
	Status     string `gorm:"type:varchar(32);index:idx_relay_logs_status;not null" json:"status"` // success | error | cancelled
	StatusCode int    `json:"status_code"`
	Streamed   bool   `json:"streamed"`
	Billable   bool   `json:"billable"`
	Model      string `gorm:"type:varchar(255)" json:"model,omitempty"`

	Latency           *float64 `json:"latency,omitempty"`          // milliseconds, pre-hooks to the end of the reply
	UpstreamLatency   *float64 `json:"upstream_latency,omitempty"` // milliseconds blocked on the upstream socket; nil = unmeasured
	OverheadLatency   *float64 `json:"overhead_latency,omitempty"` // Bifrost overhead (total minus upstream), ms; nil = unmeasured
	OverheadBreakdown string   `gorm:"type:text" json:"-"`         // Name-keyed JSON object, mirroring Log.OverheadBreakdown
	BytesOut          int64    `json:"bytes_out"`

	// Cost is the figure charged to budgets; the rest explains it.
	Cost           *float64 `gorm:"index:idx_relay_logs_cost" json:"cost,omitempty"`
	CostPerRequest *float64 `json:"cost_per_request,omitempty"`
	CostPerUnit    *float64 `json:"cost_per_unit,omitempty"`
	ExtractedCost  *float64 `json:"extracted_cost,omitempty"`
	Units          *float64 `json:"units,omitempty"`
	BilledUnits    *float64 `json:"billed_units,omitempty"`
	InputTokens    int      `json:"input_tokens,omitempty"`
	OutputTokens   int      `json:"output_tokens,omitempty"`
	TotalTokens    int      `json:"total_tokens,omitempty"`

	JobID       *string `gorm:"type:varchar(255);index:idx_relay_logs_job_id" json:"job_id,omitempty"`
	JobStatus   *string `gorm:"type:varchar(64)" json:"job_status,omitempty"`
	ParentLogID *string `gorm:"type:varchar(255);index:idx_relay_logs_parent_log_id" json:"parent_log_id,omitempty"` // a job row's start call

	UserID           *string `gorm:"type:varchar(255);index:idx_relay_logs_user_id" json:"user_id,omitempty"`
	UserName         *string `gorm:"type:varchar(255)" json:"user_name,omitempty"`
	VirtualKeyID     *string `gorm:"type:varchar(255);index:idx_relay_logs_virtual_key_id" json:"virtual_key_id,omitempty"`
	VirtualKeyName   *string `gorm:"type:varchar(255)" json:"virtual_key_name,omitempty"`
	TeamID           *string `gorm:"type:varchar(255);index:idx_relay_logs_team_id" json:"team_id,omitempty"`
	TeamName         *string `gorm:"type:varchar(255)" json:"team_name,omitempty"`
	CustomerID       *string `gorm:"type:varchar(255);index:idx_relay_logs_customer_id" json:"customer_id,omitempty"`
	CustomerName     *string `gorm:"type:varchar(255)" json:"customer_name,omitempty"`
	BusinessUnitID   *string `gorm:"type:varchar(255);index:idx_relay_logs_business_unit_id" json:"business_unit_id,omitempty"`
	BusinessUnitName *string `gorm:"type:varchar(255)" json:"business_unit_name,omitempty"`
	ProjectID        *string `gorm:"type:varchar(255);index:idx_relay_logs_project_id" json:"project_id,omitempty"`
	ProjectName      *string `gorm:"type:varchar(255)" json:"project_name,omitempty"`
	UserAgent        *string `gorm:"type:varchar(512);index:idx_relay_logs_user_agent" json:"user_agent,omitempty"` // Raw HTTP User-Agent of the calling client
	App              *string `gorm:"type:varchar(128);index:idx_relay_logs_app" json:"app,omitempty"`               // Backend-detected client app derived from user_agent
	// Every team, customer and business unit the call settled, as on logs and mcp_tool_logs: the
	// scalar columns keep the first, these keep them all, and the dimension filters read both.
	TeamIDs                 *string               `gorm:"type:text" json:"-"` // JSON serialized []string
	TeamNames               *string               `gorm:"type:text" json:"-"` // JSON serialized []string
	CustomerIDs             *string               `gorm:"type:text" json:"-"` // JSON serialized []string
	CustomerNames           *string               `gorm:"type:text" json:"-"` // JSON serialized []string
	BusinessUnitIDs         *string               `gorm:"type:text" json:"-"` // JSON serialized []string
	BusinessUnitNames       *string               `gorm:"type:text" json:"-"` // JSON serialized []string
	BudgetIDs               *string               `gorm:"type:text" json:"-"`
	RateLimitIDs            *string               `gorm:"type:text" json:"-"`
	RequestID               string                `gorm:"type:varchar(255);index:idx_relay_logs_request_id;not null" json:"request_id"`
	Metadata                *string               `gorm:"type:text" json:"-"` // JSON map: logging headers, x-bf-lh-* labels, x-bf-dim-* dimensions and the key's metadata, as on LLM and MCP rows
	RequestHeaders          *string               `gorm:"type:text" json:"-"`
	ResponseHeaders         *string               `gorm:"type:text" json:"-"`
	RequestBody             *string               `gorm:"type:text" json:"request_body,omitempty"`
	ResponseBody            *string               `gorm:"type:text" json:"response_body,omitempty"`
	ErrorDetails            string                `gorm:"type:text" json:"-"`
	ContentHidden           bool                  `gorm:"default:false" json:"content_hidden"`
	PayloadReference        *string               `gorm:"type:varchar(1024)" json:"-"` // object key of the full bodies in hybrid mode
	HasObject               bool                  `gorm:"default:false" json:"-"`
	PluginLogs              string                `gorm:"type:text" json:"plugin_logs,omitempty"`
	BudgetIDsParsed         []string              `gorm:"-" json:"budget_ids,omitempty"`
	RateLimitIDsParsed      []string              `gorm:"-" json:"rate_limit_ids,omitempty"`
	MetadataParsed          map[string]any        `gorm:"-" json:"metadata,omitempty"`
	RequestHeadersParsed    map[string]string     `gorm:"-" json:"request_headers,omitempty"`
	ResponseHeadersParsed   map[string]string     `gorm:"-" json:"response_headers,omitempty"`
	ErrorDetailsParsed      *schemas.BifrostError `gorm:"-" json:"error_details,omitempty"`
	OverheadBreakdownParsed []OverheadBucket      `gorm:"-" json:"overhead_breakdown,omitempty"`
	TeamIDsParsed           []string              `gorm:"-" json:"team_ids,omitempty"`
	TeamNamesParsed         []string              `gorm:"-" json:"team_names,omitempty"`
	CustomerIDsParsed       []string              `gorm:"-" json:"customer_ids,omitempty"`
	CustomerNamesParsed     []string              `gorm:"-" json:"customer_names,omitempty"`
	BusinessUnitIDsParsed   []string              `gorm:"-" json:"business_unit_ids,omitempty"`
	BusinessUnitNamesParsed []string              `gorm:"-" json:"business_unit_names,omitempty"`
}

// Relay log kinds and statuses.
const (
	RelayLogKindCall = "call"
	RelayLogKindJob  = "job"

	RelayLogStatusSuccess   = "success"
	RelayLogStatusError     = "error"
	RelayLogStatusCancelled = "cancelled"
)

func (RelayLog) TableName() string { return "relay_logs" }

func (l *RelayLog) BeforeCreate(_ *gorm.DB) error {
	if l.Timestamp.IsZero() {
		l.Timestamp = time.Now().UTC()
	}
	if l.CreatedAt.IsZero() {
		l.CreatedAt = l.Timestamp
	}
	if l.Kind == "" {
		l.Kind = RelayLogKindCall
	}
	return l.SerializeFields()
}

func (l *RelayLog) AfterFind(_ *gorm.DB) error {
	return l.DeserializeFields()
}

// SerializeFields writes the parsed fields into their stored columns.
func (l *RelayLog) SerializeFields() error {
	marshal := func(v any, stored **string) error {
		data, err := sonic.Marshal(v)
		if err != nil {
			return err
		}
		value := string(data)
		*stored = &value
		return nil
	}
	if len(l.BudgetIDsParsed) > 0 {
		if err := marshal(l.BudgetIDsParsed, &l.BudgetIDs); err != nil {
			return fmt.Errorf("failed to serialize relay budget ids: %w", err)
		}
	}
	if len(l.RateLimitIDsParsed) > 0 {
		if err := marshal(l.RateLimitIDsParsed, &l.RateLimitIDs); err != nil {
			return fmt.Errorf("failed to serialize relay rate limit ids: %w", err)
		}
	}
	for _, column := range []struct {
		name   string
		parsed []string
		stored **string
	}{
		{"team ids", l.TeamIDsParsed, &l.TeamIDs}, {"team names", l.TeamNamesParsed, &l.TeamNames},
		{"customer ids", l.CustomerIDsParsed, &l.CustomerIDs}, {"customer names", l.CustomerNamesParsed, &l.CustomerNames},
		{"business unit ids", l.BusinessUnitIDsParsed, &l.BusinessUnitIDs}, {"business unit names", l.BusinessUnitNamesParsed, &l.BusinessUnitNames},
	} {
		if len(column.parsed) == 0 {
			continue
		}
		if err := marshal(column.parsed, column.stored); err != nil {
			return fmt.Errorf("failed to serialize relay %s: %w", column.name, err)
		}
	}
	if len(l.MetadataParsed) > 0 {
		if err := marshal(l.MetadataParsed, &l.Metadata); err != nil {
			return fmt.Errorf("failed to serialize relay metadata: %w", err)
		}
	}
	if len(l.RequestHeadersParsed) > 0 {
		if err := marshal(l.RequestHeadersParsed, &l.RequestHeaders); err != nil {
			return fmt.Errorf("failed to serialize relay request headers: %w", err)
		}
	}
	if len(l.ResponseHeadersParsed) > 0 {
		if err := marshal(l.ResponseHeadersParsed, &l.ResponseHeaders); err != nil {
			return fmt.Errorf("failed to serialize relay response headers: %w", err)
		}
	}
	if l.ErrorDetailsParsed != nil {
		data, err := sonic.Marshal(l.ErrorDetailsParsed)
		if err != nil {
			return fmt.Errorf("failed to serialize relay error details: %w", err)
		}
		l.ErrorDetails = string(data)
	}
	if len(l.OverheadBreakdownParsed) > 0 {
		data, err := marshalOverheadBreakdown(l.OverheadBreakdownParsed)
		if err != nil {
			return fmt.Errorf("failed to serialize relay overhead breakdown: %w", err)
		}
		l.OverheadBreakdown = data
	}
	return nil
}

// DeserializeFields reads the stored columns back into their parsed fields. A malformed column is
// left unparsed rather than failing the read.
func (l *RelayLog) DeserializeFields() error {
	unmarshal := func(stored *string, v any) {
		if stored != nil && *stored != "" {
			_ = sonic.Unmarshal([]byte(*stored), v)
		}
	}
	unmarshal(l.BudgetIDs, &l.BudgetIDsParsed)
	unmarshal(l.RateLimitIDs, &l.RateLimitIDsParsed)
	unmarshal(l.TeamIDs, &l.TeamIDsParsed)
	unmarshal(l.TeamNames, &l.TeamNamesParsed)
	unmarshal(l.CustomerIDs, &l.CustomerIDsParsed)
	unmarshal(l.CustomerNames, &l.CustomerNamesParsed)
	unmarshal(l.BusinessUnitIDs, &l.BusinessUnitIDsParsed)
	unmarshal(l.BusinessUnitNames, &l.BusinessUnitNamesParsed)
	unmarshal(l.Metadata, &l.MetadataParsed)
	unmarshal(l.RequestHeaders, &l.RequestHeadersParsed)
	unmarshal(l.ResponseHeaders, &l.ResponseHeadersParsed)
	l.ErrorDetailsParsed = nil
	if l.ErrorDetails != "" {
		var details schemas.BifrostError
		if err := sonic.Unmarshal([]byte(l.ErrorDetails), &details); err == nil {
			l.ErrorDetailsParsed = &details
		}
	}
	if l.OverheadBreakdown != "" {
		l.OverheadBreakdownParsed = unmarshalOverheadBreakdown(l.OverheadBreakdown)
	}
	return nil
}

// RelayLogFilters narrows a Relay Logs search. Slice fields match any of their
// values; every populated field must match.
type RelayLogFilters struct {
	EndpointNames   []string   `json:"relay_names,omitempty"`
	Methods         []string   `json:"methods,omitempty"`
	Statuses        []string   `json:"statuses,omitempty"`
	Kinds           []string   `json:"kinds,omitempty"`
	VirtualKeyIDs   []string   `json:"virtual_key_ids,omitempty"`
	UserIDs         []string   `json:"user_ids,omitempty"`
	TeamIDs         []string   `json:"team_ids,omitempty"`
	CustomerIDs     []string   `json:"customer_ids,omitempty"`
	BusinessUnitIDs []string   `json:"business_unit_ids,omitempty"`
	ProjectIDs      []string   `json:"project_ids,omitempty"`
	Apps            []string   `json:"apps,omitempty"`
	Billable        *bool      `json:"billable,omitempty"`
	StartTime       *time.Time `json:"start_time,omitempty"`
	EndTime         *time.Time `json:"end_time,omitempty"`
	MinCost         *float64   `json:"min_cost,omitempty"`
	MaxCost         *float64   `json:"max_cost,omitempty"`
	RequestID       string     `json:"request_id,omitempty"`
	JobID           string     `json:"job_id,omitempty"`
	// Search matches the path, route, model and request id: metadata columns only, never bodies.
	Search string `json:"search,omitempty"`
}

// RelayLogSearchResult is one page of a Relay Logs search.
type RelayLogSearchResult struct {
	Logs       []*RelayLog       `json:"logs"`
	Pagination PaginationOptions `json:"pagination"`
	HasLogs    bool              `json:"has_logs"`
}

// RelayLogStats aggregates a Relay Logs search.
type RelayLogStats struct {
	TotalRequests  int64   `json:"total_requests"`
	SuccessCount   int64   `json:"success_count"`
	ErrorCount     int64   `json:"error_count"`
	SuccessRate    float64 `json:"success_rate"`    // percentage of requests that succeeded
	AverageLatency float64 `json:"average_latency"` // milliseconds
	TotalCost      float64 `json:"total_cost"`
	TotalUnits     float64 `json:"total_units"`
	TotalBytesOut  int64   `json:"total_bytes_out"`
}

// RelayHistogramBucket is one time bucket of volume, latency, units and spend, so the page's
// metric strip can draw the same sparklines the LLM logs page draws.
type RelayHistogramBucket struct {
	Timestamp  time.Time `json:"timestamp"`
	Count      int64     `json:"count"`
	Success    int64     `json:"success"`
	Error      int64     `json:"error"`
	Cancelled  int64     `json:"cancelled"` // calls the caller hung up on, as in the LLM logs histogram
	Cost       float64   `json:"cost"`
	AvgLatency float64   `json:"avg_latency"` // milliseconds, 0 when nothing was timed
	Units      float64   `json:"units"`       // billed units
	BytesOut   int64     `json:"bytes_out"`
}

// RelayHistogramResult is a time-bucketed Relay Logs histogram.
type RelayHistogramResult struct {
	Buckets           []RelayHistogramBucket `json:"buckets"`
	BucketSizeSeconds int64                  `json:"bucket_size_seconds"`
}

// RelayTopResult is one endpoint's totals in a top-endpoints ranking.
type RelayTopResult struct {
	RelayName      string  `json:"relay_name"`
	Count          int64   `json:"count"`
	ErrorCount     int64   `json:"error_count"`
	Cost           float64 `json:"cost"`
	AverageLatency float64 `json:"average_latency"`
}

// RelayTopResults ranks endpoints by request count.
type RelayTopResults struct {
	Endpoints []RelayTopResult `json:"endpoints"`
}

// RelayFilterData lists the values the Relay Logs filters can offer.
// RelayFilterData is the subset of filter dimensions a caller asked for; a dimension not asked
// for is absent. Mirrors AgentFilterData and the MCP filter data.
type RelayFilterData struct {
	EndpointNames []string            `json:"relay_names,omitempty"`
	Methods       []string            `json:"methods,omitempty"`
	Apps          []string            `json:"apps,omitempty"`
	VirtualKeys   []RelayFilterOption `json:"virtual_keys,omitempty"`
	Users         []RelayFilterOption `json:"users,omitempty"`
	Teams         []RelayFilterOption `json:"teams,omitempty"`
	Customers     []RelayFilterOption `json:"customers,omitempty"`
	BusinessUnits []RelayFilterOption `json:"business_units,omitempty"`
	Projects      []RelayFilterOption `json:"projects,omitempty"`
}

// RelayFilterDimensions are the dimensions GetRelayFilterData can list.
var RelayFilterDimensions = []string{"relay_names", "methods", "apps", "virtual_keys", "users", "teams", "customers", "business_units", "projects"}

// RelayFilterOption is an id with the name recorded for it.
type RelayFilterOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// relayLogSortColumns maps the sortable fields to their columns.
var relayLogSortColumns = map[string]string{
	"timestamp": "timestamp",
	"latency":   "latency",
	"cost":      "cost",
}

func (s *RDBLogStore) applyRelayLogFilters(query *gorm.DB, f RelayLogFilters) *gorm.DB {
	for column, values := range map[string][]string{
		"relay_name": f.EndpointNames, "method": f.Methods, "status": f.Statuses, "kind": f.Kinds,
		"virtual_key_id": f.VirtualKeyIDs, "user_id": f.UserIDs, "project_id": f.ProjectIDs, "app": f.Apps,
	} {
		if len(values) > 0 {
			query = query.Where(column+" IN ?", values)
		}
	}
	// A call settled under several teams, customers or business units matches on any of them, as
	// on the logs and mcp_tool_logs tables.
	for _, dimension := range []struct {
		scalar, array string
		values        []string
	}{
		{"team_id", "team_ids", f.TeamIDs}, {"customer_id", "customer_ids", f.CustomerIDs}, {"business_unit_id", "business_unit_ids", f.BusinessUnitIDs},
	} {
		if len(dimension.values) > 0 {
			sql, args := dimensionMembershipFilterSQL(s.db.Dialector.Name(), dimension.scalar, dimension.array, dimension.values)
			query = query.Where(sql, args...)
		}
	}
	if f.Billable != nil {
		query = query.Where("billable = ?", *f.Billable)
	}
	// timestampBound keeps sub-second bounds on ClickHouse, whose driver drops them otherwise.
	if f.StartTime != nil {
		placeholder, arg := timestampBound(s.db.Dialector.Name(), *f.StartTime, lowerTimeBound)
		query = query.Where("timestamp >= "+placeholder, arg)
	}
	if f.EndTime != nil {
		placeholder, arg := timestampBound(s.db.Dialector.Name(), *f.EndTime, upperTimeBound)
		query = query.Where("timestamp <= "+placeholder, arg)
	}
	if f.MinCost != nil {
		query = query.Where("cost >= ?", *f.MinCost)
	}
	if f.MaxCost != nil {
		query = query.Where("cost <= ?", *f.MaxCost)
	}
	if f.RequestID != "" {
		query = query.Where("request_id = ?", f.RequestID)
	}
	if f.JobID != "" {
		query = query.Where("job_id = ?", f.JobID)
	}
	if search := strings.TrimSpace(f.Search); search != "" {
		like := "%" + escapeLike(search) + "%"
		// Backslash is the default LIKE escape on Postgres, MySQL and ClickHouse, and ClickHouse
		// rejects an ESCAPE clause; only SQLite needs it declared.
		escape := ""
		if query.Dialector != nil && query.Dialector.Name() == "sqlite" {
			escape = ` ESCAPE '\'`
		}
		query = query.Where("(path LIKE ?"+escape+" OR route_path LIKE ?"+escape+" OR model LIKE ?"+escape+" OR request_id LIKE ?"+escape+")", like, like, like, like)
	}
	return query
}

// escapeLike escapes the LIKE wildcards in a search term.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// BatchCreateRelayLogsIfNotExists inserts the rows whose id is not stored yet and
// returns the ids it inserted.
func (s *RDBLogStore) BatchCreateRelayLogsIfNotExists(ctx context.Context, entries []*RelayLog) ([]string, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	inserted := make([]string, 0, len(entries))
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, entry := range entries {
			if entry == nil {
				continue
			}
			result := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, DoNothing: true}).Create(entry)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 1 {
				inserted = append(inserted, entry.ID)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return inserted, nil
}

// FindRelayLog loads one row, ErrNotFound when it does not exist or is out of scope.
func (s *RDBLogStore) FindRelayLog(ctx context.Context, id string) (*RelayLog, error) {
	var entry RelayLog
	if err := s.ScopedDB(ctx).Where("id = ?", id).First(&entry).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &entry, nil
}

// SearchRelayLogs returns one page of rows matching the filters, newest first by default.
func (s *RDBLogStore) SearchRelayLogs(ctx context.Context, filters RelayLogFilters, pagination PaginationOptions) (*RelayLogSearchResult, error) {
	query := s.applyRelayLogFilters(s.ScopedDB(ctx).Model(&RelayLog{}), filters)
	if !pagination.SkipCount {
		if err := query.Session(&gorm.Session{}).Count(&pagination.TotalCount).Error; err != nil {
			return nil, fmt.Errorf("failed to count relay logs: %w", err)
		}
	}
	column, ok := relayLogSortColumns[pagination.SortBy]
	if !ok {
		column = "timestamp"
	}
	direction := "DESC"
	if strings.EqualFold(pagination.Order, "asc") {
		direction = "ASC"
	}
	if pagination.Limit <= 0 || pagination.Limit > 1000 {
		pagination.Limit = 50
	}
	var logs []*RelayLog
	if err := query.Order(column + " " + direction).Order("id " + direction).Limit(pagination.Limit).Offset(pagination.Offset).Find(&logs).Error; err != nil {
		return nil, fmt.Errorf("failed to search relay logs: %w", err)
	}
	hasLogs := len(logs) > 0
	if !hasLogs {
		var probe []string
		if err := s.ScopedDB(ctx).Model(&RelayLog{}).Limit(1).Pluck("id", &probe).Error; err == nil {
			hasLogs = len(probe) > 0
		}
	}
	return &RelayLogSearchResult{Logs: logs, Pagination: pagination, HasLogs: hasLogs}, nil
}

// GetRelayLogStats aggregates the rows matching the filters.
func (s *RDBLogStore) GetRelayLogStats(ctx context.Context, filters RelayLogFilters) (*RelayLogStats, error) {
	query := s.applyRelayLogFilters(s.ScopedDB(ctx).Model(&RelayLog{}), filters)
	var row struct {
		Total          int64   `gorm:"column:total"`
		SuccessCount   int64   `gorm:"column:success_count"`
		ErrorCount     int64   `gorm:"column:error_count"`
		AverageLatency float64 `gorm:"column:average_latency"`
		TotalCost      float64 `gorm:"column:total_cost"`
		TotalUnits     float64 `gorm:"column:total_units"`
		TotalBytesOut  int64   `gorm:"column:total_bytes_out"`
	}
	if err := query.Select(`
			COUNT(*) as total,
			SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END) as success_count,
			SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END) as error_count,
			COALESCE(AVG(latency), 0) as average_latency,
			COALESCE(SUM(cost), 0) as total_cost,
			COALESCE(SUM(billed_units), 0) as total_units,
			COALESCE(SUM(bytes_out), 0) as total_bytes_out
		`).Scan(&row).Error; err != nil {
		return nil, fmt.Errorf("failed to get relay log stats: %w", err)
	}
	stats := &RelayLogStats{
		TotalRequests: row.Total, SuccessCount: row.SuccessCount, ErrorCount: row.ErrorCount,
		AverageLatency: row.AverageLatency, TotalCost: row.TotalCost, TotalUnits: row.TotalUnits, TotalBytesOut: row.TotalBytesOut,
	}
	if terminal := row.SuccessCount + row.ErrorCount; terminal > 0 {
		stats.SuccessRate = float64(row.SuccessCount) / float64(terminal) * 100
	}
	return stats, nil
}

// GetRelayHistogram returns time-bucketed volume and spend for the filters.
func (s *RDBLogStore) GetRelayHistogram(ctx context.Context, filters RelayLogFilters, bucketSizeSeconds int64) (*RelayHistogramResult, error) {
	if bucketSizeSeconds <= 0 {
		bucketSizeSeconds = 3600
	}
	query := s.applyRelayLogFilters(s.ScopedDB(ctx).Model(&RelayLog{}), filters)
	var results []struct {
		BucketTimestamp int64   `gorm:"column:bucket_timestamp"`
		Count           int64   `gorm:"column:count"`
		Success         int64   `gorm:"column:success"`
		Error           int64   `gorm:"column:error"`
		Cancelled       int64   `gorm:"column:cancelled_count"`
		Cost            float64 `gorm:"column:total_cost"`
		AvgLatency      float64 `gorm:"column:avg_latency"`
		Units           float64 `gorm:"column:total_units"`
		BytesOut        int64   `gorm:"column:total_bytes_out"`
	}
	selectClause := fmt.Sprintf(`
			%s as bucket_timestamp,
			COUNT(*) as count,
			SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END) as success,
			SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END) as error,
			SUM(CASE WHEN status = 'cancelled' THEN 1 ELSE 0 END) as cancelled_count,
			COALESCE(SUM(cost), 0) as total_cost,
			COALESCE(AVG(latency), 0) as avg_latency,
			COALESCE(SUM(billed_units), 0) as total_units,
			COALESCE(SUM(bytes_out), 0) as total_bytes_out
		`, unixBucketExpr(s.db.Dialector.Name(), bucketSizeSeconds))
	if err := query.Select(selectClause).Group("bucket_timestamp").Order("bucket_timestamp ASC").Find(&results).Error; err != nil {
		return nil, fmt.Errorf("failed to get relay histogram: %w", err)
	}
	byTimestamp := make(map[int64]RelayHistogramBucket, len(results))
	for _, r := range results {
		byTimestamp[r.BucketTimestamp] = RelayHistogramBucket{
			Timestamp: time.Unix(r.BucketTimestamp, 0).UTC(), Count: r.Count, Success: r.Success, Error: r.Error, Cancelled: r.Cancelled,
			Cost: r.Cost, AvgLatency: r.AvgLatency, Units: r.Units, BytesOut: r.BytesOut,
		}
	}
	all := generateBucketTimestamps(filters.StartTime, filters.EndTime, bucketSizeSeconds)
	if len(all) == 0 {
		buckets := make([]RelayHistogramBucket, 0, len(results))
		for _, r := range results {
			buckets = append(buckets, byTimestamp[r.BucketTimestamp])
		}
		return &RelayHistogramResult{Buckets: buckets, BucketSizeSeconds: bucketSizeSeconds}, nil
	}
	buckets := make([]RelayHistogramBucket, len(all))
	for i, ts := range all {
		if bucket, ok := byTimestamp[ts]; ok {
			buckets[i] = bucket
			continue
		}
		buckets[i] = RelayHistogramBucket{Timestamp: time.Unix(ts, 0).UTC()}
	}
	return &RelayHistogramResult{Buckets: buckets, BucketSizeSeconds: bucketSizeSeconds}, nil
}

// GetRelayTopEndpoints ranks endpoints by request count for the filters.
func (s *RDBLogStore) GetRelayTopEndpoints(ctx context.Context, filters RelayLogFilters, limit int) (*RelayTopResults, error) {
	if limit <= 0 {
		limit = 10
	}
	query := s.applyRelayLogFilters(s.ScopedDB(ctx).Model(&RelayLog{}), filters)
	// total_cost, not cost: ClickHouse resolves a SELECT alias in WHERE, and the cost filters would
	// then name an aggregate.
	var rows []struct {
		RelayName      string  `gorm:"column:relay_name"`
		Count          int64   `gorm:"column:count"`
		ErrorCount     int64   `gorm:"column:error_count"`
		TotalCost      float64 `gorm:"column:total_cost"`
		AverageLatency float64 `gorm:"column:average_latency"`
	}
	if err := query.
		Select("relay_name, COUNT(*) as count, SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END) as error_count, COALESCE(SUM(cost), 0) as total_cost, COALESCE(AVG(latency), 0) as average_latency").
		Group("relay_name").
		Order("count DESC, relay_name ASC").
		Limit(limit).
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to get top relays: %w", err)
	}
	results := make([]RelayTopResult, 0, len(rows))
	for _, r := range rows {
		results = append(results, RelayTopResult{RelayName: r.RelayName, Count: r.Count, ErrorCount: r.ErrorCount, Cost: r.TotalCost, AverageLatency: r.AverageLatency})
	}
	return &RelayTopResults{Endpoints: results}, nil
}

// GetRelayFilterData lists the distinct values of the asked-for dimensions (every dimension when
// none is named), each narrowed by search and bounded by limit, so a sidebar section loads only
// what it shows, as the Agent and MCP filter data do. Names are searched, ids are returned.
func (s *RDBLogStore) GetRelayFilterData(ctx context.Context, dimensions []string, limit int, search string) (*RelayFilterData, error) {
	if limit <= 0 {
		limit = 100
	}
	wanted := make(map[string]struct{}, len(dimensions))
	for _, dimension := range dimensions {
		wanted[dimension] = struct{}{}
	}
	if len(wanted) == 0 {
		for _, dimension := range RelayFilterDimensions {
			wanted[dimension] = struct{}{}
		}
	}
	search = strings.TrimSpace(search)
	like := func(q *gorm.DB, column string) *gorm.DB {
		if search == "" {
			return q
		}
		switch s.db.Dialector.Name() {
		case "postgres":
			return q.Where(column+" ILIKE ?", "%"+escapeLike(search)+"%")
		case "clickhouse":
			return q.Where("lowerUTF8("+column+") LIKE ?", "%"+strings.ToLower(escapeLike(search))+"%")
		default:
			return q.Where("LOWER("+column+") LIKE ? ESCAPE '\\'", "%"+strings.ToLower(escapeLike(search))+"%")
		}
	}
	out := &RelayFilterData{}
	values := func(column string) ([]string, error) {
		rows := []string{}
		q := s.ScopedDB(ctx).Model(&RelayLog{}).Distinct().Where(column + " IS NOT NULL AND " + column + " <> ''")
		if err := like(q, column).Order(column).Limit(limit).Pluck(column, &rows).Error; err != nil {
			return nil, err
		}
		return rows, nil
	}
	options := func(idColumn, nameColumn string) ([]RelayFilterOption, error) {
		var rows []struct {
			ID   string `gorm:"column:id"`
			Name string `gorm:"column:name"`
		}
		queryID, queryName := idColumn, nameColumn
		q := s.ScopedDB(ctx).Model(&RelayLog{})
		var src dimensionReadSource
		// Teams, customers and business units fan out over the JSON-array columns, so a call
		// settled under several of them lists every one, as the Agent Logs choices do.
		if from, ok := relayDimensionFanoutFrom(s.db.Dialector.Name(), idColumn); ok {
			q = s.ScopedDB(ctx).Table("?", gorm.Expr(from))
			q.Statement.Table = "logs"
			queryID, queryName = "dim_id", "dim_name"
			src.FannedOut = true
		}
		q = q.Select(queryID + " AS id, MAX(" + queryName + ") AS name").
			Where(queryID + " IS NOT NULL AND " + queryID + " <> ''")
		q = applyDimensionCeiling(ctx, q, src, idColumn)
		q = like(q, queryName)
		if err := q.Group(queryID).Order("name").Limit(limit).Scan(&rows).Error; err != nil {
			return nil, err
		}
		result := make([]RelayFilterOption, 0, len(rows))
		for _, row := range rows {
			result = append(result, RelayFilterOption{ID: row.ID, Name: row.Name})
		}
		return result, nil
	}
	var err error
	for _, dimension := range RelayFilterDimensions {
		if _, ok := wanted[dimension]; !ok {
			continue
		}
		switch dimension {
		case "relay_names":
			out.EndpointNames, err = values("relay_name")
		case "methods":
			out.Methods, err = values("method")
		case "apps":
			out.Apps, err = values("app")
		case "virtual_keys":
			out.VirtualKeys, err = options("virtual_key_id", "virtual_key_name")
		case "users":
			out.Users, err = options("user_id", "user_name")
		case "teams":
			out.Teams, err = options("team_id", "team_name")
		case "customers":
			out.Customers, err = options("customer_id", "customer_name")
		case "business_units":
			out.BusinessUnits, err = options("business_unit_id", "business_unit_name")
		case "projects":
			out.Projects, err = options("project_id", "project_name")
		}
		if err != nil {
			return nil, fmt.Errorf("failed to get relay %s filter data: %w", dimension, err)
		}
	}
	return out, nil
}

// FindRelayLogsForDeletion loads the rows DeleteRelayLogs removes for these ids: the rows
// themselves and the job rows that settle them.
func (s *RDBLogStore) FindRelayLogsForDeletion(ctx context.Context, ids []string) ([]*RelayLog, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var entries []*RelayLog
	if err := s.ScopedDB(ctx).Where("id IN ? OR parent_log_id IN ?", ids, ids).Find(&entries).Error; err != nil {
		return nil, err
	}
	return entries, nil
}

// UpdateRelayLog applies column updates to one row, ErrNotFound when it does not exist.
func (s *RDBLogStore) UpdateRelayLog(ctx context.Context, id string, updates map[string]any) error {
	result := s.db.WithContext(ctx).Model(&RelayLog{}).Where("id = ?", id).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteRelayLogs deletes the identified rows, with the job rows that settle them.
func (s *RDBLogStore) DeleteRelayLogs(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return s.ScopedDB(ctx).Where("id IN ? OR parent_log_id IN ?", ids, ids).Delete(&RelayLog{}).Error
}

// DeleteRelayLogsBatch deletes up to batchSize rows older than cutoff, oldest first,
// for the retention cleaner.
func (s *RDBLogStore) DeleteRelayLogsBatch(ctx context.Context, cutoff time.Time, batchSize int) (int64, error) {
	if s.db.Dialector.Name() == "clickhouse" {
		return 0, nil // ClickHouseLogStore overrides this with a lightweight delete
	}
	return s.deleteExpiredBatch(ctx, "relay_logs", "timestamp", cutoff, batchSize)
}

// HasRelayLogs reports whether any row is stored, for the empty state.
func (s *RDBLogStore) HasRelayLogs(ctx context.Context) (bool, error) {
	var ids []string
	if err := s.ScopedDB(ctx).Model(&RelayLog{}).Limit(1).Pluck("id", &ids).Error; err != nil {
		return false, err
	}
	return len(ids) > 0, nil
}
