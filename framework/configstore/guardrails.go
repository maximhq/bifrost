package configstore

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
)

// ─── Guardrail Policy CRUD ────────────────────────────────────────────────────

func (s *RDBConfigStore) CreateGuardrailPolicy(ctx context.Context, p *tables.TableGuardrailPolicy) error {
	if p.ID == "" {
		p.ID = uuid.New().String()
	}
	return s.db.WithContext(ctx).Create(p).Error
}

func (s *RDBConfigStore) GetGuardrailPolicy(ctx context.Context, id string) (*tables.TableGuardrailPolicy, error) {
	var p tables.TableGuardrailPolicy
	if err := s.db.WithContext(ctx).First(&p, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *RDBConfigStore) ListGuardrailPolicies(ctx context.Context) ([]tables.TableGuardrailPolicy, error) {
	var policies []tables.TableGuardrailPolicy
	return policies, s.db.WithContext(ctx).Order("name").Find(&policies).Error
}

func (s *RDBConfigStore) UpdateGuardrailPolicy(ctx context.Context, id string, updates map[string]any) error {
	updates["updated_at"] = time.Now()
	return s.db.WithContext(ctx).Model(&tables.TableGuardrailPolicy{}).Where("id = ?", id).Updates(updates).Error
}

func (s *RDBConfigStore) DeleteGuardrailPolicy(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("policy_id = ?", id).Delete(&tables.TableGuardrailRule{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&tables.TableGuardrailPolicy{}).Error
	})
}

// ─── Guardrail Rule CRUD ──────────────────────────────────────────────────────

func (s *RDBConfigStore) CreateGuardrailRule(ctx context.Context, r *tables.TableGuardrailRule) error {
	if r.ID == "" {
		r.ID = uuid.New().String()
	}
	return s.db.WithContext(ctx).Create(r).Error
}

func (s *RDBConfigStore) ListGuardrailRules(ctx context.Context, policyID string) ([]tables.TableGuardrailRule, error) {
	var rules []tables.TableGuardrailRule
	return rules, s.db.WithContext(ctx).Where("policy_id = ?", policyID).Find(&rules).Error
}

func (s *RDBConfigStore) DeleteGuardrailRule(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Where("id = ?", id).Delete(&tables.TableGuardrailRule{}).Error
}

// ─── Guardrail Violation CRUD ─────────────────────────────────────────────────

func (s *RDBConfigStore) AppendGuardrailViolation(ctx context.Context, v *tables.TableGuardrailViolation) error {
	if v.ID == "" {
		v.ID = uuid.New().String()
	}
	if v.Timestamp.IsZero() {
		v.Timestamp = time.Now()
	}
	return s.db.WithContext(ctx).Create(v).Error
}

// GuardrailViolationQueryOpts filters violation queries.
type GuardrailViolationQueryOpts struct {
	PolicyID string
	Layer    string
	Action   string
	Start    time.Time
	End      time.Time
	Page     int
	PageSize int
}

func (s *RDBConfigStore) QueryGuardrailViolations(ctx context.Context, opts GuardrailViolationQueryOpts) ([]tables.TableGuardrailViolation, int64, error) {
	q := s.db.WithContext(ctx).Model(&tables.TableGuardrailViolation{})
	if opts.PolicyID != "" { q = q.Where("policy_id = ?", opts.PolicyID) }
	if opts.Layer != "" { q = q.Where("layer = ?", opts.Layer) }
	if opts.Action != "" { q = q.Where("action = ?", opts.Action) }
	if !opts.Start.IsZero() { q = q.Where("timestamp >= ?", opts.Start) }
	if !opts.End.IsZero() { q = q.Where("timestamp <= ?", opts.End) }

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count: %w", err)
	}
	pageSize := opts.PageSize
	if pageSize <= 0 { pageSize = 50 }
	page := opts.Page
	if page <= 0 { page = 1 }

	var violations []tables.TableGuardrailViolation
	err := q.Order("timestamp DESC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&violations).Error
	return violations, total, err
}
