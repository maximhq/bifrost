package complexity

import (
	"errors"
	"fmt"
	"maps"
	"strings"
	"unicode/utf8"

	"github.com/maximhq/bifrost/framework/configstore"
)

// Appending phrases to a router's phrase bank, as the phrase suggestion APIs
// do: an impact check appends them to a copy for one replay, and the add
// phrases API appends them to the saved bank.

// ErrPhraseAppend is a phrase append the bank refuses: an unknown tier, an
// empty or too-long phrase, a phrase another tier already holds, or a bank
// past its phrase limit. Nothing is appended.
var ErrPhraseAppend = errors.New("phrases cannot be added")

// SkippedPhrase is a phrase an append left out because its tier already
// holds it.
type SkippedPhrase struct {
	Tier string `json:"tier"`
	Text string `json:"text"`
}

// PhraseAppend is what an append did: the phrases added, by tier, and those
// skipped as already there.
type PhraseAppend struct {
	Added   map[string][]string `json:"added"`
	Skipped []SkippedPhrase     `json:"skipped,omitempty"`
}

// AppendPhrases appends phrases, keyed by tier (SIMPLE, MEDIUM, COMPLEX, in
// any case), to a copy of keywords. A phrase is compared as the bank compares
// them, case- and whitespace-insensitively: one its tier already holds, or
// that repeats in the request, is skipped; one another tier holds refuses the
// whole append, since a phrase routes to one tier. The bank's phrase and
// length limits apply. keywords is not modified.
func AppendPhrases(keywords EditableKeywordConfig, add map[string][]string) (EditableKeywordConfig, PhraseAppend, error) {
	lists, result, err := appendToTiers(map[string][]string{
		TierSimple: keywords.SimpleKeywords, TierMedium: keywords.MediumKeywords, TierComplex: keywords.ComplexKeywords,
	}, add, phraseBankLimits)
	out := EditableKeywordConfig{SimpleKeywords: lists[TierSimple], MediumKeywords: lists[TierMedium], ComplexKeywords: lists[TierComplex]}
	return out, result, err
}

// AppendDecisionExamples appends examples, keyed by tier as AppendPhrases
// takes phrases, to a copy of a decision-model router's guidance, under the
// same rules: compared case- and whitespace-insensitively, skipped when the
// tier holds one already, refused when another tier does or a tier would go
// past its limit. A tier's examples are the ones the decision model reads, so
// a tier that has never been edited starts from its shipped examples, which
// an override would otherwise replace. Only tiers given an example are
// written. decision is not modified; nil is the shipped guidance.
func AppendDecisionExamples(decision *DecisionConfig, add map[string][]string) (*DecisionConfig, PhraseAppend, error) {
	resolved := decision.ResolvedCriteria()
	lists := make(map[string][]string, len(resolved))
	for tier, criteria := range resolved {
		lists[tier] = criteria.Examples
	}
	lists, result, err := appendToTiers(lists, add, decisionExampleLimits)
	if err != nil {
		return decision, result, err
	}
	out := &DecisionConfig{}
	if decision != nil {
		copied := *decision
		out = &copied
	}
	out.Criteria = maps.Clone(out.Criteria)
	if out.Criteria == nil {
		out.Criteria = map[string]configstore.ComplexityDecisionTierCriteria{}
	}
	for tier, added := range result.Added {
		if len(added) > 0 {
			criteria := out.Criteria[tier]
			criteria.Examples = lists[tier]
			out.Criteria[tier] = criteria
		}
	}
	return out, result, nil
}

// tierAppendLimits are what an append to per-tier lists checks, and the word
// its errors use for an item.
type tierAppendLimits struct {
	item       string // "phrase" or "example"
	oneTier    string // why an item cannot be in two tiers
	maxChars   int
	maxTotal   int // across all tiers; 0 is none
	maxPerTier int // 0 is none
}

var (
	phraseBankLimits      = tierAppendLimits{item: "phrase", oneTier: "a phrase routes to one tier", maxChars: configstore.MaxComplexitySemanticPhraseCharacters, maxTotal: configstore.MaxComplexitySemanticPhrases}
	decisionExampleLimits = tierAppendLimits{item: "example", oneTier: "an example describes one tier", maxChars: configstore.MaxComplexityDecisionCriteriaItemCharacters, maxPerTier: configstore.MaxComplexityDecisionExamples}
)

