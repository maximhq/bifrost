/**
 * Complexity Router Type Definitions
 * Mirrors the AnalyzerConfig shape exchanged with /governance/complexity-analyzer-config.
 */

import i18n from "@/lib/i18n";
import type { RoutingFallbackWire, RoutingRule } from "@/lib/types/routingRules";
import type { ParseKeys } from "i18next";
import type { RuleGroupType } from "react-querybuilder";

// Display text below resolves through i18n when read (getters), so these shared constants
// follow the active locale for every consumer.
const tModels = (key: ParseKeys<"models">): string => i18n.t(key, { ns: "models" });
const tObservability = (key: ParseKeys<"observability">): string => i18n.t(key, { ns: "observability" });

export interface EditableKeywordConfig {
	simple_keywords: string[];
	medium_keywords: string[];
	complex_keywords: string[];
}

export type SemanticVectorStore = "embedded" | "vector_store";

// Mirrors ComplexitySemanticFallback* in framework/configstore: what answers
// when semantic classification produces no tier. An absent field on the wire
// means "none".
export type ClassifierMode = "semantic" | "decision" | "llm";
export type SemanticFallback = "none" | "llm" | "decision";

export type DecisionTier = (typeof COMPLEXITY_TIER_VALUES)[number];

// One tier's editable decision-model criteria. An omitted field means the gateway sends
// its shipped default for that field.
export interface DecisionTierCriteria {
	definition?: string;
	signals?: string[];
	examples?: string[];
}

export interface DecisionConfig {
	// The decision model to call. Both omitted selects the gateway default
	// (DEFAULT_DECISION_PROVIDER/MODEL); otherwise both are set.
	provider?: string;
	model?: string;
	previous_message_count: number;
	timeout?: string;
	// The confidence an answer must reach to publish its tier. 0 or omitted keeps every answer.
	min_confidence?: number;
	criteria?: Partial<Record<DecisionTier, DecisionTierCriteria>>;
}

// The shipped decision-model guidance, served by the status endpoint.
export interface DecisionGuidanceDefaults {
	criteria: Record<DecisionTier, Required<DecisionTierCriteria>>;
}

export interface LLMConfig {
	provider: string;
	model: string;
	timeout?: string;
	// Replaces the shipped classification guidance when set; empty means the
	// shipped guidance is used. The tier-name and response-format
	// reinforcement is appended by the gateway either way and is not
	// editable or exposed.
	prompt?: string;
	// How many of the most recent user messages are given to the classifier.
	// 1 (the default) sends only the latest message.
	message_history_count?: number;
	count_toward_budgets?: boolean;
}

export interface SemanticConfig {
	provider: string;
	embedding_model: string;
	timeout?: string;
	// Similarity floor the nearest reference phrase must clear; below it no tier
	// is published. 0 means the nearest phrase always wins.
	min_similarity?: number;
	// How many of the most recent user messages are combined into the embedded
	// text. 1 (the default) embeds only the latest message.
	message_history_count?: number;
	count_toward_budgets?: boolean;
	vector_store?: SemanticVectorStore;
	fallback?: SemanticFallback;
}

export interface SessionConfig {
	enabled: boolean;
}

// Phrase suggestions: samples classified requests' vectors so suggestions can
// cluster real traffic. Edited through config.json and the API until the
// suggestions page exists; the complexity router page carries it through
// saves unchanged.
// Runs start at start_at and repeat every interval after it; start_at sets
// the time of day.
export interface SuggestionsSchedule {
	every?: string; // "6h", "1d", "1w"; default "1d"
	start_at?: string; // RFC 3339 first run; default 1970-01-01T03:00:00Z
}

export interface SuggestionsConfig {
	enabled: boolean;
	sample_rate?: number;
	lookback_days?: number;
	schedule?: SuggestionsSchedule;
	max_clustered_requests?: number;
	min_cluster_requests?: number;
	provider?: string;
	model?: string;
	instructions?: string;
	tier?: SuggestionsTierConfig;
}

