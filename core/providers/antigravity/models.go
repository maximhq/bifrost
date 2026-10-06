package antigravity

import (
	"sort"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// modelInfo describes a model the Antigravity picker offers.
type modelInfo struct {
	ID            string
	DisplayName   string
	ContextWindow int
	Image         bool // accepts image input
	OutputImage   bool // generates images
}

// staticModels are the Antigravity picker models, used when live discovery is unavailable.
var staticModels = []modelInfo{
	{ID: "gemini-3.8-flash", DisplayName: "Gemini 3.8 Flash", ContextWindow: 1_048_576, Image: true},
	{ID: "gemini-3.7-flash", DisplayName: "Gemini 3.7 Flash", ContextWindow: 1_048_576, Image: true},
	{ID: "gemini-3.1-pro", DisplayName: "Gemini 3.1 Pro", ContextWindow: 1_048_576, Image: true},
	{ID: "gemini-3.1-flash-image", DisplayName: "Gemini 3.1 Flash Image", ContextWindow: 1_048_576, Image: true, OutputImage: true},
	{ID: "claude-sonnet-4-6", DisplayName: "Claude Sonnet 4.6", ContextWindow: 250_000, Image: true},
	{ID: "claude-opus-4-6-thinking", DisplayName: "Claude Opus 4.6 (Thinking)", ContextWindow: 250_000, Image: true},
	{ID: "gpt-oss-120b-medium", DisplayName: "GPT-OSS 120B (Medium)", ContextWindow: 131_072},
}

const imageModelID = "gemini-3.1-flash-image"

// retiredFlashModels route retired Flash ids to the tiered Flash model with a fixed
// thinking level.
var retiredFlashModels = map[string]string{
	"gemini-3.6-flash":           "medium",
	"gemini-3.6-flash-low":       "low",
	"gemini-3.6-flash-medium":    "medium",
	"gemini-3.6-flash-high":      "high",
	"gemini-3.5-flash-extra-low": "low",
	"gemini-3.5-flash-low":       "medium",
	"gemini-3.5-flash-mid":       "medium",
	"gemini-3.5-flash-high":      "high",
	"gemini-3-flash-agent":       "high",
}

// passthroughModels are wire or alias ids that already pin a variant, so no thinking
// level is added.
var passthroughModels = map[string]string{
	"gemini-3.1-pro-high":     "gemini-pro-agent",
	"gemini-3.1-pro-preview":  "gemini-pro-agent",
	"gemini-3.1-pro-low":      "gemini-3.1-pro-low",
	"gemini-pro-agent":        "gemini-pro-agent",
	"gemini-3.7-flash-tiered": "gemini-3.7-flash-tiered",
	"gemini-3.8-flash-low":    "gemini-3.8-flash-low",
	"gemini-3.8-flash-medium": "gemini-3.8-flash-medium",
	"gemini-3.8-flash-high":   "gemini-3.8-flash-high",
}

// resolveThinkingLevel maps a requested effort onto the thinkingLevel values Cloud Code
// Assist accepts. "minimal" is deliberately not one of them: Google documents it as an
// error for the current Gemini generation, so it, "none" and anything unknown resolve to
// "" and the caller falls back to the model's default.
func resolveThinkingLevel(effort string) string {
	switch effort {
	case "xhigh", "max", "ultra":
		return "high"
	case "low", "medium", "high":
		return effort
	}
	return ""
}

// effortFromParams derives the requested effort from a chat request's reasoning params.
func effortFromParams(params *schemas.ChatParameters) string {
	if params == nil || params.Reasoning == nil {
		return ""
	}
	r := params.Reasoning
	if r.Enabled != nil && !*r.Enabled {
		return "none"
	}
	if r.Effort != nil {
		return strings.ToLower(strings.TrimSpace(*r.Effort))
	}
	if r.MaxTokens != nil {
		switch budget := *r.MaxTokens; {
		case budget == 0:
			return "none"
		case budget < 0:
			return "high"
		case budget <= 2048:
			return "low"
		case budget <= 8192:
			return "medium"
		default:
			return "high"
		}
	}
	if r.Enabled != nil && *r.Enabled {
		return "medium"
	}
	return ""
}

// resolveWireModel maps a requested model id and effort to the id Cloud Code Assist
// serves and the thinkingLevel to send ("" for none). It follows OpenCodex's
// resolveAntigravityEffortWireModel: an effort the model cannot express ("none",
// "minimal", unknown) falls back to the model's default rather than being sent.
func resolveWireModel(model, effort string) (wireModel string, thinkingLevel string) {
	id := strings.ToLower(strings.TrimSpace(model))
	level := resolveThinkingLevel(effort)

	// Retired Flash ids route to the tiered 3.7 Flash, carrying the tier they encoded.
	if retiredTier, ok := retiredFlashModels[id]; ok {
		return "gemini-3.7-flash-tiered", firstNonEmpty(level, retiredTier)
	}
	// Suffix and alias ids already name their tier.
	if wire, ok := passthroughModels[id]; ok {
		return wire, ""
	}

	switch id {
	case "gemini-3.7-flash":
		// One wire id; the tier rides on thinkingLevel, medium by default.
		return "gemini-3.7-flash-tiered", firstNonEmpty(level, "medium")
	case "gemini-3.8-flash":
		// One wire id per tier; sending thinkingLevel beside the suffix would state the
		// tier twice, so none is sent.
		switch level {
		case "low", "high":
			return "gemini-3.8-flash-" + level, ""
		default:
			return "gemini-3.8-flash-medium", ""
		}
	case "gemini-3.1-pro":
		// The high rung (gemini-pro-agent) has no tier suffix, so an explicit effort is
		// also sent as thinkingLevel; the default rung goes without one.
		switch effort {
		case "low":
			return "gemini-3.1-pro-low", "low"
		case "high":
			return "gemini-pro-agent", "high"
		default:
			return "gemini-pro-agent", ""
		}
	}

	if strings.HasPrefix(id, "claude-") {
		return model, level
	}
	return model, ""
}

func isClaudeModel(wireModel string) bool {
	return strings.Contains(strings.ToLower(wireModel), "claude")
}

// isGeminiModel matches the wire ids that take Gemini-specific behaviour (thought
// signatures, includeThoughts).
func isGeminiModel(wireModel string) bool {
	id := strings.ToLower(wireModel)
	if !strings.HasPrefix(id, "gemini") || len(id) < len("gemini")+1 {
		return false
	}
	c := id[len("gemini")]
	return c == '-' || c == '.' || (c >= '0' && c <= '9')
}

// maxOutputTokenCap is the largest maxOutputTokens Cloud Code Assist accepts for a model.
func maxOutputTokenCap(wireModel string) int {
	id := strings.ToLower(wireModel)
	switch {
	case strings.Contains(id, "claude"):
		return 64000
	case strings.Contains(id, "gpt-oss"):
		return 32768
	case strings.Contains(id, "gemini") && strings.Contains(id, "pro"):
		return 65535
	default:
		return 65536
	}
}

// availableModelsResponse is the fetchAvailableModels payload.
type availableModelsResponse struct {
	Models map[string]struct {
		DisplayName    string `json:"displayName"`
		MaxTokens      int    `json:"maxTokens"`
		SupportsImages bool   `json:"supportsImages"`
	} `json:"models"`
	AgentModelSorts []struct {
		Groups []struct {
			ModelIDs []string `json:"modelIds"`
		} `json:"groups"`
	} `json:"agentModelSorts"`
	ImageGenerationModelIDs []string `json:"imageGenerationModelIds"`
	TieredModelIDs          struct {
		Flash []string `json:"flash"`
	} `json:"tieredModelIds"`
}

// toModelInfos turns a fetchAvailableModels payload into the picker ids a caller can
// request: agent-callable ids plus the image and tiered Flash models, with retired ids
// skipped, complete -low/-medium/-high families collapsed and -tiered stripped from
// known picker ids.
func (r *availableModelsResponse) toModelInfos() []modelInfo {
	callable := make(map[string]bool)
	for _, ordering := range r.AgentModelSorts {
		for _, group := range ordering.Groups {
			for _, id := range group.ModelIDs {
				callable[id] = true
			}
		}
	}
	if len(callable) == 0 {
		return nil
	}
	for _, id := range r.ImageGenerationModelIDs {
		callable[id] = true
	}
	for _, id := range r.TieredModelIDs.Flash {
		callable[id] = true
	}

	known := make(map[string]modelInfo, len(staticModels))
	for _, m := range staticModels {
		known[m.ID] = m
	}

	// family base id -> member wire ids
	families := make(map[string][]string)
	for id := range callable {
		if _, retired := retiredFlashModels[id]; retired {
			continue
		}
		base := id
		switch {
		case strings.HasSuffix(id, "-tiered"):
			if trimmed := strings.TrimSuffix(id, "-tiered"); known[trimmed].ID != "" {
				base = trimmed
			}
		case id == "gemini-pro-agent" || id == "gemini-3.1-pro-low":
			if callable["gemini-pro-agent"] && callable["gemini-3.1-pro-low"] {
				base = "gemini-3.1-pro"
			}
		default:
			for _, suffix := range []string{"-low", "-medium", "-high"} {
				if !strings.HasSuffix(id, suffix) {
					continue
				}
				stem := strings.TrimSuffix(id, suffix)
				if callable[stem+"-low"] && callable[stem+"-medium"] && callable[stem+"-high"] {
					base = stem
				}
				break
			}
		}
		families[base] = append(families[base], id)
	}

	infos := make([]modelInfo, 0, len(families))
	for base, members := range families {
		info := known[base]
		info.ID = base
		contextWindow := 0
		for _, member := range members {
			meta, ok := r.Models[member]
			if !ok {
				continue
			}
			if meta.MaxTokens > 0 && (contextWindow == 0 || meta.MaxTokens < contextWindow) {
				contextWindow = meta.MaxTokens
			}
			if info.DisplayName == "" && meta.DisplayName != "" && len(members) == 1 {
				info.DisplayName = meta.DisplayName
			}
			if meta.SupportsImages {
				info.Image = true
			}
		}
		if contextWindow > 0 {
			info.ContextWindow = contextWindow
		}
		if base == imageModelID {
			info.OutputImage = true
		}
		infos = append(infos, info)
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].ID < infos[j].ID })
	return infos
}

