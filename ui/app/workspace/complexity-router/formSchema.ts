import {
	AnalyzerConfig,
	COMPLEXITY_TIER_VALUES,
	DEFAULT_DECISION_CONFIG,
	DEFAULT_DECISION_MODEL,
	DecisionGuidanceDefaults,
	DecisionTier,
	OPENROUTER_DECISION_MODELS,
	SELF_HOSTED_DECISION_MODELS,
	MAX_DECISION_CRITERIA_ITEM_CHARACTERS,
	MAX_DECISION_CRITERIA_ITEMS,
	MAX_DECISION_DEFINITION_CHARACTERS,
	DEFAULT_LLM_CONFIG,
	DEFAULT_SEMANTIC_CONFIG,
	MAX_DECISION_PREVIOUS_MESSAGE_COUNT,
	KeywordListKey,
	MAX_LLM_PROMPT_CHARACTERS,
	MAX_LLM_MESSAGE_HISTORY,
	MAX_SEMANTIC_MESSAGE_HISTORY,
	MAX_SEMANTIC_PHRASE_CHARACTERS,
	MAX_SEMANTIC_PHRASES,
	MAX_SEMANTIC_TIMEOUT_MS,
	MIN_LLM_MESSAGE_HISTORY,
	MIN_SEMANTIC_MESSAGE_HISTORY,
	parseLLMTimeoutMs,
	parseSemanticTimeoutMs,
} from "@/lib/types/complexityRouter";
import { ModelProvider } from "@/lib/types/config";
import { DBKey } from "@/lib/types/governance";
import { z } from "zod";

// Form-owned duration values are always a single unit (the controls append
// "ms"), so a plain positive-duration check is enough.
const positiveDurationPattern = /^[0-9]*\.?[0-9]+(ns|us|µs|ms|s|m|h)$/;

export function countCanonicalSemanticPhrases(keywords: Record<KeywordListKey, string[]>) {
	const count = (phrases: string[]) => new Set(phrases.map((phrase) => phrase.trim().toLowerCase()).filter(Boolean)).size;
	const simple = count(keywords.simple_keywords);
	const medium = count(keywords.medium_keywords);
	const complex = count(keywords.complex_keywords);
	return { simple, medium, complex, total: simple + medium + complex };
}

export function isPositiveDurationString(value: string | undefined): boolean {
	if (!value) return false;
	const trimmed = value.trim();
	if (!positiveDurationPattern.test(trimmed) || Number.parseFloat(trimmed) <= 0) return false;
	// Past the int64-nanosecond ceiling the server cannot parse the duration at
	// all, so it is rejected here as a value rather than sent to fail as a 400.
	return parseSemanticTimeoutMs(trimmed) <= MAX_SEMANTIC_TIMEOUT_MS;
}

const semanticSchema = z.object({
	provider: z.string(),
	embedding_model: z.string(),
	// The control edits milliseconds but the value stays a Go duration, so a
	// non-positive or malformed entry is caught here rather than snapped back to
	// the default while the operator is still typing.
	timeout: z
		.string()
		.min(1, "Enter an embedding timeout")
		.refine((value) => isPositiveDurationString(value), `Enter a timeout greater than 0 and at most ${MAX_SEMANTIC_TIMEOUT_MS}ms`)
		.optional(),
	min_similarity: z.number({ error: "Enter a number between 0 and 1" }).min(0, "Must be 0 or greater").lt(1, "Must be less than 1"),
	message_history_count: z
		.number({
			error: `Enter a number between ${MIN_SEMANTIC_MESSAGE_HISTORY} and ${MAX_SEMANTIC_MESSAGE_HISTORY}`,
		})
		.int("Must be a whole number")
		.min(MIN_SEMANTIC_MESSAGE_HISTORY, `Must be at least ${MIN_SEMANTIC_MESSAGE_HISTORY}`)
		.max(MAX_SEMANTIC_MESSAGE_HISTORY, `Must be at most ${MAX_SEMANTIC_MESSAGE_HISTORY}`),
	count_toward_budgets: z.boolean().optional(),
	vector_store: z.enum(["embedded", "vector_store"]).optional(),
	fallback: z.enum(["none", "llm", "decision"]),
});