// The classifiers a suggestion run can decide tiers with. Phrases are always
// written by an LLM.
export const SUGGESTION_TIER_CLASSIFIERS = ["llm", "decision"] as const;
export type SuggestionTierClassifier = (typeof SUGGESTION_TIER_CLASSIFIERS)[number];

// The tier step's text. Left out, a run generates it from the router (its
// tier definitions, its own phrases as examples); once set, it is sent as
// written. prompt is the llm's whole prompt; instructions and criteria are the
// decision model's question and per-tier choices.
export interface SuggestionsTierConfig {
	classifier: SuggestionTierClassifier;
	provider?: string;
	model?: string;
	prompt?: string;
	instructions?: string;
	criteria?: Partial<Record<DecisionTier, SuggestionTierCriteria>>;
}

export interface SuggestionTierCriteria {
	definition: string;
	signals?: string[];
	examples?: string[];
}

// What a router's tier step sends while nothing is edited, rendered by the
// gateway from the saved router.
export interface SuggestionTierDefaults {
	prompt: string;
	criteria: Record<DecisionTier, { definition: string; signals: string[]; examples: string[] }>;
}

export const MAX_SUGGESTION_TIER_PROMPT_CHARACTERS = 16000;

// The llm classifier has no warmup: it holds no corpus and makes its first
// provider call on the first classified request, so it is simply ready the
// moment its block is saved.
export interface LLMStatusInfo {
	state: "disabled" | "ready";
}

export interface SemanticStatusInfo {
	state: "disabled" | "warming" | "ready" | "failed";
	// Whether classified requests can be captured for suggestions: they need a
	// shared vector store, not Chromem (each gateway's own memory) or none.
	capture_available?: boolean;
	capture_unavailable_reason?: "no_vector_store" | "embedded_vector_store";
	loaded: number;
	total: number;
	serving_previous?: boolean;
	failure_reason?:
		| "authentication"
		| "model_unavailable"
		| "rate_limited"
		| "timeout"
		| "provider_unavailable"
		| "vector_store_unavailable"
		| "invalid_response"
		| "unknown";
	error?: string;
	// How many phrase vectors the gateway currently holds for the configured
	// provider/model. The cache is in-process only — vectors cannot be read back
	// out of a vector store — so a restart empties it while the saved phrases look
	// unchanged. Reuse cannot be inferred from the persisted config alone; zero
	// means the next save re-embeds every phrase regardless of what changed.
	cached_phrases?: number;
	// Where phrase vectors actually live, which is not always what was chosen:
	// "vector_store" falls back to "embedded" whenever no vector store is
	// configured on the gateway. Absent until the gateway has resolved one.
	storage_mode?: "embedded" | "vector_store";
	// The namespace the serving generation queries. The vector store holds no
	// phrase text, so this is the only handle on the records this classifier
	// owns there. Absent while nothing is serving.
	namespace?: string;
	llm?: LLMStatusInfo;
	// The shipped classification guidance, served so the prompt editor can
	// seed itself and offer a reset without holding a copy that drifts from
	// the gateway's. The fixed reinforcement is never exposed.
	llm_default_prompt?: string;
	// The shipped decision-model per-tier criteria, served for the same reason as
	// llm_default_prompt.
	decision_defaults?: DecisionGuidanceDefaults;
	// The shipped phrase-suggestion instructions, for the instructions editor.
	suggestion_default_instructions?: string;
	suggestion_default_tier_instructions?: string;
	suggestion_default_tier_prompt?: string;
	suggestion_default_tier_criteria?: SuggestionTierDefaults["criteria"];
}

export interface AnalyzerConfig {
	keywords: EditableKeywordConfig;
	classifier?: ClassifierMode;
	decision?: DecisionConfig;
	semantic?: SemanticConfig;
	// The fallback classifier, engaged only when semantic.fallback selects
	// "llm". May be present while the fallback says "none": the block is
	// retained so toggling the fallback never loses settings.
	llm?: LLMConfig;
	// When enabled, one scoped session retains its highest observed tier for a
	// fixed 24-hour inactivity window. The gateway owns that policy; there are no
	// client-tunable thresholds or downgrade controls.
	session?: SessionConfig;
	suggestions?: SuggestionsConfig;
}

