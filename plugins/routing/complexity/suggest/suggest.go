// Package suggest turns routed traffic into phrase-bank suggestions for the
// semantic complexity router.
//
// It clusters requests in the router's own embedding space, asks an LLM once
// per cluster for a tier and one or two reference phrases written as minimal
// edits of the cluster's most central request, removes request-specific
// material from those phrases, and checks each phrase against the cluster it
// came from before suggesting it: a phrase that would not take over routing
// for most of its cluster is sent back once with the requests it missed.
//
// The package does no I/O of its own. Request vectors come from the caller
// (the router already computed them), and embedding new phrases and calling
// the LLM go through the Embedder and LLM interfaces.
package suggest

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// Request is one distinct request text as the router embedded it.
type Request struct {
	ID     string    // identifies the request to the caller, e.g. a log ID
	Text   string    // the text the router embedded
	Count  int       // how many requests carried this exact text; <1 counts as 1
	Vector []float32 // unit length, in the router's embedding space
}

// Phrase is one phrase-bank entry with its vector.
type Phrase struct {
	Tier   string
	Text   string
	Vector []float32
}

// Message is one chat message sent to the LLM.
type Message struct {
	Role    string
	Content string
}

// Embedder embeds phrase texts in the router's embedding space and returns
// unit-length vectors in order.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// LLM completes a conversation and returns a JSON object as text.
type LLM interface {
	CompleteJSON(ctx context.Context, messages []Message) (string, error)
}

// Category says why a cluster is worth (or not worth) a suggestion.
type Category string

const (
	// CategoryUncovered: most of the cluster matches no live phrase above
	// the floor.
	CategoryUncovered Category = "uncovered"
	// CategoryWeak: it routes to the suggested tier, but on weak matches.
	CategoryWeak Category = "weak"
	// CategoryMisrouted: it routes confidently to a different tier.
	CategoryMisrouted Category = "misrouted"
	// CategoryMixed: today's routing splits the cluster across tiers.
	CategoryMixed Category = "mixed"
	// CategoryCovered: it already routes well to the suggested tier.
	CategoryCovered Category = "covered"
	// CategoryIncoherent: the LLM found several request types, or gave no
	// usable tier or phrase.
	CategoryIncoherent Category = "incoherent"
	// CategoryTemplate: the requests are one prompt template sent with
	// different fills; its phrases are written from the template's wording.
	CategoryTemplate Category = "template"
	// CategoryUnreadable: none of the cluster's examples could be read back
	// (their logs were deleted or hide their content), so it was not sent.
	CategoryUnreadable Category = "unreadable"
)

// Actionable reports whether a category is worth showing as a suggestion.
func (c Category) Actionable() bool {
	switch c {
	case CategoryUncovered, CategoryWeak, CategoryMisrouted, CategoryMixed, CategoryTemplate:
		return true
	}
	return false
}

// Options tunes a run. Zero fields take the defaults in withDefaults.
type Options struct {
	// MatchFloor is the similarity a phrase needs to count as a match when
	// deciding coverage and categories. The router's min_similarity may be
	// 0; this is the stricter "this phrase really describes it" bar.
	MatchFloor float32
	// MinCoverage is the share of its cluster a phrase set must take over
	// before it is accepted without a retry.
	MinCoverage float64
	// Representatives is how many requests per cluster the LLM sees.
	Representatives int
	// MaxPhrases is how many phrases one cluster may suggest.
	MaxPhrases int
	// DuplicateSimilarity drops a suggested phrase this similar to a live one.
	DuplicateSimilarity float32
	// Instructions replace the default suggestion instructions
	// (DefaultInstructions); empty means the default. The tier definitions
	// and the fixed JSON/injection reinforcement are added around them.
	Instructions string
	// Guidance is the tier definitions; empty means the router's default LLM
	// classifier guidance. Callers pass the configured fallback guidance so
	// both use one definition of the tiers.
	Guidance string
	// Workers is how many clusters are sent to the LLM at once.
	Workers int
	// Texts, when set, supplies the text of requests clustered without it,
	// keyed by Request.ID. It is asked only for the members a cluster shows
	// the LLM; a member it returns no text for is left out. Without it every
	// request must carry its Text.
	Texts   func(ctx context.Context, ids []string) (map[string]string, error)
	Cluster ClusterOptions
}