// The editors always hold what the gateway will send, so an emptied field is
// rejected rather than saved: it would reload as the shipped default, silently
// undoing the deletion. Reset to default is the way back.
const decisionCriteriaListSchema = (label: string) =>
	z
		.array(
			z
				.string()
				.max(MAX_DECISION_CRITERIA_ITEM_CHARACTERS, `Each ${label} must be at most ${MAX_DECISION_CRITERIA_ITEM_CHARACTERS} characters`),
		)
		.min(1, `Add at least one ${label}`)
		.max(MAX_DECISION_CRITERIA_ITEMS, `At most ${MAX_DECISION_CRITERIA_ITEMS} ${label}s`);

const decisionTierCriteriaSchema = z.object({
	definition: z
		.string()
		.trim()
		.min(1, "Enter a definition")
		.max(MAX_DECISION_DEFINITION_CHARACTERS, `Must be at most ${MAX_DECISION_DEFINITION_CHARACTERS} characters`),
	signals: decisionCriteriaListSchema("signal"),
	examples: decisionCriteriaListSchema("example"),
});

// The unvalidated shape the form always holds; decisionGuidanceSchema checks it
// only once the editors are seeded.
const decisionTierFormShape = z.object({
	definition: z.string(),
	signals: z.array(z.string()),
	examples: z.array(z.string()),
});

const decisionGuidanceSchema = z.object({
	criteria: z.object({
		SIMPLE: decisionTierCriteriaSchema,
		MEDIUM: decisionTierCriteriaSchema,
		COMPLEX: decisionTierCriteriaSchema,
	}),
});

const decisionSchema = z
	.object({
		provider: z.string().trim().min(1, "Select the provider serving the decision model"),
		model: z.string().trim().min(1, "Select the decision model"),
		previous_message_count: z
			.number()
			.int("Must be a whole number")
			.min(0, "Must be at least 0")
			.max(MAX_DECISION_PREVIOUS_MESSAGE_COUNT, `Must be at most ${MAX_DECISION_PREVIOUS_MESSAGE_COUNT}`),
		timeout: z
			.string()
			.min(1, "Enter a decision-model timeout")
			.refine((value) => isPositiveDurationString(value), "Enter a timeout greater than 0"),
	})
	// OpenRouter's decisions endpoint serves only its Jev models; any other model it lists is a chat model.
	.refine((decision) => decision.provider !== "openrouter" || (OPENROUTER_DECISION_MODELS as readonly string[]).includes(decision.model), {
		message: "Select one of OpenRouter's Jev models",
		path: ["model"],
	});

const llmSchema = z.object({
	provider: z.string(),
	model: z.string(),
	// Same millisecond-edited Go duration treatment as the semantic timeout.
	timeout: z
		.string()
		.min(1, "Enter a classification timeout")
		.refine((value) => isPositiveDurationString(value), "Enter a timeout greater than 0")
		.optional(),
	prompt: z.string().max(MAX_LLM_PROMPT_CHARACTERS, `Must be at most ${MAX_LLM_PROMPT_CHARACTERS} characters`),
	message_history_count: z
		.number({
			error: `Enter a number between ${MIN_LLM_MESSAGE_HISTORY} and ${MAX_LLM_MESSAGE_HISTORY}`,
		})
		.int("Must be a whole number")
		.min(MIN_LLM_MESSAGE_HISTORY, `Must be at least ${MIN_LLM_MESSAGE_HISTORY}`)
		.max(MAX_LLM_MESSAGE_HISTORY, `Must be at most ${MAX_LLM_MESSAGE_HISTORY}`),
	count_toward_budgets: z.boolean().optional(),
});

// usesDecision reports whether the Decision block is live: the primary classifier or the
// semantic fallback. Its controls are hidden and its values are not saved otherwise.
function usesDecision(values: { classifier: string; semantic: { fallback: string } }): boolean {
	return values.classifier === "decision" || values.semantic.fallback === "decision";
}