// A named model router: its identity, the classifier configuration the
// Complexity Router page edits (phrase suggestions included), the model each
// tier gets, and the requests it takes.
// Targets and routing are read-only on the router itself: each is replaced
// through its own endpoint.
export interface ModelRouter extends AnalyzerConfig {
	id: string;
	name: string;
	description?: string;
	targets?: ModelRouterTarget[];
	routing: ModelRouterRouting;
}

// What a new router starts from, before anything is saved.
export interface ModelRouterDefaults {
	config: AnalyzerConfig;
	decision_defaults: DecisionGuidanceDefaults;
	llm_default_prompt: string;
}

export interface ModelRouterList {
	routers: ModelRouter[];
	count: number;
}

export type KeywordListKey = keyof EditableKeywordConfig;

export const COMPLEXITY_TIER_VALUES = ["SIMPLE", "MEDIUM", "COMPLEX"] as const;

// REASONING was merged into COMPLEX and survives only in historical log rows.
// Kept out of COMPLEXITY_TIER_VALUES so the CEL builder never offers it; the
// logs filter renders it separately so those old rows stay reachable.
export const LEGACY_COMPLEXITY_TIER_VALUES = ["REASONING"] as const;

// Mirrors the complexity_mechanism values recorded by the gateway (plugins/governance/complexity).
// "skipped" means classification was demanded by a routing rule but produced no tier.
// The filter offers exactly these, with no legacy entry alongside them (unlike
// LEGACY_COMPLEXITY_TIER_VALUES): the complexity_mechanism column ships with the
// semantic classifier, so no row was ever written with the retired "lexical"
// mechanism and filtering on it could only ever return nothing.
export const COMPLEXITY_MECHANISM_VALUES = ["semantic", "decision", "llm", "session", "skipped"] as const;

// Labels cover "lexical" even though nothing filters on it. Rows predating the
// structured columns record their decision only in the prose routing log, and
// deriveComplexityRouting (logs/sheets/logDetailView.tsx) reconstructs those as
// "lexical" — the classifier that actually wrote them — for the detail view.
export const COMPLEXITY_MECHANISM_LABELS: Record<string, string> = {
	get lexical() {
		return tObservability("labels.complexityMechanism.lexical");
	},
	get semantic() {
		return tObservability("labels.semantic");
	},
	get decision() {
		return tObservability("labels.complexityMechanism.decision");
	},
	get llm() {
		return tObservability("labels.complexityMechanism.llm");
	},
	get session() {
		return tObservability("labels.session");
	},
	get skipped() {
		return tObservability("labels.complexityMechanism.skipped");
	},
};

// One card per tier in the Phrase to Tier Mapping section.
//
// The descriptions anchor on the model the tier should route to, not on what
// makes a request "simple" or "complex". That keeps the judgment with the
// operator — it is their model lineup and their cost tolerance — while still
// giving them something to sort against. Describing the tiers themselves put
// Bifrost in charge of the definition, and restating the tier name back at them
// ("phrases you deem simple") is not a description at all: the three cards have
// to differ in something the operator can act on.
export const TIER_PHRASE_LIST_DEFINITIONS: Array<{
	key: KeywordListKey;
	label: string;
	description: string;
}> = [
	{
		key: "simple_keywords",
		get label() {
			return tModels("routing.complexityUi.tierSimple");
		},
		get description() {
			return tModels("routing.complexityUi.tierSimpleDesc");
		},
	},
	{
		key: "medium_keywords",
		get label() {
			return tModels("routing.complexityUi.tierMedium");
		},
		get description() {
			return tModels("routing.complexityUi.tierMediumDesc");
		},
	},
	{
		key: "complex_keywords",
		get label() {
			return tModels("routing.complexityUi.tierComplex");
		},
		get description() {
			return tModels("routing.complexityUi.tierComplexDesc");
		},
	},
];

