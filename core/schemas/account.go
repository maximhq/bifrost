// Package schemas defines the core schemas and types used by the Bifrost system.
package schemas

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/bytedance/sonic"
)

type KeyStatusType string

const (
	KeyStatusSuccess          KeyStatusType = "success"
	KeyStatusListModelsFailed KeyStatusType = "list_models_failed"
)

// WhiteList is a list of values that are allowed to be used.
// Semantics:
//   - "*" (alone) means all values are allowed.
//   - Empty list means nothing is allowed.
//   - Non-empty list (without "*") means only the listed values are allowed.
//
// This type is used generically for any field that needs whitelist behavior
// (e.g., allowed models, allowed tools).
type WhiteList []string

// Contains reports whether value is in the whitelist.
// Returns true if value is in the list.
func (wl WhiteList) Contains(value string) bool {
	return slices.ContainsFunc(wl, func(s string) bool {
		return strings.EqualFold(s, value)
	})
}

// IsAllowed reports whether value is in the whitelist.
// Returns true if value is in the list.
func (wl WhiteList) IsAllowed(value string) bool {
	return wl.IsUnrestricted() || wl.Contains(value)
}

// IsEmpty reports whether the whitelist has no entries.
func (wl WhiteList) IsEmpty() bool {
	return len(wl) == 0
}

// IsUnrestricted reports whether the whitelist contains only "*",
// meaning all values are allowed.
func (wl WhiteList) IsUnrestricted() bool {
	return len(wl) == 1 && wl[0] == "*"
}

// IsRestricted reports whether the whitelist contains entries other than "*",
// meaning only the listed values are allowed.
func (wl WhiteList) IsRestricted() bool {
	return !wl.IsUnrestricted()
}

// Validate checks that the whitelist is well-formed.
// Returns an error if "*" is present alongside other values, or if there are duplicate entries.
func (wl WhiteList) Validate() error {
	if wl.Contains("*") && len(wl) > 1 {
		return fmt.Errorf("wildcard '*' cannot be used with other values in the whitelist")
	}
	seen := make(map[string]struct{}, len(wl))
	for _, v := range wl {
		normalized := strings.ToLower(v)
		if _, ok := seen[normalized]; ok {
			return fmt.Errorf("duplicate value '%s' in whitelist", v)
		}
		seen[normalized] = struct{}{}
	}
	return nil
}

// BlackList is a list of values that are denied.
// Semantics:
//   - "*" (alone) means all values are blocked.
//   - Empty list means nothing is blocked.
//   - Non-empty list (without "*") means only the listed values are blocked.
type BlackList []string

func (bl BlackList) Contains(value string) bool {
	return slices.ContainsFunc(bl, func(s string) bool {
		return strings.EqualFold(s, value)
	})
}

// IsBlocked reports whether value is blocked.
func (bl BlackList) IsBlocked(value string) bool {
	return bl.IsBlockAll() || bl.Contains(value)
}

// IsEmpty reports whether the blacklist has no entries (nothing is blocked).
func (bl BlackList) IsEmpty() bool {
	return len(bl) == 0
}

// IsBlockAll reports whether the blacklist contains "*", meaning all values are blocked.
func (bl BlackList) IsBlockAll() bool {
	return len(bl) == 1 && bl[0] == "*"
}

// Validate checks that the blacklist is well-formed.
func (bl BlackList) Validate() error {
	if bl.Contains("*") && len(bl) > 1 {
		return fmt.Errorf("wildcard '*' cannot be used with other values in the blacklist")
	}
	seen := make(map[string]struct{}, len(bl))
	for _, v := range bl {
		normalized := strings.ToLower(v)
		if _, ok := seen[normalized]; ok {
			return fmt.Errorf("duplicate value '%s' in blacklist", v)
		}
		seen[normalized] = struct{}{}
	}
	return nil
}