func (o Options) withDefaults() Options {
	if o.MatchFloor <= 0 {
		o.MatchFloor = 0.40
	}
	if o.MinCoverage <= 0 {
		o.MinCoverage = 0.6
	}
	if o.Representatives <= 0 {
		o.Representatives = 8
	}
	if o.MaxPhrases <= 0 {
		o.MaxPhrases = 2
	}
	if o.DuplicateSimilarity <= 0 {
		o.DuplicateSimilarity = 0.97
	}
	if o.Workers <= 0 {
		o.Workers = 4
	}
	o.Cluster = o.Cluster.withDefaults()
	return o
}

// Suggestion is the outcome for one cluster.
type Suggestion struct {
	Cluster    *Cluster
	Category   Category
	Tier       string
	Confidence string
	Reason     string
	// Phrases are the suggested phrase texts, safe to show and add.
	Phrases []string
	// Coverage is the request-weighted share of the cluster's kept members
	// the phrases would take over routing for.
	Coverage float64
	// Attempts is how many LLM calls the cluster took.
	Attempts int
	// Leaked are phrases the LLM wrote that carried request-specific
	// material and were rejected; Sanitized reports that Phrases came from
	// the deterministic fallback instead of the LLM's own rewrite.
	Leaked    []string
	Sanitized bool
	// Duplicates are phrases dropped as near-copies of a live phrase.
	Duplicates []string
	// Nearest is the live phrase closest to the cluster centre.
	Nearest    Phrase
	NearestSim float32
	// Examples are the requests the LLM saw, the medoid first.
	Examples []Member
	// Template is the opening the examples share when they are one prompt
	// template with different fills; empty otherwise.
	Template string

	vectors [][]float32
}

// Run clusters requests and suggests phrases for each cluster. Live phrases
// without vectors are embedded first. Suggestions come back heaviest cluster
// first, including non-actionable ones so callers can show why a cluster was
// not suggested.
func Run(ctx context.Context, reqs []Request, live []Phrase, emb Embedder, llm LLM, opts Options) ([]Suggestion, error) {
	opts = opts.withDefaults()
	live, err := embedLive(ctx, emb, live)
	if err != nil {
		return nil, err
	}
	c := NewClusterer(opts.Cluster, live, opts.MatchFloor)
	c.Add(reqs)
	return NewSuggester(live, emb, llm, opts).SuggestAll(ctx, c.Clusters())
}

func embedLive(ctx context.Context, emb Embedder, live []Phrase) ([]Phrase, error) {
	var texts []string
	var idx []int
	for i, p := range live {
		if len(p.Vector) == 0 {
			texts = append(texts, normalizePhrase(p.Text))
			idx = append(idx, i)
		}
	}
	if len(texts) == 0 {
		return live, nil
	}
	vecs, err := emb.Embed(ctx, texts)
	if err != nil {
		return nil, fmt.Errorf("embed live phrases: %w", err)
	}
	if len(vecs) != len(texts) {
		return nil, fmt.Errorf("embed live phrases: asked for %d, got %d", len(texts), len(vecs))
	}
	out := append([]Phrase(nil), live...)
	for j, i := range idx {
		out[i].Vector = normalize(vecs[j])
	}
	return out, nil
}

// Suggester asks the LLM about clusters and verifies its phrases.
type Suggester struct {
	live []Phrase
	emb  Embedder
	llm  LLM
	opts Options
}

// NewSuggester returns a suggester against the live phrase bank, whose
// phrases must carry vectors.
func NewSuggester(live []Phrase, emb Embedder, llm LLM, opts Options) *Suggester {
	return &Suggester{live: live, emb: emb, llm: llm, opts: opts.withDefaults()}
}

// SuggestAll suggests for every cluster, keeping their order.
func (s *Suggester) SuggestAll(ctx context.Context, clusters []*Cluster) ([]Suggestion, error) {
	out := make([]Suggestion, len(clusters))
	err := forEach(len(clusters), s.opts.Workers, func(i int) error {
		sg, err := s.Suggest(ctx, clusters[i])
		if err != nil {
			return err
		}
		out[i] = sg
		return nil
	})
	return out, err
}

