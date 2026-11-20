package suggest

import (
	"context"
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/plugins/routing/complexity"
)

// TierInput is what the tier step sees of one cluster.
type TierInput struct {
	// Requests are the texts shown, the most typical first.
	Requests []string
	// Total is how many requests the cluster holds.
	Total int
	// Template is the opening the requests share when they are one prompt
	// template with different fills; empty otherwise.
	Template string
}

// TierVerdict is the tier step's answer. An empty Tier means no usable tier.
type TierVerdict struct {
	Tier       string `json:"tier"`
	Confidence string `json:"confidence"`
	Reason     string `json:"reason"`
}

// TierDecider picks the tier for a coherent cluster. LLMTierDecider is the
// chat-model implementation; a decision model implements it outside this
// package, which does no I/O.
type TierDecider interface {
	DecideTier(ctx context.Context, in TierInput) (TierVerdict, error)
}

// LLMTierDecider asks a chat model for the tier.
type LLMTierDecider struct {
	LLM LLM
	// Prompt is an edited system prompt body (everything but the fixed
	// reinforcement). Empty renders TierPrompt from Guidance and Examples.
	Prompt string
	// Guidance is the tier definitions for the rendered prompt; empty means
	// the router's default LLM classifier guidance.
	Guidance string
	// Examples are the router's own phrases per tier, shown as examples of
	// each tier. Callers pass only the phrases an administrator configured,
	// not the shipped defaults, bounded by MaxTierExamples per tier.
	Examples map[string][]string
}

// DecideTier implements TierDecider. One call: an unparseable answer is no
// usable tier rather than a failed run, which keeps a cluster at three model
// calls at most (two phrase attempts and this one).
func (d *LLMTierDecider) DecideTier(ctx context.Context, in TierInput) (TierVerdict, error) {
	if d == nil || d.LLM == nil {
		return TierVerdict{}, fmt.Errorf("no tier model configured")
	}
	raw, err := d.LLM.CompleteJSON(ctx, []Message{
		{Role: "system", Content: tierSystemPrompt(d.Prompt, d.Guidance, d.Examples)},
		{Role: "user", Content: TierRequestText(in)},
	})
	if err != nil {
		return TierVerdict{}, err
	}
	v, err := ParseTierVerdict(raw)
	if err != nil {
		return TierVerdict{}, nil
	}
	return v, nil
}

// BankTierExamples picks the tier examples from a router's phrase lists, in
// list order: the phrases its administrator configured, never the shipped
// defaults. A run and the editors that show the generated prompt both use it,
// so what an editor shows is what a run sends.
func BankTierExamples(bank complexity.EditableKeywordConfig) map[string][]string {
	var live []Phrase
	for _, list := range []struct {
		tier    string
		phrases []string
	}{
		{complexity.TierSimple, bank.SimpleKeywords},
		{complexity.TierMedium, bank.MediumKeywords},
		{complexity.TierComplex, bank.ComplexKeywords},
	} {
		for _, p := range list.phrases {
			live = append(live, Phrase{Tier: list.tier, Text: p})
		}
	}
	defaults := complexity.DefaultEditableKeywordConfig()
	return TierExamples(live, map[string][]string{
		complexity.TierSimple:  defaults.SimpleKeywords,
		complexity.TierMedium:  defaults.MediumKeywords,
		complexity.TierComplex: defaults.ComplexKeywords,
	})
}

// TierExamples picks each tier's examples from the router's phrases: those
// not in the shipped defaults, in the router's order, at most MaxTierExamples
// per tier. Phrases are compared the way the router embeds them, so a default
// that differs only in case or spacing is still a default.
func TierExamples(live []Phrase, defaults map[string][]string) map[string][]string {
	shipped := make(map[string]bool)
	for _, phrases := range defaults {
		for _, p := range phrases {
			shipped[normalizePhrase(p)] = true
		}
	}
	out := make(map[string][]string)
	seen := make(map[string]bool)
	for _, p := range live {
		key := normalizePhrase(p.Text)
		tier := strings.ToUpper(strings.TrimSpace(p.Tier))
		if key == "" || shipped[key] || seen[key] || !validTier(tier) || len(out[tier]) >= MaxTierExamples {
			continue
		}
		seen[key] = true
		out[tier] = append(out[tier], strings.TrimSpace(p.Text))
	}
	return out
}