// toBifrostListModelsResponse filters models through the key's allow/block lists and
// aliases, like every other provider's list-models converter.
func toBifrostListModelsResponse(models []modelInfo, key schemas.Key, unfiltered bool) *schemas.BifrostListModelsResponse {
	response := &schemas.BifrostListModelsResponse{Data: make([]schemas.Model, 0, len(models))}
	pipeline := &providerUtils.ListModelsPipeline{
		AllowedModels:     key.Models,
		BlacklistedModels: key.BlacklistedModels,
		Aliases:           key.Aliases,
		Unfiltered:        unfiltered,
		ProviderKey:       schemas.Antigravity,
		MatchFns:          providerUtils.DefaultMatchFns(),
	}
	if pipeline.ShouldEarlyExit() {
		return response
	}

	included := make(map[string]bool)
	for _, m := range models {
		for _, result := range pipeline.FilterModel(m.ID) {
			entry := schemas.Model{
				ID:      string(schemas.Antigravity) + "/" + result.ResolvedID,
				OwnedBy: schemas.Ptr("antigravity"),
			}
			if m.DisplayName != "" {
				entry.Name = schemas.Ptr(m.DisplayName)
			}
			if m.ContextWindow > 0 {
				entry.ContextLength = schemas.Ptr(m.ContextWindow)
			}
			input := []string{"text"}
			if m.Image {
				input = append(input, "image")
			}
			output := []string{"text"}
			if m.OutputImage {
				output = append(output, "image")
			}
			entry.Architecture = &schemas.Architecture{InputModalities: input, OutputModalities: output}
			if result.AliasValue != "" {
				entry.Alias = schemas.Ptr(result.AliasValue)
			}
			response.Data = append(response.Data, entry)
			included[strings.ToLower(result.ResolvedID)] = true
		}
	}
	response.Data = append(response.Data, pipeline.BackfillModels(included)...)
	return response
}