// Mirrors classifier timeout and history defaults in framework/configstore.
export const DEFAULT_SEMANTIC_TIMEOUT_MS = 1500;
export const DEFAULT_DECISION_TIMEOUT_MS = 1500;
export const DEFAULT_DECISION_PREVIOUS_MESSAGE_COUNT = 1;
export const MAX_DECISION_PREVIOUS_MESSAGE_COUNT = 5;
export const DEFAULT_DECISION_MIN_CONFIDENCE = 0;

// Server-side bounds from validateComplexityDecisionGuidance in framework/configstore.
export const MAX_DECISION_DEFINITION_CHARACTERS = 500;
export const MAX_DECISION_CRITERIA_ITEMS = 12;
export const MAX_DECISION_CRITERIA_ITEM_CHARACTERS = 300;

// The timeout is stored as a Go time.Duration — int64 nanoseconds — so this is
// the largest whole millisecond value time.ParseDuration accepts. One more and
// it does not parse to a very long timeout, it fails outright, which the form
// should catch rather than the save returning a 400.
export const MAX_SEMANTIC_TIMEOUT_MS = 9223372036854;

// Server-side bounds from validateComplexitySemanticPhrases. Enforced here too
// so an over-long phrase fails in the form instead of as an opaque 400.
export const MIN_SEMANTIC_MESSAGE_HISTORY = 1;
export const MAX_SEMANTIC_MESSAGE_HISTORY = 10;
export const MAX_SEMANTIC_PHRASE_CHARACTERS = 2000;
export const MAX_SEMANTIC_PHRASES = 750;

// DEFAULT_DECISION_PROVIDER and DEFAULT_DECISION_MODEL mirror the gateway's
// default decision model (DefaultComplexityDecisionProvider/Model in framework/configstore).
export const DEFAULT_DECISION_PROVIDER = "typesafe";
export const DEFAULT_DECISION_MODEL = "jev-latest";

// The checkpoints self-hosted decision models serve, grouped by model. Offered
// when a custom provider cannot list its own models (Laya has no model listing,
// and a Clef served by Ollama has no Cloudflare URL to read its model from), so
// the operator picks a real checkpoint instead of typing its name. Clef starts on
// the smaller Flash model, the one a typical self-hosted box actually has pulled.
export const SELF_HOSTED_DECISION_MODELS = [
	{ label: "Laya", models: ["english", "multilingual", "typed-decisions"] }, // i18n-ignore (model family name)
	{ label: "Nimble", models: ["nimble-latest", "bespokelabs/Bespoke-Nimble-9B"] }, // i18n-ignore (model family name)
	{ label: "Clef", models: ["clef-flash", "clef"] }, // i18n-ignore (model family name)
] as const;

// OPENROUTER_DECISION_MODEL is the model a newly selected OpenRouter provider starts on.
// The picker lists every OpenRouter model the datasheet marks as serving decisions.
export const OPENROUTER_DECISION_MODEL = "~typesafe/jev-latest";

// OPENAI_DECISION_MODEL is the model a newly selected OpenAI provider starts on.
// The picker lists every model the datasheet marks as serving decisions.
export const OPENAI_DECISION_MODEL = "gpt-6-luna";

// PERPLEXITY_DECISION_MODEL is the model a newly selected Perplexity provider starts on:
// the alias that follows Perplexity's newest decider. The picker lists the decider
// models Perplexity serves.
export const PERPLEXITY_DECISION_MODEL = "pplx-decider-latest";

// AZURE_DECISION_MODEL is the model a newly selected Azure provider starts on: the model
// name a key alias maps to the user's deployment. The picker lists Microsoft's decision models.
export const AZURE_DECISION_MODEL = "Microsoft-Decision-1";

// DATABRICKS_DECISION_MODEL is the model a newly selected Databricks provider starts on:
// the ai_decide AI Function. The picker also lists the System One models.
export const DATABRICKS_DECISION_MODEL = "ai_decide";

