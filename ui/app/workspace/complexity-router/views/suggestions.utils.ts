import {
	COMPLEXITY_TIER_VALUES,
	SUGGESTION_RUN_WINDOW_DAYS,
	SUGGESTION_RUN_WINDOWS,
	UNMATCHED_TIER,
	type DecisionTier,
	type EditableKeywordConfig,
	type KeywordListKey,
	type Suggestion,
	type SuggestionCategory,
	type SuggestionRun,
	type SuggestionRunEstimate,
	type SuggestionRunWindow,
} from "@/lib/types/complexityRouter";

// Staging is how a suggestion reaches the phrase bank: its phrases are added to
// the draft lists, where they can be edited or taken back, and the page's Save
// applies them with every other edit. Once the save succeeds the staged
// suggestions are marked accepted, so later runs leave them alone.

export interface StagedSuggestion {
	id: string;
	tier: DecisionTier;
	// The phrases as they now stand in the draft, edits included.
	phrases: string[];
}

export const TIER_LIST_KEYS: Record<DecisionTier, KeywordListKey> = {
	SIMPLE: "simple_keywords",
	MEDIUM: "medium_keywords",
	COMPLEX: "complex_keywords",
};

// normalizePhrase is the gateway's phrase identity: it lowercases and trims
// before embedding, so two spellings that differ only in case are one phrase.
export function normalizePhrase(phrase: string): string {
	return phrase.trim().toLowerCase();
}

// bankPhrases is every phrase in the draft, normalized.
export function bankPhrases(keywords: EditableKeywordConfig): Set<string> {
	return new Set([...keywords.simple_keywords, ...keywords.medium_keywords, ...keywords.complex_keywords].map(normalizePhrase));
}

// newPhrases is what staging phrases would actually add: blanks and phrases
// already somewhere in the bank are left out, since the gateway would refuse a
// phrase listed under two tiers.
export function newPhrases(keywords: EditableKeywordConfig, phrases: string[]): string[] {
	const seen = bankPhrases(keywords);
	const out: string[] = [];
	for (const phrase of phrases) {
		const key = normalizePhrase(phrase);
		if (!key || seen.has(key)) continue;
		seen.add(key);
		out.push(phrase.trim());
	}
	return out;
}

// addPhrases appends phrases to a tier's list.
export function addPhrases(keywords: EditableKeywordConfig, tier: DecisionTier, phrases: string[]): EditableKeywordConfig {
	const key = TIER_LIST_KEYS[tier];
	return { ...keywords, [key]: [...keywords[key], ...phrases] };
}

// removePhrases takes phrases back out of a tier's list.
export function removePhrases(keywords: EditableKeywordConfig, tier: DecisionTier, phrases: string[]): EditableKeywordConfig {
	const key = TIER_LIST_KEYS[tier];
	const drop = new Set(phrases);
	return { ...keywords, [key]: keywords[key].filter((phrase) => !drop.has(phrase)) };
}

// replacePhrase swaps one staged phrase for its edited text in place.
export function replacePhrase(keywords: EditableKeywordConfig, tier: DecisionTier, from: string, to: string): EditableKeywordConfig {
	const key = TIER_LIST_KEYS[tier];
	return { ...keywords, [key]: keywords[key].map((phrase) => (phrase === from ? to : phrase)) };
}

// stagedPhraseCount is how many phrases staging has added to the draft.
export function stagedPhraseCount(staged: StagedSuggestion[]): number {
	return staged.reduce((sum, entry) => sum + entry.phrases.length, 0);
}

// stillStaged drops staged entries whose phrases are no longer in the draft,
// which happens when a phrase is removed from the list on the configuration
// tab: the suggestion is then no longer being accepted.
export function stillStaged(staged: StagedSuggestion[], keywords: EditableKeywordConfig): StagedSuggestion[] {
	return staged
		.map((entry) => {
			const list = new Set(keywords[TIER_LIST_KEYS[entry.tier]]);
			return { ...entry, phrases: entry.phrases.filter((phrase) => list.has(phrase)) };
		})
		.filter((entry) => entry.phrases.length > 0);
}

export interface SuggestionFilter {
	query: string;
	category: SuggestionCategory | "all";
}

// filterSuggestions narrows the list by a search over phrases and reason, and
// by category, keeping the gateway's order (largest cluster first).
export function filterSuggestions(suggestions: Suggestion[], { query, category }: SuggestionFilter, hidden: Set<string>): Suggestion[] {
	const needle = query.trim().toLowerCase();
	return suggestions.filter((suggestion) => {
		if (hidden.has(suggestion.id)) return false;
		if (category !== "all" && suggestion.category !== category) return false;
		if (!needle) return true;
		return [...suggestion.phrases, suggestion.reason ?? "", suggestion.nearest_phrase ?? ""].some((text) =>
			text.toLowerCase().includes(needle),
		);
	});
}

// categoryCounts counts suggestions per category, for the filter menu.
export function categoryCounts(suggestions: Suggestion[], hidden: Set<string>): Record<SuggestionCategory | "all", number> {
	const counts: Record<SuggestionCategory | "all", number> = { all: 0, uncovered: 0, weak: 0, misrouted: 0, mixed: 0, template: 0 };
	for (const suggestion of suggestions) {
		if (hidden.has(suggestion.id)) continue;
		counts.all += 1;
		if (suggestion.category in counts) counts[suggestion.category] += 1;
	}
	return counts;
}
// availableWindows lists the run windows that fit in the lookback: a longer
// one would read vectors that have already been swept.
export function availableWindows(lookbackDays: number): SuggestionRunWindow[] {
	return SUGGESTION_RUN_WINDOWS.filter((value) => SUGGESTION_RUN_WINDOW_DAYS[value] <= lookbackDays);
}

