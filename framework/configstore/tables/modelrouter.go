package tables

import "time"

// TableModelRouter stores one model router: a named complexity classifier with
// its own phrases and settings, and the routing that decides which requests it
// takes; its per-tier models live in model_router_targets. One column per
// setting (lists and maps as JSON text). Each optional block (semantic,
// decision, llm, session, suggestions) is present exactly when its anchor
// column is non-NULL. No classifier column has a gorm default, since zero is
// often a real setting; defaults come from the config's normalization. The
// configstore package converts to and from the config type.
type TableModelRouter struct {
	ID          string `gorm:"primaryKey;type:varchar(64)" json:"id"`
	Name        string `gorm:"type:varchar(255);not null;uniqueIndex:idx_model_router_name" json:"name"`
	Description string `gorm:"type:text" json:"description"`
	Classifier  string `gorm:"type:varchar(20);not null" json:"classifier"` // "semantic", "decision" or "llm"

	// Routing: which requests the router takes, tried after the routing rules.
	// These columns do carry defaults, each the value a router without routing
	// gets, so they can be added to existing rows.
	Enabled       bool    `gorm:"not null;default:false" json:"-"`
	Scope         string  `gorm:"type:varchar(50);not null;default:'global'" json:"-"` // "global" | "team" | "customer" | "virtual_key" | "user"
	ScopeID       *string `gorm:"type:varchar(255)" json:"-"`                          // nil for global, otherwise the entity's id
	CelExpression string  `gorm:"type:text" json:"-"`                                  // empty takes every request in scope
	Query         *string `gorm:"type:text" json:"-"`                                  // the dashboard's condition builder state, JSON; never evaluated
	Priority      int     `gorm:"not null;default:0" json:"-"`                         // lower is tried first within a scope

	// Deprecated lexical tier boundaries, still accepted and returned; zero reads as the default.
	SimpleMediumBoundary  float64 `json:"-"`
	MediumComplexBoundary float64 `json:"-"`

	SimplePhrasesJSON  string `gorm:"column:simple_phrases_json;type:text" json:"-"`  // JSON []string
	MediumPhrasesJSON  string `gorm:"column:medium_phrases_json;type:text" json:"-"`  // JSON []string
	ComplexPhrasesJSON string `gorm:"column:complex_phrases_json;type:text" json:"-"` // JSON []string

	SemanticProvider            *string  `gorm:"type:varchar(255)" json:"-"` // anchor: NULL means no semantic block
	SemanticEmbeddingModel      *string  `gorm:"type:varchar(255)" json:"-"`
	SemanticTimeoutNS           *int64   `gorm:"column:semantic_timeout_ns" json:"-"`
	SemanticMinSimilarity       *float64 `json:"-"`
	SemanticMessageHistoryCount *int     `json:"-"`
	SemanticCountTowardBudgets  *bool    `json:"-"`
	SemanticVectorStore         *string  `gorm:"type:varchar(50)" json:"-"`
	SemanticFallback            *string  `gorm:"type:varchar(20)" json:"-"`

	DecisionProvider             *string  `gorm:"type:varchar(255)" json:"-"` // anchor: NULL means no decision block
	DecisionModel                *string  `gorm:"type:varchar(255)" json:"-"`
	DecisionPreviousMessageCount *int     `json:"-"` // NULL and 0 differ: NULL takes the default history
	DecisionTimeoutNS            *int64   `gorm:"column:decision_timeout_ns" json:"-"`
	DecisionMinConfidence        *float64 `json:"-"`
	DecisionCriteriaJSON         *string  `gorm:"column:decision_criteria_json;type:text" json:"-"` // JSON map[tier]criteria

	LLMProvider            *string `gorm:"column:llm_provider;type:varchar(255)" json:"-"` // anchor: NULL means no llm block
	LLMModel               *string `gorm:"column:llm_model;type:varchar(255)" json:"-"`
	LLMTimeoutNS           *int64  `gorm:"column:llm_timeout_ns" json:"-"`
	LLMPrompt              *string `gorm:"column:llm_prompt;type:text" json:"-"`
	LLMMessageHistoryCount *int    `gorm:"column:llm_message_history_count" json:"-"`
	LLMCountTowardBudgets  *bool   `gorm:"column:llm_count_toward_budgets" json:"-"`

	SessionEnabled *bool `json:"-"` // anchor: NULL means no session block

	SuggestionsEnabled              *bool      `json:"-"` // anchor: NULL means no suggestions block
	SuggestionsSampleRate           *float64   `json:"-"`
	SuggestionsLookbackDays         *int       `json:"-"`
	SuggestionsMaxClusteredRequests *int       `json:"-"`
	SuggestionsMinClusterRequests   *int       `json:"-"`
	SuggestionsProvider             *string    `gorm:"type:varchar(255)" json:"-"`
	SuggestionsModel                *string    `gorm:"type:varchar(255)" json:"-"`
	SuggestionsInstructions         *string    `gorm:"type:text" json:"-"`
	SuggestionsScheduleEvery        *string    `gorm:"type:varchar(16)" json:"-"` // anchor: NULL means no schedule
	SuggestionsScheduleStartAt      *time.Time `json:"-"`                         // NULL takes the default first run
	SuggestionsTierClassifier       *string    `gorm:"type:varchar(16)" json:"-"` // anchor: NULL means no tier block
	SuggestionsTierProvider         *string    `gorm:"type:varchar(255)" json:"-"`
	SuggestionsTierModel            *string    `gorm:"type:varchar(255)" json:"-"`
	SuggestionsTierInstructions     *string    `gorm:"type:text" json:"-"`
	SuggestionsTierPrompt           *string    `gorm:"type:text" json:"-"`
	SuggestionsTierCriteriaJSON     *string    `gorm:"column:suggestions_tier_criteria_json;type:text" json:"-"` // JSON map[tier]criteria

	ConfigHashesJSON string `gorm:"column:config_hashes_json;type:text" json:"-"` // per-section hashes of the config.json entry last applied
	ConfigHash       string `gorm:"type:varchar(255)" json:"-"`                   // hash of that entry's name and description

	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"index;not null" json:"updated_at"`
}

// TableName pins the physical table name.
func (TableModelRouter) TableName() string { return "model_routers" }
