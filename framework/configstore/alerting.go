package configstore

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

// ─── Alert Rule CRUD ──────────────────────────────────────────────────────────

func (s *RDBConfigStore) CreateAlertRule(ctx context.Context, r *tables.TableAlertRule) error {
	if r.ID == "" {
		r.ID = uuid.New().String()
	}
	return s.db.WithContext(ctx).Create(r).Error
}

func (s *RDBConfigStore) GetAlertRule(ctx context.Context, id string) (*tables.TableAlertRule, error) {
	var r tables.TableAlertRule
	if err := s.db.WithContext(ctx).First(&r, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *RDBConfigStore) ListAlertRules(ctx context.Context) ([]tables.TableAlertRule, error) {
	var rules []tables.TableAlertRule
	return rules, s.db.WithContext(ctx).Order("name").Find(&rules).Error
}

func (s *RDBConfigStore) UpdateAlertRule(ctx context.Context, id string, updates map[string]any) error {
	updates["updated_at"] = time.Now()
	return s.db.WithContext(ctx).Model(&tables.TableAlertRule{}).Where("id = ?", id).Updates(updates).Error
}

func (s *RDBConfigStore) DeleteAlertRule(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Where("id = ?", id).Delete(&tables.TableAlertRule{}).Error
}

// ─── Alert Channel CRUD ───────────────────────────────────────────────────────

func (s *RDBConfigStore) CreateAlertChannel(ctx context.Context, c *tables.TableAlertChannel) error {
	if c.ID == "" {
		c.ID = uuid.New().String()
	}
	return s.db.WithContext(ctx).Create(c).Error
}

func (s *RDBConfigStore) GetAlertChannel(ctx context.Context, id string) (*tables.TableAlertChannel, error) {
	var c tables.TableAlertChannel
	if err := s.db.WithContext(ctx).First(&c, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *RDBConfigStore) ListAlertChannels(ctx context.Context) ([]tables.TableAlertChannel, error) {
	var channels []tables.TableAlertChannel
	return channels, s.db.WithContext(ctx).Order("name").Find(&channels).Error
}

func (s *RDBConfigStore) UpdateAlertChannel(ctx context.Context, id string, updates map[string]any) error {
	updates["updated_at"] = time.Now()
	return s.db.WithContext(ctx).Model(&tables.TableAlertChannel{}).Where("id = ?", id).Updates(updates).Error
}

func (s *RDBConfigStore) DeleteAlertChannel(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Where("id = ?", id).Delete(&tables.TableAlertChannel{}).Error
}

// ─── Alert State CRUD ─────────────────────────────────────────────────────────

func (s *RDBConfigStore) UpsertAlertState(ctx context.Context, state *tables.TableAlertState) error {
	state.UpdatedAt = time.Now()
	return s.db.WithContext(ctx).
		Where("rule_id = ?", state.RuleID).
		Assign(*state).
		FirstOrCreate(state).Error
}

func (s *RDBConfigStore) GetAlertState(ctx context.Context, ruleID string) (*tables.TableAlertState, error) {
	var state tables.TableAlertState
	if err := s.db.WithContext(ctx).First(&state, "rule_id = ?", ruleID).Error; err != nil {
		return nil, err
	}
	return &state, nil
}

func (s *RDBConfigStore) ListAlertStates(ctx context.Context) ([]tables.TableAlertState, error) {
	var states []tables.TableAlertState
	return states, s.db.WithContext(ctx).Find(&states).Error
}

// ─── Alert History ────────────────────────────────────────────────────────────

func (s *RDBConfigStore) AppendAlertHistory(ctx context.Context, h *tables.TableAlertHistory) error {
	if h.ID == "" {
		h.ID = uuid.New().String()
	}
	if h.Timestamp.IsZero() {
		h.Timestamp = time.Now()
	}
	return s.db.WithContext(ctx).Create(h).Error
}

type AlertHistoryQueryOpts struct {
	RuleID   string
	Severity string
	Start    time.Time
	End      time.Time
	Page     int
	PageSize int
}

func (s *RDBConfigStore) QueryAlertHistory(ctx context.Context, opts AlertHistoryQueryOpts) ([]tables.TableAlertHistory, int64, error) {
	q := s.db.WithContext(ctx).Model(&tables.TableAlertHistory{})
	if opts.RuleID != "" { q = q.Where("rule_id = ?", opts.RuleID) }
	if opts.Severity != "" { q = q.Where("severity = ?", opts.Severity) }
	if !opts.Start.IsZero() { q = q.Where("timestamp >= ?", opts.Start) }
	if !opts.End.IsZero() { q = q.Where("timestamp <= ?", opts.End) }

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	pageSize := opts.PageSize
	if pageSize <= 0 { pageSize = 50 }
	page := opts.Page
	if page <= 0 { page = 1 }

	var history []tables.TableAlertHistory
	err := q.Order("timestamp DESC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&history).Error
	return history, total, err
}
