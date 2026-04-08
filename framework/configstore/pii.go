package configstore

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
)

// ─── PII Policy CRUD ──────────────────────────────────────────────────────────

func (s *RDBConfigStore) CreatePIIPolicy(ctx context.Context, p *tables.TablePIIPolicy) error {
	if p.ID == "" {
		p.ID = uuid.New().String()
	}
	return s.db.WithContext(ctx).Create(p).Error
}

func (s *RDBConfigStore) GetPIIPolicy(ctx context.Context, id string) (*tables.TablePIIPolicy, error) {
	var p tables.TablePIIPolicy
	if err := s.db.WithContext(ctx).First(&p, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *RDBConfigStore) ListPIIPolicies(ctx context.Context) ([]tables.TablePIIPolicy, error) {
	var policies []tables.TablePIIPolicy
	return policies, s.db.WithContext(ctx).Order("name").Find(&policies).Error
}

func (s *RDBConfigStore) UpdatePIIPolicy(ctx context.Context, id string, updates map[string]any) error {
	updates["updated_at"] = time.Now()
	return s.db.WithContext(ctx).Model(&tables.TablePIIPolicy{}).Where("id = ?", id).Updates(updates).Error
}

func (s *RDBConfigStore) DeletePIIPolicy(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("policy_id = ?", id).Delete(&tables.TablePIIDetectorRule{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&tables.TablePIIPolicy{}).Error
	})
}

// ─── PII Detector Rule CRUD ───────────────────────────────────────────────────

func (s *RDBConfigStore) CreatePIIDetectorRule(ctx context.Context, r *tables.TablePIIDetectorRule) error {
	if r.ID == "" {
		r.ID = uuid.New().String()
	}
	return s.db.WithContext(ctx).Create(r).Error
}

func (s *RDBConfigStore) ListPIIDetectorRules(ctx context.Context, policyID string) ([]tables.TablePIIDetectorRule, error) {
	var rules []tables.TablePIIDetectorRule
	return rules, s.db.WithContext(ctx).Where("policy_id = ?", policyID).Find(&rules).Error
}

func (s *RDBConfigStore) DeletePIIDetectorRule(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Where("id = ?", id).Delete(&tables.TablePIIDetectorRule{}).Error
}

// ─── PII Token Store ──────────────────────────────────────────────────────────

func (s *RDBConfigStore) UpsertPIIToken(ctx context.Context, t *tables.TablePIITokenStore) error {
	if t.Token == "" {
		t.Token = uuid.New().String()
	}
	return s.db.WithContext(ctx).Where("original_hash = ? AND entity_type = ?", t.OriginalHash, t.EntityType).
		FirstOrCreate(t).Error
}

func (s *RDBConfigStore) GetPIIToken(ctx context.Context, token string) (*tables.TablePIITokenStore, error) {
	var t tables.TablePIITokenStore
	if err := s.db.WithContext(ctx).First(&t, "token = ?", token).Error; err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *RDBConfigStore) DeleteExpiredPIITokens(ctx context.Context) (int64, error) {
	res := s.db.WithContext(ctx).Where("expires_at IS NOT NULL AND expires_at < ?", time.Now()).
		Delete(&tables.TablePIITokenStore{})
	return res.RowsAffected, res.Error
}