export const analyzerConfigSchema = z
	.object({
		classifier: z.enum(["semantic", "decision"]),
		keywords: z.object({
			simple_keywords: z.array(z.string()).min(1, "Simple phrases cannot be empty"),
			medium_keywords: z.array(z.string()).min(1, "Medium phrases cannot be empty"),
			complex_keywords: z.array(z.string()).min(1, "Complex phrases cannot be empty"),
		}),
		semantic: semanticSchema,
		// Only shape-checked here: the Decision controls are hidden unless Decision is the
		// classifier or the fallback, so decisionSchema runs in superRefine under that
		// condition rather than letting an invisible error block Save. An emptied
		// number input registers as NaN, which must pass the shape check too.
		decision: z.object({
			provider: z.string(),
			model: z.string(),
			previous_message_count: z.number().or(z.nan()),
			timeout: z.string(),
			criteria: z.object({
				SIMPLE: decisionTierFormShape,
				MEDIUM: decisionTierFormShape,
				COMPLEX: decisionTierFormShape,
			}),
		}),
		llm: llmSchema,
		session: z.object({ enabled: z.boolean() }),
	})
	.superRefine((data, ctx) => {
		// A blank provider and model means the classifier simply is not configured
		// yet, which is a legal state: phrase edits still save. Half-filled is not,
		// because it cannot be turned into a working classifier.
		const hasProvider = data.semantic.provider.trim() !== "";
		const hasModel = data.semantic.embedding_model.trim() !== "";
		if (hasProvider || hasModel) {
			if (!hasProvider) {
				ctx.addIssue({
					code: "custom",
					message: "Select an embedding provider",
					path: ["semantic", "provider"],
				});
			}
			if (!hasModel) {
				ctx.addIssue({
					code: "custom",
					message: "Select an embedding model",
					path: ["semantic", "embedding_model"],
				});
			}
		}
		if (data.session.enabled && data.classifier === "semantic" && (!hasProvider || !hasModel)) {
			ctx.addIssue({
				code: "custom",
				message: "Configure the semantic classifier before enabling session routing",
				path: ["session", "enabled"],
			});
		}

		if (usesDecision(data)) {
			const decision = decisionSchema.safeParse(data.decision);
			const issues = decision.success ? [] : [...decision.error.issues];
			// Guidance that was never seeded (the status endpoint has not supplied
			// defaults) is hidden and saves as "use the defaults", so it is only
			// validated once the editors hold something.
			if (!isDecisionGuidanceEmpty(data.decision)) {
				const guidance = decisionGuidanceSchema.safeParse(data.decision);
				if (!guidance.success) issues.push(...guidance.error.issues);
			}
			for (const issue of issues) {
				ctx.addIssue({ code: "custom", message: issue.message, path: ["decision", ...issue.path] });
			}
		}

		// The llm block follows the same half-filled rule, with one addition:
		// switching the semantic fallback to "llm" makes the block mandatory,
		// because the server rejects that fallback without one.
		const hasLLMProvider = data.llm.provider.trim() !== "";
		const hasLLMModel = data.llm.model.trim() !== "";
		if (hasLLMProvider || hasLLMModel || data.semantic.fallback === "llm") {
			if (!hasLLMProvider) {
				ctx.addIssue({
					code: "custom",
					message: "Select a fallback provider",
					path: ["llm", "provider"],
				});
			}
			if (!hasLLMModel) {
				ctx.addIssue({
					code: "custom",
					message: "Select a fallback model",
					path: ["llm", "model"],
				});
			}
		}

		// Mirrors validateComplexitySemanticPhrases so invalid input fails in the
		// form instead of as an opaque 400.
		const lists: Array<{ key: KeywordListKey; label: string }> = [
			{ key: "simple_keywords", label: "Simple" },
			{ key: "medium_keywords", label: "Medium" },
			{ key: "complex_keywords", label: "Complex" },
		];

		const seen = new Map<string, string>();
		for (const { key, label } of lists) {
			for (const phrase of data.keywords[key]) {
				if (phrase.length > MAX_SEMANTIC_PHRASE_CHARACTERS) {
					ctx.addIssue({
						code: "custom",
						message: `A ${label} phrase exceeds the ${MAX_SEMANTIC_PHRASE_CHARACTERS}-character limit.`,
						path: ["keywords", key],
					});
					break;
				}
				const normalized = phrase.trim().toLowerCase();
				const firstTier = seen.get(normalized);
				if (firstTier && firstTier !== label) {
					ctx.addIssue({
						code: "custom",
						message: `"${phrase}" is also in the ${firstTier} list. Each phrase must belong to exactly one tier.`,
						path: ["keywords", key],
					});
				} else if (!firstTier) {
					seen.set(normalized, label);
				}
			}
		}

		if (hasProvider && hasModel) {
			const counts = countCanonicalSemanticPhrases(data.keywords);
			if (counts.total > MAX_SEMANTIC_PHRASES) {
				ctx.addIssue({
					code: "custom",
					message: `Semantic routing has ${counts.total} phrases (Simple=${counts.simple}, Medium=${counts.medium}, Complex=${counts.complex}); the maximum is ${MAX_SEMANTIC_PHRASES} across all tiers.`,
					path: ["keywords"],
				});
			}
		}
	});

