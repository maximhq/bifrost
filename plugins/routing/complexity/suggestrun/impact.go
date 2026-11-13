package suggestrun

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
)

const (
	// MaxImpactRequests bounds the requests one replay routes. Each costs one
	// dot product per distinct phrase, so a replay stays a few seconds; past
	// the bound a uniform sample of the window is replayed instead.
	MaxImpactRequests = 10000
	// MaxImpactPhrases bounds the draft bank a replay is given, which is the
	// router's whole bank: the gateway refuses to save more than this.
	MaxImpactPhrases = configstore.MaxComplexitySemanticPhrases
	// A live match is confident when it is at least confidentSimilarity, or
	// confidentMargin above the router's floor when that is higher: only then
	// does a draft phrase taking the request to another tier override a real
	// match rather than a coincidental one. Paraphrases of one request type
	// sit around 0.6 and above in common embedding spaces; unrelated requests
	// that merely clear a low floor sit in the 0.4s.
	confidentSimilarity = 0.60
	confidentMargin     = 0.20
)

// ImpactSettings are one replay's parameters.
type ImpactSettings struct {
	From, To time.Time
	// MinSimilarity is the router's floor: a request whose nearest phrase is
	// below it matches nothing and takes the router's fallback.
	MinSimilarity float64
	// DraftMinSimilarity is the floor the draft bank is judged at, when the
	// draft changes it too; nil is MinSimilarity. The live side, and what
	// counts as a confident live match, keep MinSimilarity.
	DraftMinSimilarity *float64
	// Draft is the phrase bank to compare with the live one, whole: every
	// phrase the draft would serve, with its tier. Vectors are not read.
	Draft []complexity.SuggestionPhrase
	// Traffic supplies the requests; nil scans the space's captured requests.
	Traffic Traffic
	Seed    int64
}

// ImpactChange is one request whose tier the draft changes. A tier is
// logstore.ComplexitySuggestionUnmatchedTier when nothing matches it.
type ImpactChange struct {
	LogID      string  `json:"log_id"`
	From       string  `json:"from"`
	To         string  `json:"to"`
	Phrase     string  `json:"phrase"`     // the draft phrase it now matches; empty when it matches none
	Similarity float64 `json:"similarity"` // to that phrase
}

// ImpactAttention is a draft phrase that takes requests the live bank routed
// confidently to another tier: worth a second look before applying.
type ImpactAttention struct {
	Phrase   string `json:"phrase"`
	Tier     string `json:"tier"`
	FromTier string `json:"from_tier"` // where most of them went before
	Requests int    `json:"requests"`
	LogID    string `json:"log_id"` // one of them, for an example
}

// Impact is what a draft phrase bank would do to recent traffic.
type Impact struct {
	// Requests is how many requests were replayed; Total is how many the
	// window held. They differ when the window held more than
	// MaxImpactRequests and a sample was replayed.
	Requests int `json:"requests"`
	Total    int `json:"total"`
	// Live and Draft count the replayed requests per tier each bank routes
	// them to.
	Live      map[string]int    `json:"live"`
	Draft     map[string]int    `json:"draft"`
	Changed   []ImpactChange    `json:"changed"`
	Attention []ImpactAttention `json:"attention"`
}