// Suggest runs the LLM on one cluster. A phrase set that covers less than
// MinCoverage, or that only leaked, is sent back once with feedback; the
// better of the two attempts is kept.
func (s *Suggester) Suggest(ctx context.Context, cl *Cluster) (Suggestion, error) {
	reps, err := s.withTexts(ctx, representatives(cl, s.opts.Representatives))
	if err != nil {
		return Suggestion{}, err
	}
	if len(reps) == 0 {
		return Suggestion{Cluster: cl, Category: CategoryUnreadable}, nil
	}
	base := Suggestion{Cluster: cl, Examples: reps, Template: sharedTemplate(reps)}
	if i, sim := nearest(cl.Centroid, s.live); i >= 0 {
		base.Nearest, base.NearestSim = s.live[i], sim
	}
	messages := buildPrompt(s.opts.Instructions, s.opts.Guidance, reps, cl.Weight)
	if base.Template != "" {
		messages[1].Content += "\n" + templateNote(base.Template)
	}

	var best *Suggestion
	for attempt := 1; attempt <= 2; attempt++ {
		raw, err := s.llm.CompleteJSON(ctx, messages)
		if err != nil {
			return Suggestion{}, err
		}
		v, err := parseVerdict(raw, s.opts.MaxPhrases)
		if err != nil {
			// An unparseable answer is one failed attempt, not a failed run.
			if best == nil {
				b := base
				b.Attempts = attempt
				best = &b
			}
			continue
		}
		cand := base
		cand.Attempts = attempt
		cand.Tier, cand.Confidence, cand.Reason = v.Tier, v.Confidence, v.Reason
		kept, leaked := filterLeaks(v.Phrases)
		kept, leaked = withoutPasted(kept, leaked, base.Template, reps)
		cand.Leaked = leaked
		if !v.Coherent || !validTier(v.Tier) {
			cand.Tier = ""
			if best == nil || best.Tier == "" {
				best = &cand
			}
			break
		}
		if err := s.adopt(ctx, &cand, kept); err != nil {
			return Suggestion{}, err
		}
		// Prefer an attempt with phrases, then higher coverage; on a tie
		// keep the earlier one.
		if best == nil || best.Tier == "" || (len(cand.Phrases) > 0 && (len(best.Phrases) == 0 || cand.Coverage > best.Coverage)) {
			best = &cand
		}
		if len(cand.Phrases) > 0 && cand.Coverage >= s.opts.MinCoverage {
			break
		}
		if len(cand.Phrases) == 0 && len(leaked) == 0 {
			// Every phrase was a near-copy of a live one: the bank already
			// says this, so another attempt would only say it again.
			break
		}
		messages = append(messages, Message{Role: "assistant", Content: raw})
		if len(cand.Phrases) == 0 && len(leaked) > 0 {
			messages = append(messages, Message{Role: "user", Content: leakFeedback(leaked)})
		} else {
			missed, err := s.withTexts(ctx, s.missed(cl, cand.vectors, 5))
			if err != nil {
				return Suggestion{}, err
			}
			messages = append(messages, Message{Role: "user", Content: coverageFeedback(cand.Coverage, missed)})
		}
	}

	sg := *best
	if sg.Tier != "" && len(sg.Phrases) == 0 && len(sg.Leaked) > 0 {
		var cleaned []string
		for _, p := range sg.Leaked {
			if c := sanitize(p); c != "" && !leaks(c) && !pastedFrom(c, sg.Template, reps) {
				cleaned = append(cleaned, c)
			}
		}
		if err := s.adopt(ctx, &sg, cleaned); err != nil {
			return Suggestion{}, err
		}
		sg.Sanitized = len(sg.Phrases) > 0
	}
	sg.Category = s.categorize(&sg)
	// A template cluster is worth suggesting for whatever its routing is
	// today; saying it is a template is what tells the reviewer why its
	// phrase reads like the template's opening.
	if sg.Template != "" && sg.Category.Actionable() {
		sg.Category = CategoryTemplate
	}
	return sg, nil
}

// withTexts fills in the text of members clustered without it, through
// Options.Texts, and drops the members it cannot read.
func (s *Suggester) withTexts(ctx context.Context, members []Member) ([]Member, error) {
	if s.opts.Texts == nil {
		return members, nil
	}
	var ids []string
	for _, m := range members {
		if m.Text == "" {
			ids = append(ids, m.ID)
		}
	}
	if len(ids) == 0 {
		return members, nil
	}
	texts, err := s.opts.Texts(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]Member, 0, len(members))
	for _, m := range members {
		if m.Text == "" {
			m.Text = texts[m.ID]
		}
		if strings.TrimSpace(m.Text) != "" {
			out = append(out, m)
		}
	}
	return out, nil
}