// The form is stricter than the wire type: the API omits semantic fields left at
// their zero value (Go `omitempty`), but every control here is controlled and
// needs a concrete value, so the schema's inferred type is the source of truth.
export type AnalyzerFormValues = z.infer<typeof analyzerConfigSchema>;
export type SemanticFormValues = AnalyzerFormValues["semantic"];
export type LLMFormValues = AnalyzerFormValues["llm"];

export const DEFAULT_LLM_FORM_VALUES: LLMFormValues = {
	provider: DEFAULT_LLM_CONFIG.provider,
	model: DEFAULT_LLM_CONFIG.model,
	timeout: DEFAULT_LLM_CONFIG.timeout,
	prompt: DEFAULT_LLM_CONFIG.prompt ?? "",
	message_history_count: DEFAULT_LLM_CONFIG.message_history_count ?? MIN_LLM_MESSAGE_HISTORY,
	count_toward_budgets: DEFAULT_LLM_CONFIG.count_toward_budgets ?? false,
};

export const DEFAULT_SEMANTIC_FORM_VALUES: SemanticFormValues = {
	...DEFAULT_SEMANTIC_CONFIG,
	min_similarity: DEFAULT_SEMANTIC_CONFIG.min_similarity ?? 0,
	message_history_count: DEFAULT_SEMANTIC_CONFIG.message_history_count ?? MIN_SEMANTIC_MESSAGE_HISTORY,
	vector_store: "embedded",
	fallback: DEFAULT_SEMANTIC_CONFIG.fallback ?? "none",
};

export const DEFAULT_FORM_VALUES: AnalyzerFormValues = {
	keywords: {
		simple_keywords: [],
		medium_keywords: [],
		complex_keywords: [],
	},
	classifier: "semantic",
	semantic: DEFAULT_SEMANTIC_FORM_VALUES,
	decision: { ...DEFAULT_DECISION_CONFIG, ...decisionGuidanceFormValues() },
	llm: DEFAULT_LLM_FORM_VALUES,
	session: { enabled: false },
};

export type DecisionGuidanceFormValues = Pick<AnalyzerFormValues["decision"], "criteria">;

// isDecisionTierEmpty reports a tier with no definition and no list entries.
function isDecisionTierEmpty({ definition, signals, examples }: DecisionGuidanceFormValues["criteria"][DecisionTier]): boolean {
	return definition.trim() === "" && signals.length === 0 && examples.length === 0;
}

// isDecisionGuidanceEmpty reports guidance the form never seeded: no tier content
// anywhere.
export function isDecisionGuidanceEmpty(decision: DecisionGuidanceFormValues): boolean {
	return COMPLEXITY_TIER_VALUES.every((tier) => isDecisionTierEmpty(decision.criteria[tier]));
}

// decisionGuidanceFormValues fills the Decision guidance editors: each saved override
// wins, and anything unset shows the shipped default so the editor always
// displays what the gateway will send. Without defaults (status not loaded)
// unset fields stay empty, which saves as "use the default".
export function decisionGuidanceFormValues(
	saved?: AnalyzerConfig["decision"],
	defaults?: DecisionGuidanceDefaults,
): DecisionGuidanceFormValues {
	const tier = (name: DecisionTier) => {
		const override = saved?.criteria?.[name];
		const shipped = defaults?.criteria[name];
		return {
			definition: override?.definition || shipped?.definition || "",
			signals: override?.signals?.length ? override.signals : (shipped?.signals ?? []),
			examples: override?.examples?.length ? override.examples : (shipped?.examples ?? []),
		};
	};
	return {
		criteria: {
			SIMPLE: tier("SIMPLE"),
			MEDIUM: tier("MEDIUM"),
			COMPLEX: tier("COMPLEX"),
		},
	};
}