// appendToTiers appends items, keyed by tier in any case, to copies of
// per-tier lists; see AppendPhrases for the rules. lists is not modified.
func appendToTiers(lists map[string][]string, add map[string][]string, limits tierAppendLimits) (map[string][]string, PhraseAppend, error) {
	out := make(map[string][]string, 3)
	held := map[string]string{} // canonical item -> tier
	total := 0
	for _, tier := range OrderedTiers() {
		out[tier] = append([]string(nil), lists[tier]...)
		for _, item := range out[tier] {
			held[canonicalPhrase(item)] = tier
		}
		total += len(out[tier])
	}
	result := PhraseAppend{Added: map[string][]string{}}
	given := 0
	for _, items := range add {
		given += len(items)
	}
	if given == 0 {
		return out, result, fmt.Errorf("%w: no %ss given", ErrPhraseAppend, limits.item)
	}
	// One key per tier: two spellings of a tier ("SIMPLE" and "simple") would
	// merge in map order, so which of their items was added and which skipped
	// would vary from call to call.
	keys := make(map[string]string, len(add))
	for key := range add {
		tier := strings.ToUpper(strings.TrimSpace(key))
		if !isComplexityTier(tier) {
			return out, result, fmt.Errorf("%w: unknown tier %q; use SIMPLE, MEDIUM or COMPLEX", ErrPhraseAppend, key)
		}
		if other, ok := keys[tier]; ok {
			first, second := other, key
			if second < first {
				first, second = second, first
			}
			return out, result, fmt.Errorf("%w: tier %s is given twice (%q and %q)", ErrPhraseAppend, tier, first, second)
		}
		keys[tier] = key
	}
	// Tiers in a fixed order, so the result does not depend on map order.
	for _, tier := range OrderedTiers() {
		var items []string
		for key, values := range add {
			if strings.EqualFold(strings.TrimSpace(key), tier) {
				items = append(items, values...)
			}
		}
		for _, raw := range items {
			item := strings.TrimSpace(raw)
			if item == "" {
				return out, result, fmt.Errorf("%w: a %s %s is empty", ErrPhraseAppend, tier, limits.item)
			}
			if n := utf8.RuneCountInString(item); n > limits.maxChars {
				return out, result, fmt.Errorf("%w: a %s %s has %d characters; the limit is %d", ErrPhraseAppend, tier, limits.item, n, limits.maxChars)
			}
			key := canonicalPhrase(item)
			if holder, ok := held[key]; ok {
				if holder != tier {
					return out, result, fmt.Errorf("%w: %q is already a %s %s; %s", ErrPhraseAppend, item, holder, limits.item, limits.oneTier)
				}
				result.Skipped = append(result.Skipped, SkippedPhrase{Tier: tier, Text: item})
				continue
			}
			held[key] = tier
			out[tier] = append(out[tier], item)
			result.Added[tier] = append(result.Added[tier], item)
			total++
		}
		if limits.maxPerTier > 0 && len(out[tier]) > limits.maxPerTier {
			return out, result, fmt.Errorf("%w: %s would hold %d %ss; the limit is %d per tier", ErrPhraseAppend, tier, len(out[tier]), limits.item, limits.maxPerTier)
		}
	}
	if limits.maxTotal > 0 && total > limits.maxTotal {
		return out, result, fmt.Errorf("%w: the bank would hold %d %ss; the limit is %d across all tiers", ErrPhraseAppend, total, limits.item, limits.maxTotal)
	}
	return out, result, nil
}

// canonicalPhrase is a phrase as the bank compares phrases: lower case, with
// runs of whitespace folded to one space.
func canonicalPhrase(phrase string) string {
	return strings.ToLower(strings.Join(strings.Fields(phrase), " "))
}

// BankPhrases lists a bank's phrases with their tiers.
func BankPhrases(keywords EditableKeywordConfig) []SuggestionPhrase {
	out := make([]SuggestionPhrase, 0, len(keywords.SimpleKeywords)+len(keywords.MediumKeywords)+len(keywords.ComplexKeywords))
	for _, tier := range []struct {
		name  string
		items []string
	}{{TierSimple, keywords.SimpleKeywords}, {TierMedium, keywords.MediumKeywords}, {TierComplex, keywords.ComplexKeywords}} {
		for _, text := range tier.items {
			out = append(out, SuggestionPhrase{Tier: tier.name, Text: text})
		}
	}
	return out
}