// adopt embeds candidate phrases, drops near-copies of live phrases, and
// records what remains with its coverage.
func (s *Suggester) adopt(ctx context.Context, sg *Suggestion, phrases []string) error {
	sg.Phrases, sg.vectors, sg.Coverage = nil, nil, 0
	if len(phrases) == 0 {
		return nil
	}
	norm := make([]string, len(phrases))
	for i, p := range phrases {
		norm[i] = normalizePhrase(p)
	}
	vecs, err := s.emb.Embed(ctx, norm)
	if err != nil {
		return fmt.Errorf("embed suggested phrases: %w", err)
	}
	if len(vecs) != len(norm) {
		return fmt.Errorf("embed suggested phrases: asked for %d, got %d", len(norm), len(vecs))
	}
	liveText := make(map[string]bool, len(s.live))
	for _, p := range s.live {
		liveText[normalizePhrase(p.Text)] = true
	}
	for i, p := range norm {
		v := normalize(vecs[i])
		if _, sim := nearest(v, s.live); liveText[p] || (len(s.live) > 0 && sim >= s.opts.DuplicateSimilarity) {
			sg.Duplicates = append(sg.Duplicates, p)
			continue
		}
		if slices.Contains(sg.Phrases, p) {
			continue
		}
		sg.Phrases = append(sg.Phrases, p)
		sg.vectors = append(sg.vectors, v)
	}
	sg.Coverage = s.coverage(sg.Cluster, sg.vectors)
	return nil
}

// wins reports whether the candidate phrases would take over routing for a
// member: they clear the floor and beat the phrase that routes it today.
func (s *Suggester) wins(m Member, cands [][]float32) bool {
	best := float32(-2)
	for _, c := range cands {
		best = max(best, dot(m.Vector, c))
	}
	return best >= s.opts.MatchFloor && best > m.CurrentSim
}

func (s *Suggester) coverage(cl *Cluster, cands [][]float32) float64 {
	if len(cands) == 0 {
		return 0
	}
	var won, total int
	for _, m := range cl.Members {
		total += m.Count
		if s.wins(m, cands) {
			won += m.Count
		}
	}
	if total == 0 {
		return 0
	}
	return float64(won) / float64(total)
}

func (s *Suggester) missed(cl *Cluster, cands [][]float32, n int) []Member {
	var out []Member
	for _, m := range cl.Members {
		if !s.wins(m, cands) {
			out = append(out, m)
			if len(out) == n {
				break
			}
		}
	}
	return out
}

// categorize says why the cluster is (or is not) worth a suggestion, from how
// the whole cluster routes today. A cluster whose phrases were all near-copies
// of live phrases is covered, unless it reliably routes to a tier other than
// the suggested one: then the live phrase it copies carries the wrong tier.
func (s *Suggester) categorize(sg *Suggestion) Category {
	if sg.Tier == "" {
		return CategoryIncoherent
	}
	top, covered, reliable := topRoutedTier(sg.Cluster)
	if len(sg.Phrases) == 0 {
		if len(sg.Duplicates) == 0 {
			return CategoryIncoherent
		}
		if reliable && top != sg.Tier {
			return CategoryMisrouted
		}
		return CategoryCovered
	}
	switch {
	case !covered:
		return CategoryUncovered
	case !reliable:
		return CategoryMixed
	case top != sg.Tier:
		return CategoryMisrouted
	}
	var simSum float64
	var n int
	for _, m := range sg.Cluster.Members {
		simSum += float64(m.CurrentSim) * float64(m.Count)
		n += m.Count
	}
	if n > 0 && simSum/float64(n) >= float64(s.opts.MatchFloor)+0.10 {
		return CategoryCovered
	}
	return CategoryWeak
}

// topRoutedTier returns the tier that carries most of the cluster's routed
// requests today. covered reports that at least half the cluster's weight
// matches a live phrase above the floor; reliable additionally requires the
// heaviest tier to hold at least 70% of those matched requests.
func topRoutedTier(cl *Cluster) (tier string, covered, reliable bool) {
	cur := cl.CurrentRouting()
	total := cl.Weight
	matched := total - cur[""]
	if matched <= 0 || float64(matched) < 0.5*float64(total) {
		return "", false, false
	}
	var top int
	for t, n := range cur {
		if t == "" {
			continue
		}
		if n > top || (n == top && t < tier) {
			tier, top = t, n
		}
	}
	return tier, true, float64(top) >= 0.7*float64(matched)
}