// Key represents an API key and its associated configuration for a provider.
// It contains the key value, supported models, and a weight for load balancing.
type Key struct {
	ID                     string                  `json:"id"`                                  // The unique identifier for the key (used by bifrost to identify the key)
	Name                   string                  `json:"name"`                                // The name of the key (used by users to identify the key, not used by bifrost)
	Value                  SecretVar               `json:"value"`                               // The actual API key value
	Models                 WhiteList               `json:"models"`                              // List of models this key can access
	BlacklistedModels      BlackList               `json:"blacklisted_models"`                  // List of models this key cannot access
	Weight                 float64                 `json:"weight"`                              // Weight for load balancing between multiple keys
	Aliases                KeyAliases              `json:"aliases,omitempty"`                   // Mapping of model identifiers to inference profiles
	AzureKeyConfig         *AzureKeyConfig         `json:"azure_key_config,omitempty"`          // Azure-specific key configuration
	VertexKeyConfig        *VertexKeyConfig        `json:"vertex_key_config,omitempty"`         // Vertex-specific key configuration
	BedrockKeyConfig       *BedrockKeyConfig       `json:"bedrock_key_config,omitempty"`        // AWS Bedrock-specific key configuration
	BedrockMantleKeyConfig *BedrockMantleKeyConfig `json:"bedrock_mantle_key_config,omitempty"` // Bedrock Mantle-specific key configuration
	VLLMKeyConfig          *VLLMKeyConfig          `json:"vllm_key_config,omitempty"`           // vLLM-specific key configuration
	ReplicateKeyConfig     *ReplicateKeyConfig     `json:"replicate_key_config,omitempty"`      // Replicate-specific key configuration
	OllamaKeyConfig        *OllamaKeyConfig        `json:"ollama_key_config,omitempty"`         // Ollama-specific key configuration
	SGLKeyConfig           *SGLKeyConfig           `json:"sgl_key_config,omitempty"`            // SGLang-specific key configuration
	Enabled                *bool                   `json:"enabled,omitempty"`                   // Whether the key is active (default:true)
	UseForBatchAPI         *bool                   `json:"use_for_batch_api,omitempty"`         // Whether this key can be used for batch API operations (default:false for new keys, migrated keys default to true)
	UseAnthropicEndpoints  *bool                   `json:"use_anthropic_endpoints,omitempty"`   // Whether to use anthropic endpoints for this key
	ConfigHash             string                  `json:"config_hash,omitempty"`               // Hash of config.json version, used for change detection
	Status                 KeyStatusType           `json:"status,omitempty"`                    // Status of key
	Description            string                  `json:"description,omitempty"`               // Description of key
}

// ModelFamily is a typed enum identifying the underlying model family of an alias target.
// It enables provider routing decisions (request shape, response parsing, auth headers,
// URL construction) without substring-sniffing the wire model ID.
type ModelFamily string

const (
	ModelFamilyAnthropic ModelFamily = "anthropic"
	ModelFamilyOpenAI    ModelFamily = "openai"
	ModelFamilyMistral   ModelFamily = "mistral"
	ModelFamilyCohere    ModelFamily = "cohere"
	ModelFamilyGemini    ModelFamily = "gemini"
	ModelFamilyGemma     ModelFamily = "gemma"
	ModelFamilyLlama     ModelFamily = "llama"
	ModelFamilyImagen    ModelFamily = "imagen"
	ModelFamilyVeo       ModelFamily = "veo"
	ModelFamilyNova      ModelFamily = "nova"
	ModelFamilyTitan     ModelFamily = "titan"
)

// IsValid reports whether mf is a recognized model family.
func (mf *ModelFamily) IsValid() bool {
	if mf == nil {
		return false
	}
	switch *mf {
	case ModelFamilyAnthropic, ModelFamilyOpenAI, ModelFamilyMistral,
		ModelFamilyCohere, ModelFamilyGemini, ModelFamilyGemma,
		ModelFamilyLlama, ModelFamilyImagen, ModelFamilyVeo,
		ModelFamilyNova, ModelFamilyTitan:
		return true
	}
	return false
}

// AzureAliasCfg holds Azure-specific overrides that apply to a single alias.
// Each field, when non-nil, overrides the corresponding key-level default.
type AzureAliasCfg struct {
	APIVersion       *string    `json:"api_version,omitempty"`       // overrides the Azure OpenAI api-version query param for this alias
	AnthropicVersion *string    `json:"anthropic_version,omitempty"` // overrides the anthropic-version header for Claude-on-Azure deployments
	Endpoint         *SecretVar `json:"endpoint,omitempty"`          // overrides AzureKeyConfig.Endpoint for this alias — lets one credential span deployments on multiple Azure resources
}

// VertexAliasCfg holds Vertex-specific overrides that apply to a single alias.
//
// Deprecated for ProjectID: the per-alias project override now lives on the
// shared top-level AliasConfig.ProjectID field (see below), so one alias key can
// scope any provider (Vertex, Bedrock, Bedrock Mantle) to a project without a
// JSON field-name collision between the embedded sub-configs. ProjectID is kept
// here only so Go code that constructs VertexAliasCfg directly keeps compiling;
// the Vertex resolver reads AliasConfig.ProjectID first and falls back to this.
type VertexAliasCfg struct {
	ProjectID         *SecretVar `json:"-"` // superseded by AliasConfig.ProjectID; not (de)serialized to avoid colliding with the top-level project_id
	ProjectNumber     *SecretVar `json:"project_number,omitempty"`
	ForceSingleRegion *bool      `json:"force_single_region,omitempty"`
}

