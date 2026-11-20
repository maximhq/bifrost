package suggestrun

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/maximhq/bifrost/plugins/routing/complexity/suggest"
)

// A decision-model router's run writes signals after its groups: each tier
// the run moved groups into is asked, once, for the traits those groups
// share (suggest.SuggestSignals). Each signal is its own suggestion row
// (logstore.ComplexitySuggestionCategorySignal), so it is accepted or
// dismissed on its own.
//
// Reconciliation is by tier and text, since a signal has no cluster to match
// by centroid: a run that writes a signal an open row holds refreshes that
// row; one a user accepted or dismissed is not written again (a dismissed
// signal is shown to the model as declined, so a rewording is dropped too);
// and an open signal row this run did not write again is superseded, unless
// its tier's call failed, in which case it is left for the next run.

// signalCallsPerRun bounds a run's signal calls: one per tier.
var signalCallsPerRun = len(complexity.OrderedTiers())

// signalGroup is one group behind a tier's signal step.
type signalGroup struct {
	suggestion suggest.Suggestion
	input      suggest.SignalGroup
}

// signalPlan is what one run's signal step wrote.
type signalPlan struct {
	writes                         []logstore.ComplexitySuggestionWrite
	created, refreshed, superseded int
	failed                         int   // tiers whose call failed
	first                          error // the first of those failures
}

// suggestSignals runs the signal step for each tier and reconciles what it
// wrote with the stored signal rows. suggestions are the run's, after the
// room was fitted; only actionable ones with examples count as groups moved
// into their tier.
func suggestSignals(ctx context.Context, emb suggest.Embedder, llm suggest.LLM, s Settings, suggestions []suggest.Suggestion, stored []logstore.ComplexitySuggestion, scope rowScope, now time.Time) signalPlan {
	tiers := complexity.OrderedTiers()
	groups := map[string][]signalGroup{}
	for _, sg := range suggestions {
		if !sg.Category.Actionable() || len(sg.Phrases) == 0 || sg.Cluster == nil {
			continue
		}
		requests := make([]string, 0, len(sg.Examples))
		for _, m := range sg.Examples {
			requests = append(requests, m.Text)
		}
		groups[sg.Tier] = append(groups[sg.Tier], signalGroup{suggestion: sg, input: suggest.SignalGroup{
			Examples: sg.Phrases, Requests: requests, Weight: sg.Cluster.Weight,
		}})
	}
	for _, gs := range groups {
		sort.SliceStable(gs, func(i, j int) bool { return gs[i].input.Weight > gs[j].input.Weight })
	}

	// The stored signal rows, by tier: open ones to refresh or supersede,
	// decided ones never to write again.
	open := map[string]map[string]logstore.ComplexitySuggestion{}
	decidedText := map[string]map[string]bool{}
	declined := map[string][]string{}
	for _, row := range stored {
		if row.Category != logstore.ComplexitySuggestionCategorySignal {
			continue
		}
		phrases := row.Phrases()
		if len(phrases) == 0 {
			continue
		}
		key := signalKey(phrases[0])
		switch {
		case row.Status == logstore.ComplexitySuggestionStatusOpen:
			if open[row.Tier] == nil {
				open[row.Tier] = map[string]logstore.ComplexitySuggestion{}
			}
			open[row.Tier][key] = row
		case decided(row.Status):
			if decidedText[row.Tier] == nil {
				decidedText[row.Tier] = map[string]bool{}
			}
			decidedText[row.Tier][key] = true
			if row.Status == logstore.ComplexitySuggestionStatusDismissed {
				declined[row.Tier] = append(declined[row.Tier], phrases[0])
			}
		}
	}

	results := make([]suggest.SignalResult, len(tiers))
	errs := make([]error, len(tiers))
	_ = suggest.ForEach(len(tiers), len(tiers), func(i int) error {
		tier := tiers[i]
		inputs := make([]suggest.SignalGroup, len(groups[tier]))
		for j, g := range groups[tier] {
			inputs[j] = g.input
		}
		results[i], errs[i] = suggest.SuggestSignals(ctx, llm, emb, suggest.SignalInput{
			Tier: tier, Definition: s.Definitions[tier], Signals: s.Signals[tier], Declined: declined[tier],
			Groups: inputs, Max: s.MaxSignalsPerTier - len(s.Signals[tier]),
		})
		return nil
	})

	var plan signalPlan
	for i, tier := range tiers {
		if errs[i] != nil {
			plan.failed++
			if plan.first == nil {
				plan.first = fmt.Errorf("%s: %w", tier, errs[i])
			}
			continue // its open rows are left for the next run
		}
		written := map[string]bool{}
		for _, signal := range results[i].Signals {
			key := signalKey(signal.Text)
			if decidedText[tier][key] {
				continue
			}
			row := logstore.ComplexitySuggestion{ID: uuid.NewString(), CreatedAt: now}
			from := ""
			if match, ok := open[tier][key]; ok {
				row.ID, row.CreatedAt, from = match.ID, match.CreatedAt, match.Status
				plan.refreshed++
			} else {
				plan.created++
			}
			written[key] = true
			fillSignal(&row, tier, signal, groups[tier], scope, now)
			plan.writes = append(plan.writes, logstore.ComplexitySuggestionWrite{Suggestion: row, From: from})
		}
		for key, row := range open[tier] {
			if !written[key] {
				plan.supersede(row, now)
			}
		}
	}
	// Open signal rows of a tier this router no longer has are superseded too.
	for tier, rows := range open {
		if !slices.Contains(tiers, tier) {
			for _, row := range rows {
				plan.supersede(row, now)
			}
		}
	}
	return plan
}