// decisionCriteriaFromDefaults copies the shipped per-tier criteria into form
// values, so restoring them never shares arrays with the status response.
export function decisionCriteriaFromDefaults(defaults: DecisionGuidanceDefaults): DecisionGuidanceFormValues["criteria"] {
	const tier = (name: DecisionTier) => ({
		definition: defaults.criteria[name].definition,
		signals: [...defaults.criteria[name].signals],
		examples: [...defaults.criteria[name].examples],
	});
	return { SIMPLE: tier("SIMPLE"), MEDIUM: tier("MEDIUM"), COMPLEX: tier("COMPLEX") };
}

// isDecisionGuidanceDefault reports whether every tier's definition, signals, and
// examples equal the shipped criteria, in order.
export function isDecisionGuidanceDefault(criteria: DecisionGuidanceFormValues["criteria"], defaults: DecisionGuidanceDefaults): boolean {
	const sameList = (a: string[], b: string[]) => a.length === b.length && a.every((value, index) => value === b[index]);
	return COMPLEXITY_TIER_VALUES.every((tier) => {
		const shipped = defaults.criteria[tier];
		const current = criteria[tier];
		return (
			current.definition === shipped.definition &&
			sameList(current.signals, shipped.signals) &&
			sameList(current.examples, shipped.examples)
		);
	});
}

// Fills in the fields the API omitted so the semantic controls stay controlled.
export function toFormValues(config: AnalyzerConfig, decisionDefaults?: DecisionGuidanceDefaults): AnalyzerFormValues {
	const saved = config.semantic;
	const savedLLM = config.llm;
	const classifier = config.classifier?.trim().toLowerCase();
	return {
		classifier: classifier === "decision" ? "decision" : "semantic",
		keywords: config.keywords,
		decision: {
			// A block saved without a model means the gateway default.
			...(config.decision?.provider && config.decision?.model
				? { provider: config.decision.provider, model: config.decision.model }
				: { provider: DEFAULT_DECISION_CONFIG.provider, model: DEFAULT_DECISION_CONFIG.model }),
			previous_message_count: config.decision?.previous_message_count ?? DEFAULT_DECISION_CONFIG.previous_message_count,
			timeout: config.decision?.timeout ?? DEFAULT_DECISION_CONFIG.timeout,
			...decisionGuidanceFormValues(config.decision, decisionDefaults),
		},
		session: config.session ?? { enabled: false },
		llm: savedLLM
			? {
					...DEFAULT_LLM_FORM_VALUES,
					...savedLLM,
					timeout: savedLLM.timeout ?? DEFAULT_LLM_FORM_VALUES.timeout,
					prompt: savedLLM.prompt ?? "",
					message_history_count: savedLLM.message_history_count ?? MIN_LLM_MESSAGE_HISTORY,
					count_toward_budgets: savedLLM.count_toward_budgets ?? false,
				}
			: DEFAULT_LLM_FORM_VALUES,
		semantic: saved
			? {
					...DEFAULT_SEMANTIC_FORM_VALUES,
					...saved,
					min_similarity: saved.min_similarity ?? 0,
					message_history_count: saved.message_history_count ?? MIN_SEMANTIC_MESSAGE_HISTORY,
					vector_store: saved.vector_store ?? DEFAULT_SEMANTIC_FORM_VALUES.vector_store,
					fallback: saved.fallback ?? "none",
				}
			: DEFAULT_SEMANTIC_FORM_VALUES,
	};
}

