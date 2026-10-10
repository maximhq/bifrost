package logstore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"gorm.io/gorm"
)

// sessionMoveChunkSize bounds the ids bound into one UPDATE so a long session
// stays under every dialect's parameter limit.
const sessionMoveChunkSize = 500

// ErrSessionMoveUnsupported is returned by log stores that cannot rewrite rows
// in place (ClickHouse stores a row once and replaces it by version).
var ErrSessionMoveUnsupported = fmt.Errorf("moving a session between virtual keys is not supported by this log store")

// SessionMoveTarget is the attribution a moved log row takes on. A nil optional
// field clears the matching column, so a target with no team leaves the row
// with no team rather than the source key's.
type SessionMoveTarget struct {
	VirtualKeyID     string
	VirtualKeyName   string
	TeamID           *string
	TeamName         *string
	CustomerID       *string
	CustomerName     *string
	BusinessUnitID   *string
	BusinessUnitName *string
}

// SessionMoveRequest asks for every log of one session that was recorded under
// SourceVirtualKeyID to be re-attributed to Target.
type SessionMoveRequest struct {
	SessionID          string
	SourceVirtualKeyID string
	Target             SessionMoveTarget
	// RemoveBudgetIDs are budgets only the source key answers to; they are dropped
	// from each moved row's budget_ids. AddBudgetIDs are budgets only the target
	// answers to; they are added. Budgets both keys share are left alone.
	RemoveBudgetIDs []string
	AddBudgetIDs    []string
	// RemoveRateLimitIDs and AddRateLimitIDs do the same for rate_limit_ids.
	RemoveRateLimitIDs []string
	AddRateLimitIDs    []string
	// DryRun reports what would move without writing.
	DryRun bool
}

// SessionMoveRow is the part of a moved log the caller needs to work out how much
// each budget window was charged for it.
type SessionMoveRow struct {
	Timestamp    time.Time
	Cost         float64
	TotalTokens  int64
	BudgetIDs    []string
	RateLimitIDs []string
}

// SessionMoveResult reports the rows a move matched.
type SessionMoveResult struct {
	MovedCount int64
	TotalCost  float64
	Rows       []SessionMoveRow
}