// BedrockAliasCfg holds Bedrock-specific overrides that apply to a single alias.
type BedrockAliasCfg struct {
	InferenceProfileARN *SecretVar `json:"inference_profile_arn,omitempty"`
}

// ReplicateAliasCfg holds Replicate-specific overrides that apply to a single alias.
type ReplicateAliasCfg struct {
	UseDeploymentsEndpoint *bool `json:"use_deployments_endpoint,omitempty"`
}

// AliasConfig is the rich value type held by KeyAliases. It carries everything
// needed to call a provider for an aliased model: the wire model identifier
// (ModelID), the canonical model name used for pricing/logging (ModelName), the
// family used for provider routing decisions (ModelFamily), and optional
// provider-specific overrides that override the key-level defaults.
type AliasConfig struct {
	ModelID     string       `json:"model_id"`               // wire model identifier sent to the provider
	ModelName   *string      `json:"model_name,omitempty"`   // canonical model name used for pricing, logging, and 2nd-tier family routing
	ModelFamily *ModelFamily `json:"model_family,omitempty"` // 1st-tier family routing enum
	Description string       `json:"description,omitempty"`  // description of the alias for users to understand its purpose (not used by bifrost)
	Region      *SecretVar   `json:"region,omitempty"`
	// ProjectID is a per-alias project override shared across providers (like Region).
	// Vertex uses it as the GCP project; Bedrock and Bedrock Mantle use it as the
	// AWS project sent via the OpenAI-Project / anthropic-workspace-id header. Kept
	// top-level (rather than inside each provider sub-config) so the flat "project_id"
	// JSON key does not collide between embedded sub-configs — Go/sonic silently drop
	// a field name shared by multiple same-depth anonymous structs.
	ProjectID             *SecretVar `json:"project_id,omitempty"`
	UseAnthropicEndpoints *bool      `json:"use_anthropic_endpoints,omitempty"` // Whether to use anthropic endpoints for this alias

	*AzureAliasCfg
	*VertexAliasCfg
	*BedrockAliasCfg
	*ReplicateAliasCfg
}

// isLegacyShape reports whether this AliasConfig carries only ModelID and no
// other fields. Used by MarshalJSON to emit the legacy string-valued wire
// shape so older consumers that expect map[string]string keep working.
func (ac AliasConfig) isLegacyShape() bool {
	return ac.ModelID != "" &&
		ac.ModelName == nil &&
		ac.ModelFamily == nil &&
		ac.Description == "" &&
		ac.Region == nil &&
		ac.ProjectID == nil &&
		ac.UseAnthropicEndpoints == nil &&
		ac.AzureAliasCfg == nil &&
		ac.VertexAliasCfg == nil &&
		ac.BedrockAliasCfg == nil &&
		ac.ReplicateAliasCfg == nil
}

// MarshalJSON emits the legacy string wire shape when only ModelID is set, so
// callers that haven't opted into the rich AliasConfig see no observable
// change on the wire. When any other field is populated, the full object is
// emitted.
func (ac AliasConfig) MarshalJSON() ([]byte, error) {
	// The deprecated VertexAliasCfg.ProjectID is json:"-" (it would otherwise collide
	// with the top-level project_id on the wire). Promote it to the shared top-level
	// ProjectID before serializing so Go-constructed aliases that set only the legacy
	// field don't lose their project on a save/export round-trip (or drop it from the
	// config hash). Value receiver: this mutates only the local copy, not the caller's.
	if ac.ProjectID == nil && ac.VertexAliasCfg != nil && ac.VertexAliasCfg.ProjectID != nil {
		ac.ProjectID = ac.VertexAliasCfg.ProjectID
	}
	if ac.isLegacyShape() {
		return Marshal(ac.ModelID)
	}
	type aliasConfigJSON AliasConfig
	return Marshal(aliasConfigJSON(ac))
}

// KeyAliases maps a user-facing model name to its AliasConfig.
//
// Both the input (UnmarshalJSON) and the output (AliasConfig.MarshalJSON)
// transparently accept and emit two JSON wire shapes:
//   - Legacy: {"my-model": "provider-model-id"}                              — value is a string
//   - New:    {"my-model": {"model_id": "provider-model-id", ... }}          — value is an object
//
// Legacy entries deserialize to AliasConfig{ModelID: <string>}; an AliasConfig
// that only has ModelID set serializes back to a plain string. This keeps the
// wire format byte-for-byte compatible with the pre-refactor flow until
// ModelName / ModelFamily / provider sub-configs are populated explicitly.
type KeyAliases map[string]AliasConfig