// supersedeSignals supersedes every open signal row: a run that writes no
// signals leaves none open.
func supersedeSignals(stored []logstore.ComplexitySuggestion, now time.Time) signalPlan {
	var plan signalPlan
	for _, row := range stored {
		if row.Category == logstore.ComplexitySuggestionCategorySignal && row.Status == logstore.ComplexitySuggestionStatusOpen {
			plan.supersede(row, now)
		}
	}
	return plan
}

func (p *signalPlan) supersede(row logstore.ComplexitySuggestion, now time.Time) {
	from := row.Status
	row.Status = logstore.ComplexitySuggestionStatusSuperseded
	row.ResolvedAt = &now
	p.writes = append(p.writes, logstore.ComplexitySuggestionWrite{Suggestion: row, From: from})
	p.superseded++
}

// fillSignal writes one signal into a row: its tier and text, and, from the
// groups it describes, their requests in all, how they route today, and some
// of their requests, each group's most typical first, as its examples.
func fillSignal(row *logstore.ComplexitySuggestion, tier string, signal suggest.Signal, groups []signalGroup, scope rowScope, now time.Time) {
	row.RouterID, row.RouterVersion = scope.routerID, scope.routerVersion
	row.Status = logstore.ComplexitySuggestionStatusOpen
	row.ResolvedAt = nil
	row.Tier, row.Category, row.Reason, row.Confidence = tier, logstore.ComplexitySuggestionCategorySignal, "", ""
	row.Coverage, row.NearestPhrase, row.NearestTier, row.NearestSimilarity, row.Centroid = 0, "", "", 0, nil
	row.EmbeddingSignature, row.RunID, row.LastSeenAt = scope.namespace, scope.runID, now
	_ = row.SetPhrases([]string{signal.Text})

	var requests int64
	routing := map[string]int64{}
	var examples [][]string
	for _, g := range signal.Groups {
		if g < 0 || g >= len(groups) {
			continue
		}
		sg := groups[g].suggestion
		requests += int64(sg.Cluster.Weight)
		for t, n := range currentRouting(sg.Cluster) {
			routing[t] += n
		}
		ids := make([]string, 0, len(sg.Examples))
		for _, m := range sg.Examples {
			ids = append(ids, m.ID)
		}
		examples = append(examples, ids)
	}
	row.RequestCount = requests
	_ = row.SetCurrentRouting(routing)
	_ = row.SetExampleLogIDs(interleave(examples, MaxExampleTestRequests))
}

// interleave takes the first of each list, then the second of each, and so
// on, up to n in all, so every group is represented however few are kept.
func interleave(lists [][]string, n int) []string {
	var out []string
	for i := 0; len(out) < n; i++ {
		added := false
		for _, list := range lists {
			if i < len(list) && len(out) < n {
				out = append(out, list[i])
				added = true
			}
		}
		if !added {
			break
		}
	}
	return out
}

// signalKey is how signal texts are compared across runs: case and spacing
// aside, word for word.
func signalKey(text string) string {
	return strings.ToLower(strings.Join(strings.Fields(text), " "))
}
