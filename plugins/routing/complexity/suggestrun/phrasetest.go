package suggestrun

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/plugins/routing/complexity"
)

// MaxTestPhrases and MaxTestExamples bound one phrase test: it embeds every
// phrase and example it is given.
const (
	MaxTestPhrases  = 10
	MaxTestExamples = 32
)

// ErrInvalidPhraseTest is a phrase test asked with phrases it cannot route.
var ErrInvalidPhraseTest = errors.New("invalid phrase test")

// TestExample is a request a phrase test routes, by its text.
type TestExample struct {
	LogID string `json:"log_id"`
	Text  string `json:"text"`
}

// Routing is how one request routes against a phrase bank: the tier of its
// nearest phrase, which phrase, how similar, and whether that clears the
// router's similarity floor.
type Routing struct {
	Tier       string  `json:"tier"`
	Phrase     string  `json:"phrase"`
	Similarity float64 `json:"similarity"`
	Accepted   bool    `json:"accepted"`
	Candidate  bool    `json:"candidate"` // the phrase is one being tested
}

// TestedExample is one example before and after adding the phrases.
type TestedExample struct {
	LogID   string  `json:"log_id"`
	Text    string  `json:"text"`
	Current Routing `json:"current"`
	With    Routing `json:"with_phrases"`
	Changed bool    `json:"changed"` // the routed tier or its acceptance changes
}

// TestedPhrase is one tested phrase and the live phrase nearest to it, which
// says whether it duplicates the bank.
type TestedPhrase struct {
	Tier    string  `json:"tier"`
	Text    string  `json:"text"`
	Nearest Routing `json:"nearest_existing"`
}

// PhraseTest is the outcome of routing examples with and without phrases.
type PhraseTest struct {
	Phrases  []TestedPhrase  `json:"phrases"`
	Examples []TestedExample `json:"examples"`
	// TakenOver is the share of examples whose nearest phrase is a tested
	// one, whether or not it clears minSimilarity.
	TakenOver float64 `json:"taken_over"`
}

// TestPhrases routes examples the way the router does, by nearest phrase,
// against the live bank and against the bank with phrases added, so a phrase
// can be tried before it is saved. Nothing is stored.
func TestPhrases(ctx context.Context, space Space, minSimilarity float64, phrases []complexity.SuggestionPhrase, examples []TestExample) (PhraseTest, error) {
	if len(phrases) == 0 || len(phrases) > MaxTestPhrases {
		return PhraseTest{}, fmt.Errorf("%w: test between 1 and %d phrases", ErrInvalidPhraseTest, MaxTestPhrases)
	}
	if len(examples) > MaxTestExamples {
		examples = examples[:MaxTestExamples]
	}
	live := space.LivePhrases()
	if len(live) == 0 {
		return PhraseTest{}, complexity.ErrNoServingGeneration
	}
	texts := make([]string, 0, len(phrases)+len(examples))
	for _, p := range phrases {
		if strings.TrimSpace(p.Text) == "" || !isTier(p.Tier) {
			return PhraseTest{}, fmt.Errorf("%w: every tested phrase needs text and a tier of SIMPLE, MEDIUM or COMPLEX", ErrInvalidPhraseTest)
		}
		texts = append(texts, p.Text)
	}
	for _, e := range examples {
		texts = append(texts, e.Text)
	}
	vectors, err := space.Embed(ctx, texts)
	if err != nil {
		return PhraseTest{}, fmt.Errorf("embed tested phrases: %w", err)
	}
	livePhrases := make([]complexity.SuggestionPhrase, len(live))
	for i, p := range live {
		livePhrases[i] = complexity.SuggestionPhrase{Tier: p.Tier, Text: p.Text, Vector: unit(p.Vector)}
	}
	candidates := make([]complexity.SuggestionPhrase, len(phrases))
	out := PhraseTest{Phrases: make([]TestedPhrase, len(phrases))}
	for i, p := range phrases {
		candidates[i] = complexity.SuggestionPhrase{Tier: p.Tier, Text: p.Text, Vector: unit(vectors[i])}
		out.Phrases[i] = TestedPhrase{Tier: p.Tier, Text: p.Text, Nearest: route(candidates[i].Vector, livePhrases, nil, minSimilarity)}
	}
	var taken int
	for j, e := range examples {
		v := unit(vectors[len(phrases)+j])
		before := route(v, livePhrases, nil, minSimilarity)
		after := route(v, livePhrases, candidates, minSimilarity)
		if after.Candidate {
			taken++
		}
		out.Examples = append(out.Examples, TestedExample{
			LogID: e.LogID, Text: e.Text, Current: before, With: after,
			Changed: before.Tier != after.Tier || before.Accepted != after.Accepted,
		})
	}
	if len(examples) > 0 {
		out.TakenOver = float64(taken) / float64(len(examples))
	}
	return out, nil
}

// route finds the nearest phrase across live and candidates.
func route(v []float32, live, candidates []complexity.SuggestionPhrase, minSimilarity float64) Routing {
	best := Routing{Similarity: -2}
	consider := func(set []complexity.SuggestionPhrase, candidate bool) {
		for _, p := range set {
			if s := float64(dot(v, p.Vector)); s > best.Similarity {
				best = Routing{Tier: p.Tier, Phrase: p.Text, Similarity: s, Candidate: candidate}
			}
		}
	}
	consider(live, false)
	consider(candidates, true)
	best.Accepted = minSimilarity <= 0 || best.Similarity >= minSimilarity
	return best
}

func isTier(tier string) bool {
	return tier == complexity.TierSimple || tier == complexity.TierMedium || tier == complexity.TierComplex
}