// Validate checks that every entry in the alias map is well-formed and that
// any provider-specific sub-configs (AzureAliasCfg, VertexAliasCfg,
// BedrockAliasCfg, ReplicateAliasCfg) are only set when the owning Key
// actually belongs to that provider. Catches misconfigurations like an
// AzureAliasCfg attached to a Bedrock key.
//
// providerKey is the provider this Key is registered under (e.g. schemas.Azure
// for keys in the azure provider config).
func (ka KeyAliases) Validate(providerKey ModelProvider) error {
	seen := make(map[string]struct{}, len(ka))
	for from, ac := range ka {
		if strings.TrimSpace(from) == "" {
			return fmt.Errorf("alias source cannot be empty")
		}
		if strings.TrimSpace(ac.ModelID) == "" {
			return fmt.Errorf("alias %q: model_id cannot be empty", from)
		}
		if strings.TrimSpace(from) != from {
			return fmt.Errorf("alias source %q cannot have leading or trailing whitespace", from)
		}
		if strings.TrimSpace(ac.ModelID) != ac.ModelID {
			return fmt.Errorf("alias %q: model_id cannot have leading or trailing whitespace", from)
		}
		if ac.ModelName != nil && strings.TrimSpace(*ac.ModelName) != *ac.ModelName {
			return fmt.Errorf("alias %q: model_name cannot have leading or trailing whitespace", from)
		}
		if ac.ModelFamily != nil && !ac.ModelFamily.IsValid() {
			return fmt.Errorf("alias %q: invalid model_family %q", from, *ac.ModelFamily)
		}
		if ac.AzureAliasCfg != nil && providerKey != Azure {
			return fmt.Errorf("alias %q: azure sub-config is only valid on Azure keys (got provider %q)", from, providerKey)
		}
		if ac.VertexAliasCfg != nil && providerKey != Vertex {
			return fmt.Errorf("alias %q: vertex sub-config is only valid on Vertex keys (got provider %q)", from, providerKey)
		}
		if ac.BedrockAliasCfg != nil && providerKey != Bedrock {
			return fmt.Errorf("alias %q: bedrock sub-config is only valid on Bedrock keys (got provider %q)", from, providerKey)
		}
		if ac.ReplicateAliasCfg != nil && providerKey != Replicate {
			return fmt.Errorf("alias %q: replicate sub-config is only valid on Replicate keys (got provider %q)", from, providerKey)
		}
		normalized := strings.ToLower(from)
		if _, ok := seen[normalized]; ok {
			return fmt.Errorf("duplicate alias source %q (case-insensitive)", from)
		}
		seen[normalized] = struct{}{}
	}
	return nil
}

// Resolve returns the wire model identifier for the given user-facing model name.
// If no alias matches, the input is returned unchanged. Case-insensitive fallback
// matches the prior behavior.
//
// This signature is preserved for backward compatibility with existing callers
// that only need the wire model string. For access to the full AliasConfig
// (ModelName, ModelFamily, provider overrides), use ResolveConfig.
func (ka KeyAliases) Resolve(model string) string {
	if ac := ka.ResolveConfig(model); ac != nil {
		return ac.ModelID
	}
	return model
}

// ResolvedAlias is what core stashes in BifrostContext after key-level alias
// resolution. Key is the user-facing model name the client sent (LHS of the
// alias map). Config is the matched AliasConfig.
//
// Carrying the alias key alongside the config lets providers consult it as
// the lowest-precedence tier for family detection — common case: an admin
// names their alias "best-claude" but the wire ModelID is an opaque Azure
// deployment ID, so neither the config fields nor request.Model carry the
// "claude" substring; the alias key does.
type ResolvedAlias struct {
	Key    string
	Config *AliasConfig
}

// GetResolvedAlias returns the ResolvedAlias that core stashed in ctx after
// key-level alias resolution, or nil if no alias matched or ctx is nil.
//
// This is set by bifrost.go alongside req.SetModel(resolved). Plugins must
// not write to this key directly.
func GetResolvedAlias(ctx *BifrostContext) *ResolvedAlias {
	if ctx == nil {
		return nil
	}
	v := ctx.Value(BifrostContextKeyResolvedAlias)
	if v == nil {
		return nil
	}
	ra, _ := v.(*ResolvedAlias)
	return ra
}