// GEMINI_DECISION_MODEL is the model a newly selected Gemini provider starts on.
// The picker lists the Gemini Decision models.
export const GEMINI_DECISION_MODEL = "gemini-decision-1";

// CLOUDFLARE_DECISION_MODEL is the model a newly selected Cloudflare provider starts on.
// The picker lists the Clef models Cloudflare Workers AI serves.
export const CLOUDFLARE_DECISION_MODEL = "@cf/cloudflare/clef-flash";

// DEFAULT_DECISION_CONFIG supplies the model, history window, timeout, and confidence floor for decision-model requests.
export const DEFAULT_DECISION_CONFIG: Required<
	Pick<DecisionConfig, "provider" | "model" | "previous_message_count" | "timeout" | "min_confidence">
> = {
	provider: DEFAULT_DECISION_PROVIDER,
	model: DEFAULT_DECISION_MODEL,
	previous_message_count: DEFAULT_DECISION_PREVIOUS_MESSAGE_COUNT,
	timeout: `${DEFAULT_DECISION_TIMEOUT_MS}ms`,
	min_confidence: DEFAULT_DECISION_MIN_CONFIDENCE,
};

// Seeded when a deployment has no semantic block saved yet. Provider and model
// stay blank because only the operator knows them.
export const DEFAULT_SEMANTIC_CONFIG: SemanticConfig = {
	provider: "",
	embedding_model: "",
	timeout: `${DEFAULT_SEMANTIC_TIMEOUT_MS}ms`,
	min_similarity: 0,
	message_history_count: 1,
	count_toward_budgets: false,
	vector_store: "embedded",
	fallback: "none",
};

// These are the wire values, not display-only aliases: config.json, the Helm
// chart, and the governance API all take the same two strings.
export const SEMANTIC_VECTOR_STORE_OPTIONS: Array<{ value: SemanticVectorStore; label: string; tooltip: string }> = [
	{
		value: "embedded",
		get label() {
			return tModels("routing.complexityUi.vectorStoreEmbedded");
		},
		get tooltip() {
			return tModels("routing.complexityUi.vectorStoreEmbeddedTip");
		},
	},
	{
		value: "vector_store",
		get label() {
			return tModels("routing.complexityUi.vectorStoreExternal");
		},
		get tooltip() {
			return tModels("routing.complexityUi.vectorStoreExternalTip");
		},
	},
];

export const SEMANTIC_STATUS_LABELS: Record<SemanticStatusInfo["state"], string> = {
	get disabled() {
		return tModels("routing.disabled");
	},
	get warming() {
		return tModels("routing.complexityUi.statusWarming");
	},
	get ready() {
		return tModels("routing.complexityUi.statusReady");
	},
	get failed() {
		return tModels("routing.complexityUi.statusFailed");
	},
};

// Duration strings round-trip through the API as Go durations ("500ms"), but the
// form edits milliseconds. Anything unparseable falls back to the default rather
// than silently sending 0, which the server rejects.
const DURATION_UNIT_TO_MS: Record<string, number> = { ns: 1e-6, us: 1e-3, µs: 1e-3, ms: 1, s: 1000, m: 60000, h: 3600000 };
const DURATION_SEGMENT = /([0-9]*\.?[0-9]+)(ns|us|µs|ms|s|m|h)/g;

export function parseSemanticTimeoutMs(timeout: string | undefined): number {
	if (!timeout) return DEFAULT_SEMANTIC_TIMEOUT_MS;
	const trimmed = timeout.trim();
	// Durations are summed segment by segment because time.Duration.String() — what the
	// API marshals with — compounds anything from a minute up: 60000ms comes back as
	// "1m0s", not "60s". Matching a single unit would send every such value to the
	// fallback below and silently reset the field to the default.
	const segments = [...trimmed.matchAll(DURATION_SEGMENT)];
	const consumed = segments.reduce((total, segment) => total + segment[0].length, 0);
	// Every character has to belong to a segment, so "1m30x" or a stray sign is rejected
	// here rather than being read as the part that happened to parse.
	if (segments.length === 0 || consumed !== trimmed.length) {
		const numeric = Number(trimmed);
		return Number.isFinite(numeric) && numeric > 0 ? numeric : DEFAULT_SEMANTIC_TIMEOUT_MS;
	}
	const milliseconds = segments.reduce((total, [, value, unit]) => total + Number(value) * DURATION_UNIT_TO_MS[unit], 0);
	return Number.isFinite(milliseconds) && milliseconds > 0 ? milliseconds : DEFAULT_SEMANTIC_TIMEOUT_MS;
}

