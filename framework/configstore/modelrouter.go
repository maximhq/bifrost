package configstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
)

const (
	maxModelRouterIDLength   = 64  // matches the id column
	maxModelRouterNameLength = 255 // matches the name column
)

// ModelRouter is one named complexity classifier: its identity plus the
// classifier configuration the Complexity Router page edits, phrase
// suggestions included. The config is embedded so a router reads, in
// config.json and over the API, as that config with an id, a name and a
// description beside it.
//
// Routers are separate from the Complexity Router page's config, which stays
// in governance_config and is the one routing rules read complexity_tier from.
type ModelRouter struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	ComplexityAnalyzerConfig
	ConfigHash string `json:"-"` // hash of the config.json entry's name and description last applied
}

// ValidateModelRouterIdentity trims a router's id, name and description in
// place and checks them; the store's create and update run the same check.
// IDs are limited to letters, digits, '-' and '_' because other stores key
// data by them (vector store namespaces, log rows), and a UUID fits.
func ValidateModelRouterIdentity(r *ModelRouter) error {
	r.ID = strings.TrimSpace(r.ID)
	r.Name = strings.TrimSpace(r.Name)
	r.Description = strings.TrimSpace(r.Description)
	if r.ID == "" {
		return fmt.Errorf("model router id is required")
	}
	if len(r.ID) > maxModelRouterIDLength {
		return fmt.Errorf("model router id must be at most %d characters, got %d", maxModelRouterIDLength, len(r.ID))
	}
	for _, ch := range r.ID {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_') {
			return fmt.Errorf("model router id %q may contain only letters, digits, '-' and '_'", r.ID)
		}
	}
	if r.Name == "" {
		return fmt.Errorf("model router %q requires a name", r.ID)
	}
	if n := utf8.RuneCountInString(r.Name); n > maxModelRouterNameLength {
		return fmt.Errorf("model router name must be at most %d characters, got %d", maxModelRouterNameLength, n)
	}
	return nil
}

// GenerateModelRouterHash hashes the fields of a config.json router entry that
// the per-section config hashes do not cover: its name and its description.
func GenerateModelRouterHash(r ModelRouter) string {
	hash := sha256.New()
	writeHashField(hash, "name", strings.TrimSpace(r.Name))
	writeHashField(hash, "description", strings.TrimSpace(r.Description))
	return hex.EncodeToString(hash.Sum(nil))
}

// valueOrZero dereferences a nullable column, reading NULL as the zero value.
func valueOrZero[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}