// ResolveFamily returns the model family for the current attempt, walking
// the precedence: explicit alias ModelFamily → alias ModelName → alias
// ModelID → alias Key. When no alias matched, falls back to substring
// matching against fallbackModel (typically request.Model), preserving
// pre-refactor behavior.
//
// Returns an empty ModelFamily if nothing matches.
func ResolveFamily(ctx *BifrostContext, fallbackModel string) ModelFamily {
	ra := GetResolvedAlias(ctx)
	var candidates []string
	if ra != nil && ra.Config != nil {
		if ra.Config.ModelFamily != nil && *ra.Config.ModelFamily != "" {
			return *ra.Config.ModelFamily
		}
		if ra.Config.ModelName != nil {
			candidates = append(candidates, *ra.Config.ModelName)
		}
		candidates = append(candidates, ra.Config.ModelID, ra.Key)
	} else {
		candidates = append(candidates, fallbackModel)
	}
	for _, s := range candidates {
		switch {
		case IsAnthropicModel(s):
			return ModelFamilyAnthropic
		case IsOpenAIModel(s):
			return ModelFamilyOpenAI
		case IsMistralModel(s):
			return ModelFamilyMistral
		// Imagen and Veo are checked before Gemini as a defensive ordering:
		// they are distinct Google model families whose names do not contain
		// "gemini", so they could never be mis-classified here, but keeping
		// them first makes the intent explicit.
		case IsImagenModel(s):
			return ModelFamilyImagen
		case IsVeoModel(s):
			return ModelFamilyVeo
		case IsGeminiModel(s):
			return ModelFamilyGemini
		case IsGemmaModel(s):
			return ModelFamilyGemma
		case IsLlamaModel(s):
			return ModelFamilyLlama
		case IsNovaModel(s):
			return ModelFamilyNova
		case IsTitanModel(s):
			return ModelFamilyTitan
		case IsCohereModel(s):
			return ModelFamilyCohere
		}
	}
	return ""
}

// ResolveCanonicalModel returns the model string that capability/version gating
// should run against for the current attempt, walking the alias hierarchy:
// canonical ModelName → wire ModelID → fallbackModel.
//
// Precedence here means "first present tier wins" — a present ModelName is
// authoritative and we do not fall through to ModelID on it. The ModelName tier
// is what makes Claude-on-Azure work: there the wire ModelID is an opaque
// deployment id, but the admin-configured ModelName carries the real
// "claude-opus-4-8" string the substring checks need.
//
// When no alias is resolved in ctx, fallbackModel (typically request.Model) is
// returned unchanged, preserving pre-refactor behavior.
func ResolveCanonicalModel(ctx *BifrostContext, fallbackModel string) string {
	if ra := GetResolvedAlias(ctx); ra != nil && ra.Config != nil {
		if ra.Config.ModelName != nil && *ra.Config.ModelName != "" {
			return *ra.Config.ModelName
		}
		if ra.Config.ModelID != "" {
			return ra.Config.ModelID
		}
	}
	return fallbackModel
}

// IsAnthropicModelFamily reports whether the current attempt resolves to the
// Anthropic model family. Thin wrapper over ResolveFamily so provider code
// reads uniformly at the many call sites that branch on Anthropic vs
// non-Anthropic (request shape, response parsing, anthropic-version header,
// URL path construction). model is passed as the substring-match fallback
// used when no alias is resolved in ctx — typically request.Model.
func IsAnthropicModelFamily(ctx *BifrostContext, model string) bool {
	return ResolveFamily(ctx, model) == ModelFamilyAnthropic
}

func IsOpenAIModelFamily(ctx *BifrostContext, model string) bool {
	return ResolveFamily(ctx, model) == ModelFamilyOpenAI
}

// IsElevenlabsSoundModelFamily reports whether the current attempt resolves to
// an ElevenLabs sound-effects (text-to-sound) model. It honors aliases by
// resolving the canonical model name first, so an alias whose ModelName/ModelID
// is a sound model is detected the same as a raw model id. See
// IsAnthropicModelFamily for usage notes.
func IsElevenlabsSoundModelFamily(ctx *BifrostContext, model string) bool {
	return IsElevenlabsSoundModel(ResolveCanonicalModel(ctx, model))
}

// IsMistralModelFamily reports whether the current attempt resolves to the
// Mistral model family. See IsAnthropicModelFamily for usage notes.
func IsMistralModelFamily(ctx *BifrostContext, model string) bool {
	return ResolveFamily(ctx, model) == ModelFamilyMistral
}