export function formatSemanticTimeout(milliseconds: number): string {
	const inRange = Number.isFinite(milliseconds) && milliseconds > 0 && milliseconds <= MAX_SEMANTIC_TIMEOUT_MS;
	const safe = inRange ? milliseconds : DEFAULT_SEMANTIC_TIMEOUT_MS;
	return `${safe}ms`;
}

// Mirrors DefaultComplexityLLMTimeout in framework/configstore.
export const DEFAULT_LLM_TIMEOUT_MS = 4000;

// Server-side bounds from ComplexityLLMConfig.Validate. Enforced here too so
// an over-long prompt fails in the form instead of as an opaque 400.
export const MIN_LLM_MESSAGE_HISTORY = 1;
export const MAX_LLM_MESSAGE_HISTORY = 10;
export const MAX_LLM_PROMPT_CHARACTERS = 4000;

// Seeded when a deployment has no llm block saved yet. Provider and model stay
// blank because only the operator knows them; prompt stays blank because the
// editor seeds it from the gateway-served default.
export const DEFAULT_LLM_CONFIG: LLMConfig = {
	provider: "",
	model: "",
	timeout: `${DEFAULT_LLM_TIMEOUT_MS}ms`,
	prompt: "",
	message_history_count: 1,
	count_toward_budgets: false,
};

// These are the wire values, not display aliases: config.json and the
// governance API take the same two strings.
export const SEMANTIC_FALLBACK_OPTIONS: Array<{ value: SemanticFallback; label: string; description: string }> = [
	{
		value: "none",
		get label() {
			return tModels("routing.complexityUi.fallbackNone");
		},
		get description() {
			return tModels("routing.complexityUi.fallbackNoneDesc");
		},
	},
	{
		value: "llm",
		get label() {
			return tModels("routing.complexityUi.fallbackLlm");
		},
		get description() {
			return tModels("routing.complexityUi.fallbackLlmDesc");
		},
	},
	{
		value: "decision",
		get label() {
			return tModels("routing.complexityUi.classifierNameDecision");
		},
		get description() {
			return tModels("routing.complexityUi.fallbackDecisionDesc");
		},
	},
];

export const SEMANTIC_FALLBACK_LABELS: Record<SemanticFallback, string> = {
	get none() {
		return tModels("routing.complexityUi.fallbackNone");
	},
	get llm() {
		return tModels("routing.complexityUi.fallbackLlm");
	},
	get decision() {
		return tModels("routing.complexityUi.classifierNameDecision");
	},
};

// Same duration round-trip as parseSemanticTimeoutMs, with the llm default.
export function parseLLMTimeoutMs(timeout: string | undefined): number {
	if (!timeout) return DEFAULT_LLM_TIMEOUT_MS;
	const match = timeout.trim().match(/^([0-9]*\.?[0-9]+)(ns|us|µs|ms|s|m|h)$/);
	if (!match) {
		const numeric = Number(timeout);
		return Number.isFinite(numeric) && numeric > 0 ? numeric : DEFAULT_LLM_TIMEOUT_MS;
	}
	const value = Number(match[1]);
	const unitToMs: Record<string, number> = { ns: 1e-6, us: 1e-3, µs: 1e-3, ms: 1, s: 1000, m: 60000, h: 3600000 };
	const milliseconds = value * unitToMs[match[2]];
	return Number.isFinite(milliseconds) && milliseconds > 0 ? milliseconds : DEFAULT_LLM_TIMEOUT_MS;
}
// Phrase suggestions: written by scheduled runs over a sample of routed
// traffic, staged into the phrase lists by the user, and marked accepted once
// the staged phrases are saved.

