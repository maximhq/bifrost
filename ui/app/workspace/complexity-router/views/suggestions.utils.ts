import type { DecisionTier, EditableKeywordConfig, KeywordListKey, Suggestion, SuggestionCategory } from "@/lib/types/complexityRouter";

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