// IsLlamaModelFamily reports whether the current attempt resolves to the
// Llama model family. Used by Bedrock to gate tool_choice handling — AWS
// Bedrock Converse rejects toolConfig.toolChoice.tool on Meta Llama variants.
func IsLlamaModelFamily(ctx *BifrostContext, model string) bool {
	return ResolveFamily(ctx, model) == ModelFamilyLlama
}

// IsNovaModelFamily reports whether the current attempt resolves to the
// Amazon Nova model family. Used by Bedrock to gate cache-point insertion
// and tool shaping that differs from Anthropic.
func IsNovaModelFamily(ctx *BifrostContext, model string) bool {
	return ResolveFamily(ctx, model) == ModelFamilyNova
}

// IsCohereModelFamily reports whether the current attempt resolves to the
// Cohere model family. Used by Bedrock to pick the Cohere request/response
// shape for embeddings (vs. the Titan envelope).
func IsCohereModelFamily(ctx *BifrostContext, model string) bool {
	return ResolveFamily(ctx, model) == ModelFamilyCohere
}

// IsTitanModelFamily reports whether the current attempt resolves to the
// Amazon Titan model family. Used by Bedrock to pick the Titan embedding
// request/response envelope.
func IsTitanModelFamily(ctx *BifrostContext, model string) bool {
	return ResolveFamily(ctx, model) == ModelFamilyTitan
}

// IsGeminiModelFamily reports whether the current attempt resolves to the
// Google Gemini model family. Used by Vertex to pick Gemini-shaped request
// transforms and the publishers/google URL prefix.
func IsGeminiModelFamily(ctx *BifrostContext, model string) bool {
	return ResolveFamily(ctx, model) == ModelFamilyGemini
}

// IsGemmaModelFamily reports whether the current attempt resolves to the
// Gemma model family. Vertex routes Gemma via the publishers/google path
// alongside Gemini.
func IsGemmaModelFamily(ctx *BifrostContext, model string) bool {
	return ResolveFamily(ctx, model) == ModelFamilyGemma
}

// IsImagenModelFamily reports whether the current attempt resolves to the
// Imagen model family. Used by Vertex for the :predict endpoint and Imagen-
// specific request shaping.
func IsImagenModelFamily(ctx *BifrostContext, model string) bool {
	return ResolveFamily(ctx, model) == ModelFamilyImagen
}

// IsVeoModelFamily reports whether the current attempt resolves to the Veo
// model family. Used by Vertex for video-generation request shaping.
func IsVeoModelFamily(ctx *BifrostContext, model string) bool {
	return ResolveFamily(ctx, model) == ModelFamilyVeo
}

// BuildRoutingInfo constructs a RoutingInfo for the current attempt from this
// attempt's chosen provider/model/key and the resolved alias stashed in ctx.
//
// Populates only the per-attempt fields (Provider, Model, Key,
// ResolvedKeyAlias). IsFallback and PrimaryProvider/PrimaryModel are layered
// on later by the orchestrator (handleRequest / handleStreamRequest) via
// SetFallbackRoutingInfo on the final response/error, since those signals
// belong to the orchestrator scope rather than the per-attempt one.
//
// ResolvedKeyAlias.ModelFamily reflects the family explicitly configured on
// the alias (nil when the admin didn't set one) — not the substring-resolved
// family used for routing.
func BuildRoutingInfo(ctx *BifrostContext, attemptProvider ModelProvider, attemptModel string, attemptKey Key) RoutingInfo {
	info := RoutingInfo{
		Provider: attemptProvider,
		Model:    attemptModel,
		Key:      attemptKey.Name,
	}
	if ra := GetResolvedAlias(ctx); ra != nil && ra.Config != nil {
		rka := &ResolvedKeyAlias{
			ModelID: ra.Config.ModelID,
		}
		if ra.Config.ModelName != nil {
			mn := *ra.Config.ModelName
			rka.ModelName = &mn
		}
		if ra.Config.ModelFamily != nil {
			f := *ra.Config.ModelFamily
			rka.ModelFamily = &f
		}
		info.ResolvedKeyAlias = rka
	}
	return info
}

// ResolveConfig returns the AliasConfig for the given user-facing model name,
// or nil if no alias matches. Case-insensitive fallback matches Resolve.
func (ka KeyAliases) ResolveConfig(model string) *AliasConfig {
	if ka == nil {
		return nil
	}
	if ac, ok := ka[model]; ok {
		return &ac
	}
	for k, v := range ka {
		if strings.EqualFold(k, model) {
			return &v
		}
	}
	return nil
}