// canStartRun reports whether an estimate leaves a run anything to read: a
// request in the window, or a backfill from logs that could not be counted
// (the run plans it again and may find requests there).
export function canStartRun(estimate: SuggestionRunEstimate): boolean {
	return estimate.requests > 0 || estimate.backfill_unknown === true;
}

// proposedTier is the tier a suggestion proposes, or null when the tier
// model was not confident enough to propose one: the operator decides.
export function proposedTier(suggestion: Pick<Suggestion, "tier" | "confidence">): DecisionTier | null {
	if (suggestion.confidence?.toLowerCase() === "low") return null;
	return (COMPLEXITY_TIER_VALUES as readonly string[]).includes(suggestion.tier) ? suggestion.tier : null;
}

export interface RoutingShare {
	tier: DecisionTier | typeof UNMATCHED_TIER;
	count: number;
}

// routingSplit is where a group's requests route today, busiest tier first,
// with the requests no phrase matches last.
export function routingSplit(routing: Suggestion["current_routing"] | undefined): RoutingShare[] {
	const shares: RoutingShare[] = [];
	for (const tier of [...COMPLEXITY_TIER_VALUES, UNMATCHED_TIER] as const) {
		const count = routing?.[tier] ?? 0;
		if (count > 0) shares.push({ tier, count });
	}
	return shares.sort((a, b) => {
		if ((a.tier === UNMATCHED_TIER) !== (b.tier === UNMATCHED_TIER)) return a.tier === UNMATCHED_TIER ? 1 : -1;
		return b.count - a.count;
	});
}

// runInProgress reports whether a run has not ended yet.
export function runInProgress(run: Pick<SuggestionRun, "status"> | undefined): boolean {
	return run?.status === "queued" || run?.status === "running";
}

// runProgress is how far a running run has got, from 0 to 1: a fixed share
// per stage, with the suggesting stage, the long one, moving as its groups
// are asked about.
export function runProgress(run: Pick<SuggestionRun, "status" | "stage" | "clusters_asked" | "clusters_done">): number {
	if (run.status === "queued") return 0;
	switch (run.stage) {
		case "backfilling":
			return 0.02;
		case "reading":
			return 0.05;
		case "clustering":
			return 0.2;
		case "suggesting":
			return 0.3 + (run.clusters_asked > 0 ? 0.6 * Math.min(1, run.clusters_done / run.clusters_asked) : 0);
		case "saving":
			return 0.95;
		default:
			return 0;
	}
}

// runWindowDays is how many days of traffic a run read, at least one.
export function runWindowDays(run: Pick<SuggestionRun, "started_at" | "window_start">): number {
	const ms = new Date(run.started_at).getTime() - new Date(run.window_start).getTime();
	return Math.max(1, Math.round(ms / (24 * 60 * 60 * 1000)));
}

// The steps of a run's breakdown: requests narrowed to the requests in groups,
// then groups narrowed to the groups shown. A step that took nothing away is
// left out.
export type BreakdownStep =
	| "window"
	| "skipped"
	| "ungrouped"
	| "grouped"
	| "decided"
	| "overLimit"
	| "failed"
	| "covered"
	| "incoherent"
	| "unreadable"
	| "shown";

export interface BreakdownRow {
	step: BreakdownStep;
	value: number;
	// total starts a section, minus takes away from it, result is what is left.
	kind: "total" | "minus" | "result";
	unit: "requests" | "groups";
}

export function runBreakdown(run: SuggestionRun): BreakdownRow[] {
	const counted = run.requests_clustered + run.requests_assigned;
	const rows: BreakdownRow[] = [
		{ step: "window", value: run.requests_read, kind: "total", unit: "requests" },
		{ step: "skipped", value: run.requests_skipped, kind: "minus", unit: "requests" },
		{ step: "ungrouped", value: Math.max(0, counted - run.requests_grouped), kind: "minus", unit: "requests" },
		{ step: "grouped", value: run.clusters, kind: "result", unit: "groups" },
		{ step: "decided", value: run.clusters_decided, kind: "minus", unit: "groups" },
		{ step: "overLimit", value: run.clusters_over_limit, kind: "minus", unit: "groups" },
		{ step: "failed", value: run.clusters_failed, kind: "minus", unit: "groups" },
		{ step: "covered", value: run.clusters_covered, kind: "minus", unit: "groups" },
		{ step: "incoherent", value: run.clusters_incoherent, kind: "minus", unit: "groups" },
		{ step: "unreadable", value: run.clusters_unreadable, kind: "minus", unit: "groups" },
		{ step: "shown", value: run.created + run.refreshed, kind: "result", unit: "groups" },
	];
	return rows.filter((row) => row.kind !== "minus" || row.value > 0);
}

// stagedAdditions is the staged suggestions' phrases by tier, as an impact
// replay takes them: added to the router's saved phrases for the replay only.
export function stagedAdditions(staged: StagedSuggestion[]): Partial<Record<DecisionTier, string[]>> {
	const out: Partial<Record<DecisionTier, string[]>> = {};
	for (const entry of staged) {
		const phrases = entry.phrases.map((text) => text.trim()).filter((text) => text !== "");
		if (phrases.length > 0) out[entry.tier] = [...(out[entry.tier] ?? []), ...phrases];
	}
	return out;
}