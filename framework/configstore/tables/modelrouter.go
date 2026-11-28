package tables

import "time"

// Modes a model router runs in.
const (
	ModelRouterModeClassifier  = "classifier"  // classifies the request and routes it by tier
	ModelRouterModeConditional = "conditional" // routes every request its condition takes to one target list
)

// What a classifier router does with a request it has no target for: it
// produced no tier, or the tier has no target.
const (
	ModelRouterEmptyTargetOriginalModel = "original_model" // keeps the request's model and ends the walk
	ModelRouterEmptyTargetNextRouter    = "next_router"    // passes the request to the next router
)

// NormalizeModelRouterMode reads a stored or submitted mode, taking an empty
// one as classifier: routers saved before modes existed are classifiers.
func NormalizeModelRouterMode(mode string) string {
	if mode == "" {
		return ModelRouterModeClassifier
	}
	return mode
}

// ModelRouterMeta is what the gateway records about a router for itself. It is
// written once, by the upgrade that turned routing rules into routers, and no
// API or config.json entry sets it.
type ModelRouterMeta struct {
	// RoutingRuleIDs are the routing rules the router was made from: logs a
	// rule recorded before the upgrade are listed under the router, and routers
	// made from one rule fire once in a chain, as the rule did.
	RoutingRuleIDs []string `json:"routing_rule_ids,omitempty"`
}

// TableModelRouter stores one model router and the routing that decides which
// requests it takes; its targets live in model_router_targets. A classifier
// router is a named complexity classifier with its own phrases and settings;
// a conditional router uses none of them and keeps their defaults. One column
// per setting (lists and maps as JSON text). Each optional block (semantic,
// decision, llm, session, suggestions) is present exactly when its anchor
// column is non-NULL. No classifier column has a gorm default, since zero is
// often a real setting; defaults come from the config's normalization. The
// configstore package converts to and from the config type.
//
// Name carries no unique index: the store refuses a name another router in
// the same scope holds when a router is created or renamed, and nothing else
// compares names.
type TableModelRouter struct {
	ID          string           `gorm:"primaryKey;type:varchar(255)" json:"id"`
	Name        string           `gorm:"type:varchar(255);not null" json:"name"`
	Description string           `gorm:"type:text" json:"description"`
	Mode        string           `gorm:"type:varchar(20);not null;default:'classifier'" json:"mode"` // "classifier" or "conditional"; set when the router is created
	Meta        *ModelRouterMeta `gorm:"type:text;serializer:json" json:"-"`                         // set once by the upgrade that made the router from routing rules; NULL otherwise
	Classifier  string           `gorm:"type:varchar(20);not null" json:"classifier"`                // "semantic", "decision" or "llm"

	// Routing: which requests the router takes, and what the walk does after
	// it. These columns do carry defaults, each the value a router without
	// routing gets, so they can be added to existing rows.
	Enabled       bool    `gorm:"not null;default:false" json:"-"`
	Scope         string  `gorm:"type:varchar(50);not null;default:'global'" json:"-"` // "global" | "team" | "customer" | "virtual_key" | "user"
	ScopeID       *string `gorm:"type:varchar(255)" json:"-"`                          // nil for global, otherwise the entity's id
	CelExpression string  `gorm:"type:text" json:"-"`                                  // empty takes every request in scope
	Query         *string `gorm:"type:text" json:"-"`                                  // the dashboard's condition builder state, JSON; never evaluated
	Priority      int     `gorm:"not null;default:0" json:"-"`                         // lower is tried first within a scope
	ChainRouter   bool    `gorm:"not null;default:false" json:"-"`                     // the routers are walked again after this one applies a target
	EmptyTarget   string  `gorm:"type:varchar(20);not null;default:''" json:"-"`       // classifier routers: "next_router", or empty for original_model

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