export const SUGGESTION_STATUSES = ["open", "accepted", "dismissed", "superseded"] as const;
export type SuggestionStatus = (typeof SUGGESTION_STATUSES)[number];

// Why a cluster of traffic needs a phrase: nothing matches it, it matches
// weakly, it routes confidently to another tier, its routing is split, or it
// is one prompt template sent with different fills.
export const SUGGESTION_CATEGORIES = ["uncovered", "weak", "misrouted", "mixed", "template"] as const;
export type SuggestionCategory = (typeof SUGGESTION_CATEGORIES)[number];

// An interval's units and how many of each the gateway accepts: hours up to
// a day, days up to a month, weeks up to four, each a fixed duration.
export const SUGGESTION_EVERY_UNITS = ["h", "d", "w"] as const;
export type SuggestionEveryUnit = (typeof SUGGESTION_EVERY_UNITS)[number];
export const MAX_SUGGESTION_EVERY: Record<SuggestionEveryUnit, number> = { h: 23, d: 30, w: 4 };
export const DEFAULT_SUGGESTION_SAMPLE_RATE = 0.1;
export const DEFAULT_SUGGESTION_LOOKBACK_DAYS = 7;
export const MAX_SUGGESTION_LOOKBACK_DAYS = 90;
export const MAX_SUGGESTION_INSTRUCTIONS_CHARACTERS = 8000;
export const DEFAULT_SUGGESTION_EVERY = "1d";
// The gateway's default first run; runs repeat every interval from it.
export const DEFAULT_SUGGESTION_START_AT = "1970-01-01T03:00:00Z";
// The intervals the gateway accepts, as config.json and the API spell them.
export const SUGGESTION_EVERY_PATTERN = /^([1-9]|1[0-9]|2[0-3])h$|^([1-9]|[12][0-9]|30)d$|^[1-4]w$/;

// Run windows offered for "Run now"; the ones longer than the lookback are
// left out, since the vectors they would read are gone.
export const SUGGESTION_RUN_WINDOWS = ["24h", "7d", "14d", "30d"] as const;
export type SuggestionRunWindow = (typeof SUGGESTION_RUN_WINDOWS)[number];
export const SUGGESTION_RUN_WINDOW_DAYS: Record<SuggestionRunWindow, number> = { "24h": 1, "7d": 7, "14d": 14, "30d": 30 };

export interface Suggestion {
	id: string;
	router_id: string;
	// The router version it was computed against; stale when that is not the
	// version served now.
	router_version: string;
	stale: boolean;
	status: SuggestionStatus;
	tier: DecisionTier;
	phrases: string[];
	category: SuggestionCategory;
	reason?: string;
	confidence?: string;
	request_count: number;
	coverage: number;
	nearest_phrase?: string;
	nearest_tier?: string;
	nearest_similarity: number;
	example_log_ids: string[];
	// How many of the group's requests route to each tier today; "none"
	// counts those no phrase matches. Empty for a suggestion written before
	// it was recorded.
	current_routing: Partial<Record<DecisionTier | typeof UNMATCHED_TIER, number>>;
	embedding_signature: string;
	run_id: string;
	last_seen_at: string;
	resolved_at?: string;
	created_at: string;
	updated_at: string;
}

// The key a group's routing counts and a replay's tiers use for requests no
// phrase matches.
export const UNMATCHED_TIER = "none";

export const SUGGESTION_RUN_STAGES = ["backfilling", "reading", "clustering", "suggesting", "saving"] as const;
export type SuggestionRunStage = (typeof SUGGESTION_RUN_STAGES)[number];