// Builds the replacement payload without writing a disabled session block.
// Session is additive to the complexity API, and nil already means disabled;
// omitting it keeps ordinary semantic edits compatible with gateways that
// predate session-aware routing. Once enabled, the block is sent explicitly.
export function toAnalyzerPayload(values: AnalyzerFormValues, saved?: AnalyzerConfig): AnalyzerConfig {
	const semantic = values.semantic.provider && values.semantic.embedding_model ? values.semantic : (saved?.semantic ?? undefined);
	const llm = values.llm.provider && values.llm.model ? values.llm : (saved?.llm ?? undefined);

	return {
		classifier: values.classifier,
		keywords: values.keywords,
		// Unused Decision values are not validated, so keep the saved block instead of
		// sending hidden, possibly invalid edits.
		...(usesDecision(values) ? { decision: toDecisionPayload(values.decision) } : saved?.decision ? { decision: saved.decision } : {}),
		...(values.session.enabled ? { session: values.session } : {}),
		...(semantic ? { semantic } : {}),
		...(llm ? { llm } : {}),
	};
}

// toDecisionPayload sends the editors' contents. The gateway drops anything equal
// to its shipped default, so saving untouched guidance stores no override and
// later default updates still apply. Empty fields are omitted only for the
// window before the status endpoint has supplied defaults to seed them.
function toDecisionPayload(decision: AnalyzerFormValues["decision"]): NonNullable<AnalyzerConfig["decision"]> {
	const criteria: NonNullable<NonNullable<AnalyzerConfig["decision"]>["criteria"]> = {};
	for (const tier of COMPLEXITY_TIER_VALUES) {
		const { definition, signals, examples } = decision.criteria[tier];
		if (isDecisionTierEmpty(decision.criteria[tier])) continue;
		criteria[tier] = {
			...(definition.trim() ? { definition } : {}),
			...(signals.length ? { signals } : {}),
			...(examples.length ? { examples } : {}),
		};
	}
	return {
		provider: decision.provider.trim(),
		model: decision.model.trim(),
		previous_message_count: decision.previous_message_count,
		timeout: decision.timeout,
		...(Object.keys(criteria).length ? { criteria } : {}),
	};
}

// The timeout control edits milliseconds while the form value stays a Go
// duration. A value this control wrote round-trips digit for digit, including a
// "0" the operator is midway through typing, which the schema rejects rather
// than the field silently rewriting. Anything else — a saved "1s", a blank —
// falls back to the parsed reading.
export function semanticTimeoutFieldValue(timeout: string | undefined): string | number {
	if (timeout === "") return "";
	const millis = timeout?.trim().match(/^([0-9]*\.?[0-9]+)ms$/);
	return millis ? millis[1] : parseSemanticTimeoutMs(timeout);
}

// decisionTimeoutFieldValue shows the stored Go duration as editable milliseconds.
export function decisionTimeoutFieldValue(timeout: string | undefined): string | number {
	if (timeout === "") return "";
	const millis = timeout?.trim().match(/^([0-9]*\.?[0-9]+)ms$/);
	return millis ? millis[1] : parseSemanticTimeoutMs(timeout ?? DEFAULT_DECISION_CONFIG.timeout);
}

// Same round-trip as semanticTimeoutFieldValue, with the llm default backstop.
export function llmTimeoutFieldValue(timeout: string | undefined): string | number {
	if (timeout === "") return "";
	const millis = timeout?.trim().match(/^([0-9]*\.?[0-9]+)ms$/);
	return millis ? millis[1] : parseLLMTimeoutMs(timeout);
}
// shouldSeedLLMPrompt decides whether the shipped guidance may initialize the draft.
export function shouldSeedLLMPrompt(enabled: boolean, defaultPrompt: string, prompt: string, edited: boolean): boolean {
	return enabled && defaultPrompt !== "" && prompt === "" && !edited;
}

// isRouterConfigured decides whether the page opens on the classifier choice
// (nothing set up yet) or straight on the configured classifier's settings.
// A router counts as configured once it can classify anything: a saved semantic
// block, or Decision chosen as the primary classifier. Phrases alone do not count;
// every install has them, and without a classifier they route nothing.
export function isRouterConfigured(config: AnalyzerConfig | undefined): boolean {
	if (!config) return false;
	if (config.classifier?.trim().toLowerCase() === "decision") return true;
	return Boolean(config.semantic?.provider && config.semantic?.embedding_model);
}
// DecisionProviderState is what the UI can actually tell about the provider the
// decision model runs through. "configured" is not a guarantee that decision
// calls succeed — only a live request proves that — so the UI never calls it "ready".
export type DecisionProviderState = "missing" | "failing" | "no-enabled-key" | "configured";

