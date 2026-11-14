package complexity

import (
	"errors"
	"fmt"
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
	out := EditableKeywordConfig{
		SimpleKeywords:  append([]string(nil), keywords.SimpleKeywords...),
		MediumKeywords:  append([]string(nil), keywords.MediumKeywords...),
		ComplexKeywords: append([]string(nil), keywords.ComplexKeywords...),
	}
	lists := map[string]*[]string{TierSimple: &out.SimpleKeywords, TierMedium: &out.MediumKeywords, TierComplex: &out.ComplexKeywords}
	held := map[string]string{} // canonical phrase -> tier
	for _, tier := range []string{TierSimple, TierMedium, TierComplex} {
		for _, phrase := range *lists[tier] {
			held[canonicalPhrase(phrase)] = tier
		}
	}
	result := PhraseAppend{Added: map[string][]string{}}
	total := len(out.SimpleKeywords) + len(out.MediumKeywords) + len(out.ComplexKeywords)
	given := 0
	for _, phrases := range add {
		given += len(phrases)
	}
	if given == 0 {
		return out, result, fmt.Errorf("%w: no phrases given", ErrPhraseAppend)
	}
	// One key per tier: two spellings of a tier ("SIMPLE" and "simple") would
	// merge in map order, so which of their phrases was added and which skipped
	// would vary from call to call.
	keys := make(map[string]string, len(add))
	for key := range add {
		tier := strings.ToUpper(strings.TrimSpace(key))
		switch tier {
		case TierSimple, TierMedium, TierComplex:
		default:
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
	for _, tier := range []string{TierSimple, TierMedium, TierComplex} {
		var phrases []string
		for key, values := range add {
			if strings.EqualFold(strings.TrimSpace(key), tier) {
				phrases = append(phrases, values...)
			}
		}
		for _, raw := range phrases {
			phrase := strings.TrimSpace(raw)
			if phrase == "" {
				return out, result, fmt.Errorf("%w: a %s phrase is empty", ErrPhraseAppend, tier)
			}
			if n := utf8.RuneCountInString(phrase); n > configstore.MaxComplexitySemanticPhraseCharacters {
				return out, result, fmt.Errorf("%w: a %s phrase has %d characters; the limit is %d", ErrPhraseAppend, tier, n, configstore.MaxComplexitySemanticPhraseCharacters)
			}
			key := canonicalPhrase(phrase)
			if holder, ok := held[key]; ok {
				if holder != tier {
					return out, result, fmt.Errorf("%w: %q is already a %s phrase; a phrase routes to one tier", ErrPhraseAppend, phrase, holder)
				}
				result.Skipped = append(result.Skipped, SkippedPhrase{Tier: tier, Text: phrase})
				continue
			}
			held[key] = tier
			*lists[tier] = append(*lists[tier], phrase)
			result.Added[tier] = append(result.Added[tier], phrase)
			total++
		}
	}
	if total > configstore.MaxComplexitySemanticPhrases {
		return out, result, fmt.Errorf("%w: the bank would hold %d phrases; the limit is %d across all tiers", ErrPhraseAppend, total, configstore.MaxComplexitySemanticPhrases)
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