// MoveSessionVirtualKey implements LogStore. Matching rows are selected and
// rewritten in one transaction, by id, so the rows reported are exactly the rows
// changed even while the session is still receiving requests.
func (s *RDBLogStore) MoveSessionVirtualKey(ctx context.Context, req SessionMoveRequest) (*SessionMoveResult, error) {
	if strings.TrimSpace(req.SessionID) == "" {
		return nil, fmt.Errorf("sessionID cannot be empty")
	}
	if req.SourceVirtualKeyID == "" || req.Target.VirtualKeyID == "" {
		return nil, fmt.Errorf("source and target virtual key ids are required")
	}

	type moveRow struct {
		ID           string    `gorm:"column:id"`
		Timestamp    time.Time `gorm:"column:timestamp"`
		Cost         *float64  `gorm:"column:cost"`
		TotalTokens  *int64    `gorm:"column:total_tokens"`
		BudgetIDs    *string   `gorm:"column:budget_ids"`
		RateLimitIDs *string   `gorm:"column:rate_limit_ids"`
	}
	var rows []moveRow
	selectRows := func(db *gorm.DB) error {
		return db.Model(&Log{}).
			Where("parent_request_id = ? AND virtual_key_id = ?", req.SessionID, req.SourceVirtualKeyID).
			Select("id, timestamp, cost, total_tokens, budget_ids, rate_limit_ids").
			Order("id").
			Find(&rows).Error
	}

	if req.DryRun {
		if err := selectRows(s.ScopedDB(ctx)); err != nil {
			return nil, err
		}
		return summarizeSessionMove(rows, func(r moveRow) (time.Time, *float64, *int64, *string, *string) {
			return r.Timestamp, r.Cost, r.TotalTokens, r.BudgetIDs, r.RateLimitIDs
		}), nil
	}

	removeBudgets := stringSet(req.RemoveBudgetIDs)
	removeRateLimits := stringSet(req.RemoveRateLimitIDs)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rows = nil
		if err := selectRows(tx); err != nil {
			return err
		}
		for start := 0; start < len(rows); start += sessionMoveChunkSize {
			end := min(start+sessionMoveChunkSize, len(rows))
			ids := make([]string, 0, end-start)
			for _, row := range rows[start:end] {
				ids = append(ids, row.ID)
			}
			if err := tx.Model(&Log{}).Where("id IN ?", ids).Updates(map[string]any{
				"virtual_key_id":     req.Target.VirtualKeyID,
				"virtual_key_name":   req.Target.VirtualKeyName,
				"team_id":            req.Target.TeamID,
				"team_name":          req.Target.TeamName,
				"customer_id":        req.Target.CustomerID,
				"customer_name":      req.Target.CustomerName,
				"business_unit_id":   req.Target.BusinessUnitID,
				"business_unit_name": req.Target.BusinessUnitName,
				// The multi-valued columns describe the old owner; the single-valued
				// ones above are the fallback readers use when these are empty.
				"team_ids":            nil,
				"team_names":          nil,
				"customer_ids":        nil,
				"customer_names":      nil,
				"business_unit_ids":   nil,
				"business_unit_names": nil,
			}).Error; err != nil {
				return err
			}
		}
		// budget_ids and rate_limit_ids differ per row, so they are rewritten row
		// by row, and only for rows whose list actually changes.
		for _, row := range rows {
			updates := map[string]any{}
			if next, changed := rewriteIDList(row.BudgetIDs, removeBudgets, req.AddBudgetIDs); changed {
				updates["budget_ids"] = next
			}
			if next, changed := rewriteIDList(row.RateLimitIDs, removeRateLimits, req.AddRateLimitIDs); changed {
				updates["rate_limit_ids"] = next
			}
			if len(updates) == 0 {
				continue
			}
			if err := tx.Model(&Log{}).Where("id = ?", row.ID).Updates(updates).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return summarizeSessionMove(rows, func(r moveRow) (time.Time, *float64, *int64, *string, *string) {
		return r.Timestamp, r.Cost, r.TotalTokens, r.BudgetIDs, r.RateLimitIDs
	}), nil
}

func stringSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, v := range values {
		set[v] = struct{}{}
	}
	return set
}

func summarizeSessionMove[T any](rows []T, get func(T) (time.Time, *float64, *int64, *string, *string)) *SessionMoveResult {
	result := &SessionMoveResult{MovedCount: int64(len(rows)), Rows: make([]SessionMoveRow, 0, len(rows))}
	for _, row := range rows {
		ts, cost, tokens, budgetIDs, rateLimitIDs := get(row)
		out := SessionMoveRow{Timestamp: ts, BudgetIDs: parseIDList(budgetIDs), RateLimitIDs: parseIDList(rateLimitIDs)}
		if cost != nil {
			out.Cost = *cost
		}
		if tokens != nil {
			out.TotalTokens = *tokens
		}
		result.TotalCost += out.Cost
		result.Rows = append(result.Rows, out)
	}
	return result
}

// rewriteIDList returns raw's JSON id list without remove and with add
// appended, and whether the list changed. A nil result clears the column.
func rewriteIDList(raw *string, remove map[string]struct{}, add []string) (*string, bool) {
	var current []string
	if raw != nil && *raw != "" {
		if err := sonic.Unmarshal([]byte(*raw), &current); err != nil {
			current = nil
		}
	}
	next := make([]string, 0, len(current)+len(add))
	seen := make(map[string]struct{}, len(current)+len(add))
	for _, id := range current {
		if _, drop := remove[id]; drop {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		next = append(next, id)
	}
	for _, id := range add {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		next = append(next, id)
	}
	if len(next) == 0 {
		return nil, raw != nil && *raw != ""
	}
	data, err := sonic.Marshal(next)
	if err != nil {
		return raw, false
	}
	encoded := string(data)
	return &encoded, raw == nil || *raw != encoded
}

// parseIDList decodes a JSON id list column, treating an empty or malformed
// value as no ids.
func parseIDList(raw *string) []string {
	if raw == nil || *raw == "" {
		return nil
	}
	var ids []string
	if err := sonic.Unmarshal([]byte(*raw), &ids); err != nil {
		return nil
	}
	return ids
}