// marshalModelRouterPhrases encodes one tier's phrase list for its column.
func marshalModelRouterPhrases(phrases []string) (string, error) {
	if len(phrases) == 0 {
		return "", nil
	}
	data, err := json.Marshal(phrases)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// unmarshalModelRouterPhrases decodes one tier's phrase column; empty is no phrases.
func unmarshalModelRouterPhrases(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var phrases []string
	if err := json.Unmarshal([]byte(raw), &phrases); err != nil {
		return nil, err
	}
	return phrases, nil
}

// modelRouterToTable lays a router out as its row, one column per setting. It
// does not normalize: what it is given is what the row holds.
func modelRouterToTable(r *ModelRouter) (*tables.TableModelRouter, error) {
	cfg := &r.ComplexityAnalyzerConfig
	row := &tables.TableModelRouter{
		ID:                    r.ID,
		Name:                  r.Name,
		Description:           r.Description,
		Classifier:            cfg.Classifier,
		SimpleMediumBoundary:  cfg.TierBoundaries.SimpleMedium,
		MediumComplexBoundary: cfg.TierBoundaries.MediumComplex,
		ConfigHash:            r.ConfigHash,
	}
	var err error
	if row.SimplePhrasesJSON, err = marshalModelRouterPhrases(cfg.Keywords.SimpleKeywords); err != nil {
		return nil, fmt.Errorf("encode simple phrases: %w", err)
	}
	if row.MediumPhrasesJSON, err = marshalModelRouterPhrases(cfg.Keywords.MediumKeywords); err != nil {
		return nil, fmt.Errorf("encode medium phrases: %w", err)
	}
	if row.ComplexPhrasesJSON, err = marshalModelRouterPhrases(cfg.Keywords.ComplexKeywords); err != nil {
		return nil, fmt.Errorf("encode complex phrases: %w", err)
	}
	if !cfg.ConfigHashes.Empty() {
		hashes, err := json.Marshal(cfg.ConfigHashes)
		if err != nil {
			return nil, fmt.Errorf("encode config hashes: %w", err)
		}
		row.ConfigHashesJSON = string(hashes)
	}
	if s := cfg.Semantic; s != nil {
		row.SemanticProvider = bifrost.Ptr(string(s.Provider))
		row.SemanticEmbeddingModel = bifrost.Ptr(s.EmbeddingModel)
		row.SemanticTimeoutNS = bifrost.Ptr(int64(s.Timeout))
		row.SemanticMinSimilarity = bifrost.Ptr(s.MinSimilarity)
		row.SemanticMessageHistoryCount = bifrost.Ptr(s.MessageHistoryCount)
		row.SemanticCountTowardBudgets = bifrost.Ptr(s.CountTowardBudgets)
		row.SemanticVectorStore = bifrost.Ptr(s.VectorStore)
		row.SemanticFallback = bifrost.Ptr(s.Fallback)
	}
	if d := cfg.Decision; d != nil {
		row.DecisionProvider = bifrost.Ptr(string(d.Provider))
		row.DecisionModel = bifrost.Ptr(d.Model)
		if d.PreviousMessageCount != nil {
			row.DecisionPreviousMessageCount = bifrost.Ptr(*d.PreviousMessageCount)
		}
		row.DecisionTimeoutNS = bifrost.Ptr(int64(d.Timeout))
		row.DecisionMinConfidence = bifrost.Ptr(d.MinConfidence)
		if len(d.Criteria) > 0 {
			criteria, err := json.Marshal(d.Criteria)
			if err != nil {
				return nil, fmt.Errorf("encode decision criteria: %w", err)
			}
			row.DecisionCriteriaJSON = bifrost.Ptr(string(criteria))
		}
	}
	if l := cfg.LLM; l != nil {
		row.LLMProvider = bifrost.Ptr(string(l.Provider))
		row.LLMModel = bifrost.Ptr(l.Model)
		row.LLMTimeoutNS = bifrost.Ptr(int64(l.Timeout))
		row.LLMPrompt = bifrost.Ptr(l.Prompt)
		row.LLMMessageHistoryCount = bifrost.Ptr(l.MessageHistoryCount)
		row.LLMCountTowardBudgets = bifrost.Ptr(l.CountTowardBudgets)
	}
	if cfg.Session != nil {
		row.SessionEnabled = bifrost.Ptr(cfg.Session.Enabled)
	}
	if g := cfg.Suggestions; g != nil {
		row.SuggestionsEnabled = bifrost.Ptr(g.Enabled)
		row.SuggestionsSampleRate = bifrost.Ptr(g.SampleRate)
		row.SuggestionsLookbackDays = bifrost.Ptr(g.LookbackDays)
		row.SuggestionsMaxClusteredRequests = bifrost.Ptr(g.MaxClusteredRequests)
		row.SuggestionsMinClusterRequests = bifrost.Ptr(g.MinClusterRequests)
		row.SuggestionsProvider = bifrost.Ptr(string(g.Provider))
		row.SuggestionsModel = bifrost.Ptr(g.Model)
		row.SuggestionsInstructions = bifrost.Ptr(g.Instructions)
		if sc := g.Schedule; sc != nil {
			row.SuggestionsScheduleEvery = bifrost.Ptr(sc.Every)
			if sc.StartAt != nil {
				row.SuggestionsScheduleStartAt = bifrost.Ptr(sc.StartAt.UTC())
			}
		}
	}
	return row, nil
}

// modelRouterFromTable reads a row back into a router exactly as stored, with
// no normalization or validation. A column it cannot decode is reported as
// ErrConfigUnreadable.
func modelRouterFromTable(row *tables.TableModelRouter) (*ModelRouter, error) {
	r := &ModelRouter{
		ID:          row.ID,
		Name:        row.Name,
		Description: row.Description,
		ConfigHash:  row.ConfigHash,
	}
	cfg := &r.ComplexityAnalyzerConfig
	cfg.Classifier = row.Classifier
	cfg.TierBoundaries = ComplexityTierBoundaries{SimpleMedium: row.SimpleMediumBoundary, MediumComplex: row.MediumComplexBoundary}
	var err error
	if cfg.Keywords.SimpleKeywords, err = unmarshalModelRouterPhrases(row.SimplePhrasesJSON); err != nil {
		return nil, fmt.Errorf("%w: model router %q simple phrases: %w", ErrConfigUnreadable, row.ID, err)
	}
	if cfg.Keywords.MediumKeywords, err = unmarshalModelRouterPhrases(row.MediumPhrasesJSON); err != nil {
		return nil, fmt.Errorf("%w: model router %q medium phrases: %w", ErrConfigUnreadable, row.ID, err)
	}
	if cfg.Keywords.ComplexKeywords, err = unmarshalModelRouterPhrases(row.ComplexPhrasesJSON); err != nil {
		return nil, fmt.Errorf("%w: model router %q complex phrases: %w", ErrConfigUnreadable, row.ID, err)
	}
	cfg.ConfigHashes = decodeModelRouterHashes(row.ConfigHashesJSON)
	if row.SemanticProvider != nil {
		cfg.Semantic = &ComplexitySemanticConfig{
			Provider:            schemas.ModelProvider(*row.SemanticProvider),
			EmbeddingModel:      valueOrZero(row.SemanticEmbeddingModel),
			Timeout:             time.Duration(valueOrZero(row.SemanticTimeoutNS)),
			MinSimilarity:       valueOrZero(row.SemanticMinSimilarity),
			MessageHistoryCount: valueOrZero(row.SemanticMessageHistoryCount),
			CountTowardBudgets:  valueOrZero(row.SemanticCountTowardBudgets),
			VectorStore:         valueOrZero(row.SemanticVectorStore),
			Fallback:            valueOrZero(row.SemanticFallback),
		}
	}
	if row.DecisionProvider != nil {
		decision := &ComplexityDecisionConfig{
			Provider:      schemas.ModelProvider(*row.DecisionProvider),
			Model:         valueOrZero(row.DecisionModel),
			Timeout:       time.Duration(valueOrZero(row.DecisionTimeoutNS)),
			MinConfidence: valueOrZero(row.DecisionMinConfidence),
		}
		if row.DecisionPreviousMessageCount != nil {
			decision.PreviousMessageCount = bifrost.Ptr(*row.DecisionPreviousMessageCount)
		}
		if raw := valueOrZero(row.DecisionCriteriaJSON); strings.TrimSpace(raw) != "" {
			if err := json.Unmarshal([]byte(raw), &decision.Criteria); err != nil {
				return nil, fmt.Errorf("%w: model router %q decision criteria: %w", ErrConfigUnreadable, row.ID, err)
			}
		}
		cfg.Decision = decision
	}
	if row.LLMProvider != nil {
		cfg.LLM = &ComplexityLLMConfig{
			Provider:            schemas.ModelProvider(*row.LLMProvider),
			Model:               valueOrZero(row.LLMModel),
			Timeout:             time.Duration(valueOrZero(row.LLMTimeoutNS)),
			Prompt:              valueOrZero(row.LLMPrompt),
			MessageHistoryCount: valueOrZero(row.LLMMessageHistoryCount),
			CountTowardBudgets:  valueOrZero(row.LLMCountTowardBudgets),
		}
	}
	if row.SessionEnabled != nil {
		cfg.Session = &ComplexitySessionConfig{Enabled: *row.SessionEnabled}
	}
	if row.SuggestionsEnabled != nil {
		suggestions := &ComplexitySuggestionsConfig{
			Enabled:              *row.SuggestionsEnabled,
			SampleRate:           valueOrZero(row.SuggestionsSampleRate),
			LookbackDays:         valueOrZero(row.SuggestionsLookbackDays),
			MaxClusteredRequests: valueOrZero(row.SuggestionsMaxClusteredRequests),
			MinClusterRequests:   valueOrZero(row.SuggestionsMinClusterRequests),
			Provider:             schemas.ModelProvider(valueOrZero(row.SuggestionsProvider)),
			Model:                valueOrZero(row.SuggestionsModel),
			Instructions:         valueOrZero(row.SuggestionsInstructions),
		}
		if row.SuggestionsScheduleEvery != nil {
			suggestions.Schedule = &ComplexitySuggestionsSchedule{
				Every: *row.SuggestionsScheduleEvery,
			}
			if row.SuggestionsScheduleStartAt != nil {
				suggestions.Schedule.StartAt = bifrost.Ptr(row.SuggestionsScheduleStartAt.UTC())
			}
		}
		cfg.Suggestions = suggestions
	}
	return r, nil
}

// decodeModelRouterHashes reads the stored section hashes. They are
// bookkeeping, so a value this version cannot decode reads as none: the next
// config.json sync re-applies the file, which is what an unknown hash means.
func decodeModelRouterHashes(raw string) ComplexityAnalyzerConfigHashes {
	var hashes ComplexityAnalyzerConfigHashes
	if strings.TrimSpace(raw) == "" {
		return hashes
	}
	if err := json.Unmarshal([]byte(raw), &hashes); err != nil {
		return ComplexityAnalyzerConfigHashes{}
	}
	return hashes
}

// decodeModelRouterRow reads a row into a runnable router: decoded, normalized
// and validated. A row that does not validate is ErrConfigUnreadable, so
// callers with defaults degrade instead of failing.
func decodeModelRouterRow(row *tables.TableModelRouter) (*ModelRouter, error) {
	r, err := modelRouterFromTable(row)
	if err != nil {
		return nil, err
	}
	normalized := r.ComplexityAnalyzerConfig.Normalized()
	if err := normalized.Validate(); err != nil {
		return nil, fmt.Errorf("%w: invalid model router %q: %w", ErrConfigUnreadable, row.ID, err)
	}
	r.ComplexityAnalyzerConfig = normalized
	return r, nil
}

// GetModelRouters returns every stored router, oldest first. A row this
// version cannot read is logged and left out rather than failing the list.
func (s *RDBConfigStore) GetModelRouters(ctx context.Context) ([]ModelRouter, error) {
	var rows []tables.TableModelRouter
	if err := s.DB().WithContext(ctx).Order("created_at ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	routers := make([]ModelRouter, 0, len(rows))
	for i := range rows {
		router, err := decodeModelRouterRow(&rows[i])
		if err != nil {
			if s.logger != nil {
				s.logger.Warn("skipping unreadable model router %q: %v", rows[i].ID, err)
			}
			continue
		}
		routers = append(routers, *router)
	}
	return routers, nil
}

// GetModelRouter returns one router. It is ErrNotFound when no row exists and
// ErrConfigUnreadable when the row exists but this version cannot run it.
func (s *RDBConfigStore) GetModelRouter(ctx context.Context, id string) (*ModelRouter, error) {
	router, err := s.getModelRouterWithDB(ctx, s.DB(), strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	if router == nil {
		return nil, ErrNotFound
	}
	return router, nil
}

// getModelRouterWithDB reads one router, returning nil when no row exists.
func (s *RDBConfigStore) getModelRouterWithDB(ctx context.Context, db *gorm.DB, id string) (*ModelRouter, error) {
	var row tables.TableModelRouter
	if err := db.WithContext(ctx).First(&row, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return decodeModelRouterRow(&row)
}

// prepareModelRouter returns a copy of router ready to store: identity
// trimmed and checked, and config normalized and validated.
func prepareModelRouter(router *ModelRouter) (ModelRouter, error) {
	if router == nil {
		return ModelRouter{}, fmt.Errorf("model router is nil")
	}
	r := *router
	if err := ValidateModelRouterIdentity(&r); err != nil {
		return ModelRouter{}, err
	}
	normalized := r.ComplexityAnalyzerConfig.Normalized()
	if err := normalized.Validate(); err != nil {
		return ModelRouter{}, err
	}
	r.ComplexityAnalyzerConfig = normalized
	return r, nil
}

// CreateModelRouter stores a new router. A taken id or name is
// ErrAlreadyExists.
func (s *RDBConfigStore) CreateModelRouter(ctx context.Context, router *ModelRouter, tx ...*gorm.DB) error {
	r, err := prepareModelRouter(router)
	if err != nil {
		return err
	}
	row, err := modelRouterToTable(&r)
	if err != nil {
		return err
	}
	return s.inModelRouterTx(ctx, tx, func(txDB *gorm.DB) error {
		db := txDB.WithContext(ctx)
		var taken int64
		if err := db.Model(&tables.TableModelRouter{}).Where("id = ? OR name = ?", r.ID, r.Name).Count(&taken).Error; err != nil {
			return err
		}
		if taken > 0 {
			return fmt.Errorf("%w: a model router with id %q or name %q", ErrAlreadyExists, r.ID, r.Name)
		}
		// The count and the insert do not share a lock, so a concurrent create
		// of the same id or name can still lose at the unique index.
		if err := db.Create(row).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) || isUniqueConstraintError(err) {
				return fmt.Errorf("%w: a model router with id %q or name %q", ErrAlreadyExists, r.ID, r.Name)
			}
			return err
		}
		return nil
	})
}

// UpdateModelRouter replaces a router's name, description and config. Section
// hashes and the config hash the caller leaves empty keep their stored values,
// so a UI save never erases config.json bookkeeping. The router must already
// exist (ErrNotFound).
func (s *RDBConfigStore) UpdateModelRouter(ctx context.Context, router *ModelRouter, tx ...*gorm.DB) error {
	r, err := prepareModelRouter(router)
	if err != nil {
		return err
	}
	return s.inModelRouterTx(ctx, tx, func(txDB *gorm.DB) error {
		// A missing router is ErrNotFound even when its new name is taken.
		existing, err := s.lockModelRouterRow(ctx, txDB, r.ID)
		if err != nil {
			return err
		}
		if existing == nil {
			return ErrNotFound
		}
		var taken int64
		if err := txDB.WithContext(ctx).Model(&tables.TableModelRouter{}).
			Where("name = ? AND id <> ?", r.Name, r.ID).Count(&taken).Error; err != nil {
			return err
		}
		if taken > 0 {
			return fmt.Errorf("%w: a model router named %q", ErrAlreadyExists, r.Name)
		}
		_, err = s.saveModelRouterWithTx(ctx, txDB, r, false)
		return err
	})
}

// DeleteModelRouter removes a router; ErrNotFound when there is none.
func (s *RDBConfigStore) DeleteModelRouter(ctx context.Context, id string, tx ...*gorm.DB) error {
	id = strings.TrimSpace(id)
	return s.inModelRouterTx(ctx, tx, func(txDB *gorm.DB) error {
		result := txDB.WithContext(ctx).Delete(&tables.TableModelRouter{}, "id = ?", id)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// ResetModelRouter restores a router's tier boundaries and phrase lists from
// defaults and keeps everything else: the classifier settings, the section
// hashes and the router's name. It returns what was persisted.
//
// The read and the save share one transaction under the router's row lock, so
// a concurrent edit to a section the reset preserves cannot be overwritten by
// a stale copy. A stored config this version cannot read resets to the
// defaults, since that is the state reset exists to recover from.
func (s *RDBConfigStore) ResetModelRouter(ctx context.Context, id string, defaults *ComplexityAnalyzerConfig) (*ModelRouter, error) {
	if defaults == nil {
		return nil, fmt.Errorf("model router defaults are nil")
	}
	id = strings.TrimSpace(id)
	var persisted *ModelRouter
	err := s.DB().WithContext(ctx).Transaction(func(txDB *gorm.DB) error {
		row, err := s.lockModelRouterRow(ctx, txDB, id)
		if err != nil {
			return err
		}
		if row == nil {
			return ErrNotFound
		}
		restored := ModelRouter{ID: row.ID, ComplexityAnalyzerConfig: *defaults}
		existing, err := decodeModelRouterRow(row)
		if err != nil && !errors.Is(err, ErrConfigUnreadable) {
			return err
		}
		if existing != nil {
			restored = *existing
			restored.TierBoundaries = defaults.TierBoundaries
			restored.Keywords = defaults.Keywords
		}
		restored.ComplexityAnalyzerConfig = restored.ComplexityAnalyzerConfig.Normalized()
		if err := restored.ComplexityAnalyzerConfig.Validate(); err != nil {
			return err
		}
		persisted, err = s.saveModelRouterWithTx(ctx, txDB, restored, true)
		return err
	})
	if err != nil {
		return nil, err
	}
	return persisted, nil
}

// inModelRouterTx runs fn in the caller's transaction, or in its own so the
// lock, the carry-over read and the save cannot interleave with another write.
func (s *RDBConfigStore) inModelRouterTx(ctx context.Context, tx []*gorm.DB, fn func(txDB *gorm.DB) error) error {
	if len(tx) > 0 && tx[0] != nil {
		return fn(tx[0])
	}
	return s.DB().WithContext(ctx).Transaction(fn)
}

// lockModelRouterRow takes the row lock that serializes saves and resets of one
// router, and returns the locked row, or nil when there is none.
//
// txDB must be a transaction, otherwise the lock is released at once.
func (s *RDBConfigStore) lockModelRouterRow(ctx context.Context, txDB *gorm.DB, id string) (*tables.TableModelRouter, error) {
	var row tables.TableModelRouter
	if err := dbForUpdate(txDB.WithContext(ctx)).First(&row, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

// saveModelRouterWithTx writes r under its row lock and returns what was
// written. Section hashes and the config hash that r leaves empty are read
// back from the stored row, which is locked first so a concurrent writer
// cannot slip a stale copy in between. They are read from the raw columns:
// stored bookkeeping this version cannot decode must not stop a valid save.
//
// keepIdentity keeps the stored name and description, for saves that only
// carry a config. r's config must already be normalized and validated, and
// txDB must be a transaction.
func (s *RDBConfigStore) saveModelRouterWithTx(ctx context.Context, txDB *gorm.DB, r ModelRouter, keepIdentity bool) (*ModelRouter, error) {
	existing, err := s.lockModelRouterRow(ctx, txDB, r.ID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, ErrNotFound
	}
	if r.ConfigHashes.Empty() {
		r.ConfigHashes = decodeModelRouterHashes(existing.ConfigHashesJSON)
	}
	if keepIdentity {
		r.Name, r.Description, r.ConfigHash = existing.Name, existing.Description, existing.ConfigHash
	} else if r.ConfigHash == "" {
		r.ConfigHash = existing.ConfigHash
	}
	row, err := modelRouterToTable(&r)
	if err != nil {
		return nil, err
	}
	row.CreatedAt = existing.CreatedAt
	row.UpdatedAt = time.Now()
	// The row is locked, so this is an update of every column, NULLs included.
	if err := txDB.WithContext(ctx).Save(row).Error; err != nil {
		return nil, err
	}
	return &r, nil
}