// UnmarshalJSON accepts both the legacy {"k":"v"} and new {"k":{...}} wire
// shapes for KeyAliases. Legacy string values are promoted to
// AliasConfig{ModelID: <string>}.
func (ka *KeyAliases) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		*ka = nil
		return nil
	}
	var raw map[string]json.RawMessage
	if err := sonic.Unmarshal(data, &raw); err != nil {
		return err
	}
	result := make(KeyAliases, len(raw))
	for k, entry := range raw {
		entryTrim := bytes.TrimSpace(entry)
		if len(entryTrim) == 0 {
			return fmt.Errorf("alias %q: empty value", k)
		}
		switch entryTrim[0] {
		case '"':
			// Legacy string value — promote to AliasConfig{ModelID: ...}.
			var modelID string
			if err := sonic.Unmarshal(entry, &modelID); err != nil {
				return fmt.Errorf("alias %q: %w", k, err)
			}
			result[k] = AliasConfig{ModelID: modelID}
		case '{':
			var ac AliasConfig
			if err := sonic.Unmarshal(entry, &ac); err != nil {
				return fmt.Errorf("alias %q: %w", k, err)
			}
			result[k] = ac
		default:
			return fmt.Errorf("alias %q: value must be a string (legacy) or object", k)
		}
	}
	*ka = result
	return nil
}

type AzureAuthType string

const (
	AzureAuthTypeClientSecret    AzureAuthType = "client_secret"
	AzureAuthTypeManagedIdentity AzureAuthType = "managed_identity"
)

// AzureKeyConfig represents the Azure-specific configuration.
// It contains Azure-specific settings required for service access and deployment management.
type AzureKeyConfig struct {
	Endpoint SecretVar `json:"endpoint"` // Azure service endpoint URL

	ClientID     *SecretVar `json:"client_id,omitempty"`     // Azure client ID for authentication
	ClientSecret *SecretVar `json:"client_secret,omitempty"` // Azure client secret for authentication
	TenantID     *SecretVar `json:"tenant_id,omitempty"`     // Azure tenant ID for authentication
	Scopes       []string   `json:"scopes,omitempty"`
}

// VertexKeyConfig represents the Vertex-specific configuration.
// It contains Vertex-specific settings required for authentication and service access.
type VertexKeyConfig struct {
	ProjectID       SecretVar `json:"project_id"`
	ProjectNumber   SecretVar `json:"project_number"`
	Region          SecretVar `json:"region"`
	AuthCredentials SecretVar `json:"auth_credentials"`
	// ForceSingleRegion pins requests to the configured region and disables automatic promotion of
	// multi-region-only models to a multi-region pool endpoint (e.g. for provisioned throughput).
	ForceSingleRegion bool `json:"force_single_region,omitempty"`
}

// NOTE: To use Vertex IAM role authentication, set AuthCredentials to empty string.

// S3BucketConfig represents a single S3 bucket configuration for batch operations.
type S3BucketConfig struct {
	BucketName string `json:"bucket_name"`          // S3 bucket name
	Prefix     string `json:"prefix,omitempty"`     // S3 key prefix for batch files
	IsDefault  bool   `json:"is_default,omitempty"` // Whether this is the default bucket for batch operations
}

// BatchS3Config holds S3 bucket configurations for Bedrock batch operations.
// Supports multiple buckets to allow flexible batch job routing.
type BatchS3Config struct {
	Buckets []S3BucketConfig `json:"buckets,omitempty"` // List of S3 bucket configurations
}

// BedrockKeyConfig represents the AWS Bedrock-specific configuration.
// It contains AWS-specific settings required for authentication and service access.
type BedrockKeyConfig struct {
	AccessKey    SecretVar  `json:"access_key,omitempty"`    // AWS access key for authentication
	SecretKey    SecretVar  `json:"secret_key,omitempty"`    // AWS secret access key for authentication
	SessionToken *SecretVar `json:"session_token,omitempty"` // AWS session token for temporary credentials
	Region       *SecretVar `json:"region,omitempty"`        // AWS region for service access
	ARN          *SecretVar `json:"arn,omitempty"`           // Amazon Resource Name for resource identification
	// IAM role for STS AssumeRole
	RoleARN         *SecretVar `json:"role_arn,omitempty"`
	ExternalID      *SecretVar `json:"external_id,omitempty"`
	RoleSessionName *SecretVar `json:"session_name,omitempty"`

	// ProjectID scopes the Bedrock Mantle sub-surface (OpenAI-compatible gpt-*/Gemma routing and the
	// mantle catalog merge in ListModels) to a specific Bedrock project via the "OpenAI-Project"
	// header. When empty, AWS routes to the account's default project. It has no effect on the
	// Converse/bedrock-runtime paths, which are not project-scoped.
	ProjectID *SecretVar `json:"project_id,omitempty"`

	BatchS3Config *BatchS3Config `json:"batch_s3_config,omitempty"` // S3 bucket configuration for batch operations
}

