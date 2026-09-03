package agentcapabilityrouter

import "fmt"

// PluginName is the canonical identifier used by Bifrost configuration and logs.
const PluginName = "agent-capability-router"

const (
	roleMain   = "main"
	roleWorker = "worker"
)

// AliasConfig identifies the logical model aliases managed by the plugin.
// All other models bypass capability classification unchanged.
type AliasConfig struct {
	Main   string `json:"main,omitempty"`
	Worker string `json:"worker,omitempty"`
}

// ActiveRolesConfig enables or disables classification independently for each
// managed role. Pointer fields preserve the safe default when a field is omitted.
type ActiveRolesConfig struct {
	Main   *bool `json:"main,omitempty"`
	Worker *bool `json:"worker,omitempty"`
}

// Config controls deterministic capability classification.
//
// ShadowMode defaults to true: classifications are logged, but the model is not
// rewritten until the operator explicitly enables live routing.
type Config struct {
	ShadowMode          *bool               `json:"shadow_mode,omitempty"`
	ConfidenceThreshold *float64            `json:"confidence_threshold,omitempty"`
	HistoryMessages     *int                `json:"history_messages,omitempty"`
	Aliases             *AliasConfig        `json:"aliases,omitempty"`
	ActiveRoles         *ActiveRolesConfig  `json:"active_roles,omitempty"`
	Keywords            map[string][]string `json:"keywords,omitempty"`
}

type resolvedConfig struct {
	ShadowMode          bool
	ConfidenceThreshold float64
	HistoryMessages     int
	Aliases             AliasConfig
	ActiveRoles         map[string]bool
	Keywords            map[string][]string
}

func defaultConfig() resolvedConfig {
	return resolvedConfig{
		ShadowMode:          true,
		ConfidenceThreshold: 0.70,
		HistoryMessages:     8,
		Aliases: AliasConfig{
			Main:   "agent-main-auto",
			Worker: "agent-worker-auto",
		},
		ActiveRoles: map[string]bool{
			roleMain:   true,
			roleWorker: true,
		},
		Keywords: cloneKeywords(defaultKeywords()),
	}
}

func resolveConfig(input *Config) (resolvedConfig, error) {
	resolved := defaultConfig()
	if input == nil {
		return resolved, nil
	}

	if input.ShadowMode != nil {
		resolved.ShadowMode = *input.ShadowMode
	}
	if input.ConfidenceThreshold != nil {
		resolved.ConfidenceThreshold = *input.ConfidenceThreshold
	}
	if input.HistoryMessages != nil {
		resolved.HistoryMessages = *input.HistoryMessages
	}
	if input.Aliases != nil {
		if input.Aliases.Main != "" {
			resolved.Aliases.Main = input.Aliases.Main
		}
		if input.Aliases.Worker != "" {
			resolved.Aliases.Worker = input.Aliases.Worker
		}
	}
	if input.ActiveRoles != nil {
		if input.ActiveRoles.Main != nil {
			resolved.ActiveRoles[roleMain] = *input.ActiveRoles.Main
		}
		if input.ActiveRoles.Worker != nil {
			resolved.ActiveRoles[roleWorker] = *input.ActiveRoles.Worker
		}
	}
	for capability, keywords := range input.Keywords {
		if !isKnownCapability(capability) {
			return resolvedConfig{}, fmt.Errorf("unsupported capability keyword group %q", capability)
		}
		resolved.Keywords[capability] = append([]string(nil), keywords...)
	}

	if err := resolved.validate(); err != nil {
		return resolvedConfig{}, err
	}
	return resolved, nil
}

func (c resolvedConfig) validate() error {
	if c.ConfidenceThreshold <= 0 || c.ConfidenceThreshold > 1 {
		return fmt.Errorf("confidence_threshold must be > 0 and <= 1")
	}
	if c.HistoryMessages < 1 || c.HistoryMessages > 32 {
		return fmt.Errorf("history_messages must be between 1 and 32")
	}
	if c.Aliases.Main == "" || c.Aliases.Worker == "" {
		return fmt.Errorf("main and worker aliases are required")
	}
	if c.Aliases.Main == c.Aliases.Worker {
		return fmt.Errorf("main and worker aliases must be different")
	}
	return nil
}

func cloneKeywords(source map[string][]string) map[string][]string {
	cloned := make(map[string][]string, len(source))
	for capability, keywords := range source {
		cloned[capability] = append([]string(nil), keywords...)
	}
	return cloned
}