// CLEF_URL_MODEL matches a decisions override that runs a Clef model on Cloudflare
// Workers AI, capturing the model the URL serves.
const CLEF_URL_MODEL = /\/ai\/run\/@cf\/cloudflare\/(clef(?:-flash)?)\/?$/;

// clefModelFromProvider reads the Clef model a provider serves from its decisions
// URL. Cloudflare binds the model to the URL and rejects any other in the body,
// so the URL is the one source of truth for it.
export function clefModelFromProvider(provider: ModelProvider | undefined): string | undefined {
	const url = provider?.custom_provider_config?.request_path_overrides?.decisions;
	return url?.match(CLEF_URL_MODEL)?.[1];
}

// isDecisionProvider reports a provider that answers /v1/decisions natively:
// Typesafe, OpenRouter, or a custom provider built on the Typesafe base (Laya,
// Nimble, Clef). Other providers would only emulate decisions through chat,
// which is what the LLM classifier is for.
export function isDecisionProvider(provider: ModelProvider): boolean {
	return (
		provider.name === "typesafe" || provider.name === "openrouter" || provider.custom_provider_config?.base_provider_type === "typesafe"
	);
}

type SelfHostedModelGroup = (typeof SELF_HOSTED_DECISION_MODELS)[number];

// namedSelfHostedGroup finds the model a self-hosted provider is named after
// ("Laya", "nimble-gpu"), when its name points at exactly one.
function namedSelfHostedGroup(providerName: string): SelfHostedModelGroup | undefined {
	const name = providerName.toLowerCase();
	const named = SELF_HOSTED_DECISION_MODELS.filter((group) => name.includes(group.label.toLowerCase()));
	return named.length === 1 ? named[0] : undefined;
}

// selfHostedModelGroups is the checkpoint list offered for a self-hosted provider
// that cannot list its models: the checkpoints of the model it is named after,
// otherwise every group.
export function selfHostedModelGroups(providerName: string): SelfHostedModelGroup[] {
	const named = namedSelfHostedGroup(providerName);
	return named ? [named] : [...SELF_HOSTED_DECISION_MODELS];
}

// defaultDecisionModel is the model a newly selected provider starts on: Jev's
// latest alias (named per provider), the Clef model a Cloudflare URL serves, or
// the first checkpoint of the model a self-hosted provider is named after (Laya
// starts on english). A provider whose name says nothing starts empty, since only
// the operator knows which model it runs.
export function defaultDecisionModel(provider: ModelProvider | undefined): string {
	if (provider?.name === "typesafe") return DEFAULT_DECISION_MODEL;
	if (provider?.name === "openrouter") return OPENROUTER_DECISION_MODELS[0];
	return clefModelFromProvider(provider) ?? namedSelfHostedGroup(provider?.name ?? "")?.models[0] ?? "";
}

export function getDecisionProviderState(
	providers: ModelProvider[] | undefined,
	keys: DBKey[] | undefined,
	providerName: string,
): DecisionProviderState {
	const provider = (providers ?? []).find((candidate) => candidate.name === providerName);
	if (!provider) return "missing";
	// The provider failed to initialise, or could not list models with its keys:
	// both mean its credentials or settings are wrong. A provider with model
	// listing turned off (Laya, Clef) never lists, so a stale listing failure on
	// it says nothing about its decisions endpoint.
	const listsModels = provider.custom_provider_config?.allowed_requests?.list_models !== false;
	if (provider.provider_status !== "active" || (listsModels && provider.status === "list_models_failed")) return "failing";
	// A keyless custom provider (a self-hosted Laya, for one) needs no key.
	if (provider.custom_provider_config?.is_key_less) return "configured";
	// A key omits `enabled` when unset, which the Go side reads as enabled.
	if (!(keys ?? []).some((key) => key.provider === providerName && key.enabled !== false)) return "no-enabled-key";
	return "configured";
}