// NOTE: To use Bedrock IAM role authentication, set both AccessKey and SecretKey to empty strings.
// To use Bedrock API Key authentication, set Value in Key struct instead.

// BedrockMantleKeyConfig represents the Bedrock Mantle-specific configuration. Mantle serves
// Claude (native-Anthropic Messages), OpenAI-compatible, and Gemma models on the
// bedrock-mantle.{region}.api.aws host. It carries only the credentials and region the mantle
// endpoints use; it intentionally omits the inference-profile ARN and batch S3 config, which
// apply only to the Converse/bedrock-runtime surface.
type BedrockMantleKeyConfig struct {
	AccessKey    SecretVar  `json:"access_key,omitempty"`    // AWS access key for SigV4 authentication
	SecretKey    SecretVar  `json:"secret_key,omitempty"`    // AWS secret access key for SigV4 authentication
	SessionToken *SecretVar `json:"session_token,omitempty"` // AWS session token for temporary credentials
	Region       *SecretVar `json:"region,omitempty"`        // AWS region used to build the bedrock-mantle endpoint host
	// IAM role for STS AssumeRole
	RoleARN         *SecretVar `json:"role_arn,omitempty"`
	ExternalID      *SecretVar `json:"external_id,omitempty"`
	RoleSessionName *SecretVar `json:"session_name,omitempty"`

	// ProjectID scopes inference and model listing to a specific Bedrock project. It is sent as the
	// "OpenAI-Project" header on the OpenAI-compatible surface and the "anthropic-workspace-id"
	// header on the native-Anthropic (Claude) surface. When empty, AWS routes to the account's
	// default project.
	ProjectID *SecretVar `json:"project_id,omitempty"`
}

// NOTE: To use Bedrock Mantle IAM role authentication, set both AccessKey and SecretKey to empty
// strings. To use Bedrock Mantle API Key authentication, set Value in the Key struct instead.

// VLLMKeyConfig represents the vLLM-specific key configuration.
// It allows each key to target a different vLLM server URL and model name,
// enabling per-key routing and round-robin load balancing across multiple vLLM instances.
type VLLMKeyConfig struct {
	URL       SecretVar `json:"url"`        // VLLM server base URL (required, supports env. prefix)
	ModelName string    `json:"model_name"` // Exact model name served on this VLLM instance (used for key selection)
}

// ReplicateKeyConfig represents the Replicate-specific key configuration.
// It contains Replicate-specific settings required for authentication and service access.
type ReplicateKeyConfig struct {
	UseDeploymentsEndpoint bool `json:"use_deployments_endpoint"` // Whether to use the deployments endpoint instead of the models endpoint
}

// OllamaKeyConfig represents the Ollama-specific key configuration.
// It allows each key to target a different Ollama server URL,
// enabling per-key routing and round-robin load balancing across multiple Ollama instances.
type OllamaKeyConfig struct {
	URL SecretVar `json:"url"` // Ollama server base URL (required, supports env. prefix)
}

// SGLKeyConfig represents the SGLang-specific key configuration.
// It allows each key to target a different SGLang server URL,
// enabling per-key routing and round-robin load balancing across multiple SGLang instances.
type SGLKeyConfig struct {
	URL SecretVar `json:"url"` // SGLang server base URL (required, supports env. prefix)
}

// Account defines the interface for managing provider accounts and their configurations.
// It provides methods to access provider-specific settings, API keys, and configurations.
type Account interface {
	// GetConfiguredProviders returns a list of providers that are configured
	// in the account. This is used to determine which providers are available for use.
	GetConfiguredProviders() ([]ModelProvider, error)

	// GetKeysForProvider returns the API keys configured for a specific provider.
	// The keys include their values, supported models, and weights for load balancing.
	// The context can carry data from any source that sets values before the Bifrost request,
	// including but not limited to plugin pre-hooks, application logic, or any in app middleware sharing the context.
	// This enables dynamic key selection based on any context values present during the request.
	GetKeysForProvider(ctx context.Context, providerKey ModelProvider) ([]Key, error)

	// GetConfigForProvider returns the configuration for a specific provider.
	// This includes network settings, authentication details, and other provider-specific
	// configurations.
	GetConfigForProvider(providerKey ModelProvider) (*ProviderConfig, error)
}