export interface SuggestionRun {
	id: string;
	router_id: string;
	router_version: string;
	window_start: string;
	triggered_by: "schedule" | "manual";
	status: "queued" | "running" | "succeeded" | "failed" | "skipped" | "cancelled";
	// How far a running run got; absent once it ends.
	stage?: SuggestionRunStage;
	model?: string;
	prompt_hash?: string;
	requests_read: number;
	// How the window became suggestions, in order: the sample groups were
	// formed from, what was counted in after it, and what was past the bound;
	// the requests in groups big enough to keep.
	requests_clustered: number;
	requests_assigned: number;
	requests_skipped: number;
	requests_grouped: number;
	clusters: number;
	// What became of the groups: decided ones are not asked again, ones past
	// the run's limits (groups asked, room in the phrase lists) wait, and of
	// those asked some failed, were already covered, were not one request
	// type, or could not be read.
	clusters_decided: number;
	clusters_over_limit: number;
	clusters_asked: number;
	clusters_done: number;
	clusters_failed: number;
	clusters_covered: number;
	clusters_incoherent: number;
	clusters_unreadable: number;
	created: number;
	refreshed: number;
	superseded: number;
	cost: number;
	error?: string;
	started_at: string;
	finished_at?: string;
}

export interface SuggestionsList {
	router_id: string;
	router_version?: string;
	suggestions: Suggestion[];
	// How many suggestions the status filter selects across every page;
	// limit and offset are the page served.
	total: number;
	limit: number;
	offset: number;
	counts: Partial<Record<SuggestionStatus, number>>;
	enabled: boolean;
	last_run?: SuggestionRun;
	next_run_at?: string;
}

export interface SuggestionExample {
	log_id: string;
	// Absent for a log that was deleted, hides its content, or that the
	// caller cannot read.
	text?: string;
}

export interface SuggestionDetail {
	suggestion: Suggestion;
	examples: SuggestionExample[];
}

export interface SuggestionImpactChange {
	log_id: string;
	from: string;
	to: string;
	phrase: string;
	similarity: number;
	text?: string;
}

export interface SuggestionImpactAttention {
	phrase: string;
	tier: string;
	from_tier: string;
	requests: number;
	log_id: string;
	text?: string;
}

export interface SuggestionImpactTier {
	tier: string;
	live: number;
	draft: number;
	model?: { provider: string; model: string };
}

// What a draft phrase bank would do to a router's recent captured traffic.
// Costs are dollars over the window, for the changed requests only.
export interface SuggestionImpact {
	window_seconds: number;
	requests: number;
	total: number;
	tiers: SuggestionImpactTier[];
	changed_count: number;
	changed: SuggestionImpactChange[];
	attention: SuggestionImpactAttention[];
	live_cost: number;
	draft_cost: number;
	savings: number;
	priced: number;
	unpriced: number;
}

export interface SuggestionRunEstimate {
	requests: number;
	clustered: number;
	assigned: number;
	skipped: number;
	max_model_calls: number;
	// How many of the window's requests a run would first embed from their
	// logs, because they came before the router captured them; counted in
	// requests.
	backfill?: number;
	// True when what a run would embed from logs could not be worked out;
	// requests then counts only what is captured.
	backfill_unknown?: boolean;
}

/** One model for one tier of a model router. Omitting provider or model keeps the request's value. */
export interface ModelRouterTarget {
	tier: DecisionTier;
	provider?: string;
	model?: string;
	key_id?: string;
	weight: number;
	/** Replaces the request's fallback chain when this target is chosen. */
	fallbacks?: RoutingFallbackWire[];
}

/**
 * Which requests a model router takes. Routers are tried after the routing
 * rules, on the request as they left it: narrowest scope first, then lowest
 * priority, and the first enabled router whose condition holds takes the
 * request. Saved on its own endpoint, so a classifier save never changes it.
 */
export interface ModelRouterRouting {
	enabled: boolean;
	scope: RoutingRule["scope"];
	scope_id?: string | null;
	/** Empty takes every request in the scope. Cannot use complexity_tier. */
	cel_expression?: string;
	/** The condition builder's state, kept so an edit reopens in the builder. */
	query?: RuleGroupType;
	priority: number;
}