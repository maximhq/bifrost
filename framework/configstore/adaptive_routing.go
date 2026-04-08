package configstore

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

// ─── Routing Policy CRUD ──────────────────────────────────────────────────────

func (s *RDBConfigStore) CreateRoutingPolicy(ctx context.Context, p *tables.TableRoutingPolicy) error {
	if p.ID == "" {
		p.ID = uuid.New().String()
	}
	return s.db.WithContext(ctx).Create(p).Error
}

func (s *RDBConfigStore) GetRoutingPolicy(ctx context.Context, id string) (*tables.TableRoutingPolicy, error) {
	var p tables.TableRoutingPolicy
	if err := s.db.WithContext(ctx).First(&p, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *RDBConfigStore) ListRoutingPolicies(ctx context.Context) ([]tables.TableRoutingPolicy, error) {
	var policies []tables.TableRoutingPolicy
	return policies, s.db.WithContext(ctx).Order("name").Find(&policies).Error
}

func (s *RDBConfigStore) UpdateRoutingPolicy(ctx context.Context, id string, updates map[string]any) error {
	updates["updated_at"] = time.Now()
	return s.db.WithContext(ctx).Model(&tables.TableRoutingPolicy{}).Where("id = ?", id).Updates(updates).Error
}

func (s *RDBConfigStore) DeleteRoutingPolicy(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Where("id = ?", id).Delete(&tables.TableRoutingPolicy{}).Error
}

// ─── Provider Metrics CRUD ────────────────────────────────────────────────────

func (s *RDBConfigStore) UpsertProviderMetrics(ctx context.Context, m *tables.TableProviderMetrics) error {
	if m.ID == "" {
		m.ID = uuid.New().String()
	}
	m.UpdatedAt = time.Now()
	return s.db.WithContext(ctx).
		Where("provider = ? AND model = ? AND window_start = ? AND window_minutes = ?",
			m.Provider, m.Model, m.WindowStart, m.WindowMinutes).
		Assign(*m).
		FirstOrCreate(m).Error
}

func (s *RDBConfigStore) GetProviderMetrics(ctx context.Context, provider, model string, windowMinutes int, since time.Time) ([]tables.TableProviderMetrics, error) {
	var metrics []tables.TableProviderMetrics
	q := s.db.WithContext(ctx).Where("window_minutes = ? AND window_start >= ?", windowMinutes, since)
	if provider != "" {
		q = q.Where("provider = ?", provider)
	}
	if model != "" {
		q = q.Where("model = ?", model)
	}
	return metrics, q.Order("window_start DESC").Find(&metrics).Error
}

// ─── Model Quality Score CRUD ─────────────────────────────────────────────────

func (s *RDBConfigStore) UpsertModelQualityScore(ctx context.Context, score *tables.TableModelQualityScore) error {
	score.UpdatedAt = time.Now()
	return s.db.WithContext(ctx).
		Where("provider = ? AND model = ?", score.Provider, score.Model).
		Assign(*score).
		FirstOrCreate(score).Error
}

func (s *RDBConfigStore) ListModelQualityScores(ctx context.Context) ([]tables.TableModelQualityScore, error) {
	var scores []tables.TableModelQualityScore
	return scores, s.db.WithContext(ctx).Order("provider, model").Find(&scores).Error
}