// ReplayImpact routes the window's requests against the live bank and the
// draft bank, by nearest phrase as the router does, and reports what changes.
// Draft phrases the live bank lacks are embedded; nothing is stored.
func ReplayImpact(ctx context.Context, space Space, s ImpactSettings) (Impact, error) {
	if len(s.Draft) > MaxImpactPhrases {
		return Impact{}, fmt.Errorf("%w: a draft bank holds at most %d phrases", ErrInvalidPhraseTest, MaxImpactPhrases)
	}
	live := space.LivePhrases()
	if len(live) == 0 {
		return Impact{}, complexity.ErrNoServingGeneration
	}
	if s.Traffic == nil {
		s.Traffic = capturedTraffic{space}
	}

	// One routing table over the union of both banks: each distinct phrase is
	// scored once per request, and each bank takes its best from its own
	// members.
	type entry struct {
		tier, text    string
		vector        []float32
		inLive, draft bool
	}
	key := func(tier, text string) string { return tier + "\x00" + normalizeText(text) }
	index := map[string]int{}
	var entries []entry
	for _, p := range live {
		k := key(p.Tier, p.Text)
		if _, ok := index[k]; ok {
			continue
		}
		index[k] = len(entries)
		entries = append(entries, entry{tier: p.Tier, text: p.Text, vector: unit(p.Vector), inLive: true})
	}
	var embed []string
	var embedAt []int
	for _, p := range s.Draft {
		text := strings.TrimSpace(p.Text)
		if text == "" || !isTier(p.Tier) {
			return Impact{}, fmt.Errorf("%w: every draft phrase needs text and a tier of SIMPLE, MEDIUM or COMPLEX", ErrInvalidPhraseTest)
		}
		k := key(p.Tier, text)
		if i, ok := index[k]; ok {
			entries[i].draft = true
			continue
		}
		index[k] = len(entries)
		// Embedded as the gateway embeds a saved phrase, lowercased
		// (ComplexityAnalyzerConfig.Normalized), so the replay routes as the
		// saved router will; the entry keeps the text as written.
		embed = append(embed, normalizeText(text))
		embedAt = append(embedAt, len(entries))
		entries = append(entries, entry{tier: p.Tier, text: text, draft: true})
	}
	if len(embed) > 0 {
		vectors, err := space.Embed(ctx, embed)
		if err != nil {
			return Impact{}, fmt.Errorf("embed draft phrases: %w", err)
		}
		if len(vectors) != len(embed) {
			return Impact{}, fmt.Errorf("embed draft phrases: asked for %d, got %d", len(embed), len(vectors))
		}
		for j, i := range embedAt {
			entries[i].vector = unit(vectors[j])
		}
	}

	// A uniform sample of the window, as a run takes one.
	rng := rand.New(rand.NewSource(s.Seed))
	var sample []complexity.CapturedRequest
	var total int
	if err := s.Traffic.EachRequest(ctx, s.From, s.To, func(page []complexity.CapturedRequest) error {
		for _, r := range page {
			total++
			if len(sample) < MaxImpactRequests {
				sample = append(sample, r)
			} else if j := rng.Intn(total); j < MaxImpactRequests {
				sample[j] = r
			}
		}
		return ctx.Err()
	}); err != nil {
		return Impact{}, err
	}

	out := Impact{Requests: len(sample), Total: total, Live: map[string]int{}, Draft: map[string]int{}, Changed: []ImpactChange{}, Attention: []ImpactAttention{}}
	draftFloor := s.MinSimilarity
	if s.DraftMinSimilarity != nil {
		draftFloor = *s.DraftMinSimilarity
	}
	routed := func(i int, sim float32, floor float64) string {
		if i < 0 || (floor > 0 && float64(sim) < floor) {
			return logstore.ComplexitySuggestionUnmatchedTier
		}
		return entries[i].tier
	}
	type attention struct {
		from    map[string]int
		count   int
		example string
	}
	flagged := map[int]*attention{}
	for _, r := range sample {
		if err := ctx.Err(); err != nil {
			return Impact{}, err
		}
		v := unit(r.Vector)
		liveBest, draftBest := -1, -1
		var liveSim, draftSim float32 = -2, -2
		for i := range entries {
			e := &entries[i]
			if len(e.vector) != len(v) {
				continue
			}
			sim := dot(v, e.vector)
			if e.inLive && sim > liveSim {
				liveBest, liveSim = i, sim
			}
			if e.draft && sim > draftSim {
				draftBest, draftSim = i, sim
			}
		}
		from, to := routed(liveBest, liveSim, s.MinSimilarity), routed(draftBest, draftSim, draftFloor)
		out.Live[from]++
		out.Draft[to]++
		if from == to {
			continue
		}
		change := ImpactChange{LogID: r.LogID, From: from, To: to}
		if to != logstore.ComplexitySuggestionUnmatchedTier {
			change.Phrase, change.Similarity = entries[draftBest].text, float64(draftSim)
		}
		out.Changed = append(out.Changed, change)
		// A new phrase pulling a request the live bank placed confidently is
		// the change most likely to be a mistake.
		confident := from != logstore.ComplexitySuggestionUnmatchedTier && float64(liveSim) >= max(confidentSimilarity, s.MinSimilarity+confidentMargin)
		if confident && to != logstore.ComplexitySuggestionUnmatchedTier && !entries[draftBest].inLive {
			a := flagged[draftBest]
			if a == nil {
				a = &attention{from: map[string]int{}, example: r.LogID}
				flagged[draftBest] = a
			}
			a.count++
			a.from[from]++
		}
	}
	for i, a := range flagged {
		top, n := "", 0
		for tier, c := range a.from {
			if c > n || (c == n && tier < top) {
				top, n = tier, c
			}
		}
		out.Attention = append(out.Attention, ImpactAttention{Phrase: entries[i].text, Tier: entries[i].tier, FromTier: top, Requests: a.count, LogID: a.example})
	}
	sort.Slice(out.Attention, func(i, j int) bool {
		if out.Attention[i].Requests != out.Attention[j].Requests {
			return out.Attention[i].Requests > out.Attention[j].Requests
		}
		return out.Attention[i].Phrase < out.Attention[j].Phrase
	})
	return out, nil
}

// normalizeText is how phrases compare: the router lowercases and trims them.
func normalizeText(text string) string { return strings.ToLower(strings.TrimSpace(text)) }
