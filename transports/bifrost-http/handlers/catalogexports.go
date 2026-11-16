package handlers

import (
	"slices"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

type piCatalogResponse struct {
	Providers struct {
		Bifrost piCatalogProvider `json:"bifrost"`
	} `json:"providers"`
}

type piCatalogProvider struct {
	Name       string           `json:"name"`
	API        string           `json:"api"`
	BaseURL    string           `json:"baseUrl"`
	APIKey     string           `json:"apiKey"`
	AuthHeader bool             `json:"authHeader"`
	Models     []piCatalogModel `json:"models"`
}

type piCatalogModel struct {
	ID               string             `json:"id"`
	Name             string             `json:"name"`
	Reasoning        *bool              `json:"reasoning,omitempty"`
	ThinkingLevelMap map[string]*string `json:"thinkingLevelMap,omitempty"`
	Input            []string           `json:"input,omitempty"`
	ContextWindow    *int               `json:"contextWindow,omitempty"`
	MaxTokens        *int               `json:"maxTokens,omitempty"`
	Cost             *piCatalogCost     `json:"cost,omitempty"`
	Compat           *piCatalogCompat   `json:"compat,omitempty"`
}

type piCatalogCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

type piCatalogCompat struct {
	SupportsReasoningEffort *bool `json:"supportsReasoningEffort,omitempty"`
}

func newPiCatalog(baseURL string) *piCatalogResponse {
	response := &piCatalogResponse{}
	response.Providers.Bifrost = piCatalogProvider{
		Name: "Bifrost", API: "openai-completions", BaseURL: baseURL,
		APIKey: "$BIFROST_VIRTUAL_KEY", AuthHeader: true, Models: []piCatalogModel{},
	}
	return response
}

func makePiCatalogModel(id string, entry *modelcatalog.PricingEntry, caps *schemas.ModelCapabilities) piCatalogModel {
	model := piCatalogModel{ID: id, Name: id}
	if entry != nil {
		model.ContextWindow = entry.ContextLength
		model.MaxTokens = entry.MaxOutputTokens
		if entry.Architecture != nil {
			for _, modality := range entry.Architecture.InputModalities {
				if (modality == "text" || modality == "image") && !slices.Contains(model.Input, modality) {
					model.Input = append(model.Input, modality)
				}
			}
		}
		// Pi requires all four rates; missing cache prices do not mean free caching.
		if entry.InputCostPerToken != nil && entry.OutputCostPerToken != nil && entry.CacheReadInputTokenCost != nil && entry.CacheCreationInputTokenCost != nil {
			model.Cost = &piCatalogCost{
				Input: *entry.InputCostPerToken * 1e6, Output: *entry.OutputCostPerToken * 1e6,
				CacheRead: *entry.CacheReadInputTokenCost * 1e6, CacheWrite: *entry.CacheCreationInputTokenCost * 1e6,
			}
		}
	}
	if caps != nil {
		model.Reasoning = catalogReasoningSupport(caps)
		if caps.SupportsReasoningEffort != nil {
			model.Compat = &piCatalogCompat{SupportsReasoningEffort: caps.SupportsReasoningEffort}
		}
		if (model.Reasoning == nil || *model.Reasoning) && len(caps.ReasoningEffortLevels) > 0 {
			// Explicit nulls prevent Pi's default mapping from advertising unsupported levels.
			levels := map[string]*string{"off": nil, "minimal": nil, "low": nil, "medium": nil, "high": nil, "xhigh": nil, "max": nil}
			known := false
			for _, effort := range caps.ReasoningEffortLevels {
				if strings.TrimSpace(effort) == "" {
					continue
				}
				known = true
				if effort == "none" {
					levels["off"] = &effort
				} else if _, ok := levels[effort]; ok && effort != "off" {
					levels[effort] = &effort
				}
			}
			if known {
				if caps.SupportsNoneReasoningEffort != nil && *caps.SupportsNoneReasoningEffort {
					none := "none"
					levels["off"] = &none
				}
				model.ThinkingLevelMap = levels
				if model.Compat == nil {
					supported := true
					model.Compat = &piCatalogCompat{SupportsReasoningEffort: &supported}
				}
			}
		}
	}
	return model
}

type openCodeCatalogResponse struct {
	Schema    string `json:"$schema"`
	Providers struct {
		Bifrost openCodeCatalogProvider `json:"bifrost"`
	} `json:"providers"`
}

type openCodeCatalogProvider struct {
	Name     string                          `json:"name"`
	Env      []string                        `json:"env"`
	Package  string                          `json:"package"`
	Settings openCodeCatalogSettings         `json:"settings"`
	Models   map[string]openCodeCatalogModel `json:"models"`
}

type openCodeCatalogSettings struct {
	BaseURL string `json:"baseURL"`
}

type openCodeCatalogModel struct {
	ModelID      string                       `json:"modelID"`
	Name         string                       `json:"name"`
	Capabilities *openCodeCatalogCapabilities `json:"capabilities,omitempty"`
	Limit        *openCodeCatalogLimit        `json:"limit,omitempty"`
	Cost         *openCodeCatalogCost         `json:"cost,omitempty"`
}

type openCodeCatalogCapabilities struct {
	Tools     *bool                     `json:"tools,omitempty"`
	Input     []string                  `json:"input,omitempty"`
	Output    []string                  `json:"output,omitempty"`
	Reasoning *openCodeCatalogReasoning `json:"reasoning,omitempty"`
}

type openCodeCatalogReasoning struct {
	Supported bool     `json:"supported"`
	Effort    []string `json:"effort,omitempty"`
	Toggle    *bool    `json:"toggle,omitempty"`
}

type openCodeCatalogLimit struct {
	Context *int `json:"context,omitempty"`
	Input   *int `json:"input,omitempty"`
	Output  *int `json:"output,omitempty"`
}

type openCodeCatalogCost struct {
	Input  float64                   `json:"input"`
	Output float64                   `json:"output"`
	Cache  *openCodeCatalogCacheCost `json:"cache,omitempty"`
}

type openCodeCatalogCacheCost struct {
	Read  *float64 `json:"read,omitempty"`
	Write *float64 `json:"write,omitempty"`
}

func newOpenCodeCatalog(baseURL string) *openCodeCatalogResponse {
	response := &openCodeCatalogResponse{Schema: "https://opencode.ai/config.json"}
	response.Providers.Bifrost = openCodeCatalogProvider{
		Name: "Bifrost", Env: []string{"BIFROST_VIRTUAL_KEY"},
		Package: "@opencode/ai/providers/openai-compatible", Settings: openCodeCatalogSettings{BaseURL: baseURL},
		Models: map[string]openCodeCatalogModel{},
	}
	return response
}

func makeOpenCodeCatalogModel(id string, entry *modelcatalog.PricingEntry, caps *schemas.ModelCapabilities) openCodeCatalogModel {
	model := openCodeCatalogModel{ModelID: id, Name: id}
	capabilities := openCodeCatalogCapabilities{}
	if entry != nil {
		if entry.ContextLength != nil || entry.MaxInputTokens != nil || entry.MaxOutputTokens != nil {
			model.Limit = &openCodeCatalogLimit{Context: entry.ContextLength, Input: entry.MaxInputTokens, Output: entry.MaxOutputTokens}
		}
		if entry.Architecture != nil {
			capabilities.Input = entry.Architecture.InputModalities
			capabilities.Output = entry.Architecture.OutputModalities
		}
		// OpenCode requires input and output, but each cache rate is optional.
		if entry.InputCostPerToken != nil && entry.OutputCostPerToken != nil {
			model.Cost = &openCodeCatalogCost{Input: *entry.InputCostPerToken * 1e6, Output: *entry.OutputCostPerToken * 1e6}
			if entry.CacheReadInputTokenCost != nil || entry.CacheCreationInputTokenCost != nil {
				model.Cost.Cache = &openCodeCatalogCacheCost{
					Read: catalogPricePerMillion(entry.CacheReadInputTokenCost), Write: catalogPricePerMillion(entry.CacheCreationInputTokenCost),
				}
			}
		}
	}
	if caps != nil {
		capabilities.Tools = caps.SupportsFunctionCalling
		if supported := catalogReasoningSupport(caps); supported != nil {
			capabilities.Reasoning = &openCodeCatalogReasoning{Supported: *supported}
			if *supported {
				for _, effort := range caps.ReasoningEffortLevels {
					if strings.TrimSpace(effort) != "" {
						capabilities.Reasoning.Effort = append(capabilities.Reasoning.Effort, effort)
					}
				}
				capabilities.Reasoning.Toggle = caps.SupportsNoneReasoningEffort
			}
		}
	}
	if capabilities.Tools != nil || len(capabilities.Input) > 0 || len(capabilities.Output) > 0 || capabilities.Reasoning != nil {
		model.Capabilities = &capabilities
	}
	return model
}

func catalogReasoningSupport(caps *schemas.ModelCapabilities) *bool {
	if caps.SupportsReasoning != nil {
		return caps.SupportsReasoning
	}
	for _, effort := range caps.ReasoningEffortLevels {
		if strings.TrimSpace(effort) != "" && effort != "none" && effort != "off" {
			supported := true
			return &supported
		}
	}
	return nil
}

func catalogPricePerMillion(price *float64) *float64 {
	if price == nil {
		return nil
	}
	perMillion := *price * 1e6
	return &perMillion
}
