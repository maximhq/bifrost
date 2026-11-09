import type {
	CatalogEntry,
	Criterion,
	CriterionOperator,
	EvalKey,
	EvaluationResult,
	EvaluationScore,
	EvaluatorConfig,
	EvaluatorSummary,
	EvaluatorType,
	GradingStyle,
	PassCriteria,
} from "@/lib/types/evals";

/** Evaluator types in the order every list and filter shows them. */
export const EVALUATOR_TYPES: EvaluatorType[] = ["ai", "programmatic", "statistical", "api", "human"];

/** Types a user can write from scratch. A statistical evaluator only comes from the catalog. */
export const CREATABLE_EVALUATOR_TYPES: EvaluatorType[] = ["ai", "programmatic", "api", "human"];

export const GRADING_STYLES: GradingStyle[] = ["yes_no", "scale", "multiselect", "number"];

export const NUMERIC_OPERATORS: CriterionOperator[] = [">=", ">", "<=", "<", "=", "!="];

export const RUN_CRITERION_NAMES = ["queries_passed", "mean_score", "median_score", "minimum_score", "maximum_score"] as const;

export const API_METHODS = ["GET", "POST", "PUT", "PATCH", "DELETE"];

export const SCORE_YES = "Yes";
export const SCORE_NO = "No";

export const DEFAULT_SCALE = { min: 1, max: 5 };

const OPERATOR_SYMBOLS: Record<CriterionOperator, string> = {
	"=": "=",
	"!=": "≠",
	">": ">",
	"<": "<",
	">=": "≥",
	"<=": "≤",
	"is one of": "",
};

export function operatorSymbol(operator: CriterionOperator): string {
	return OPERATOR_SYMBOLS[operator] ?? operator;
}

function criterionValueText(value: Criterion["value"]): string {
	if (Array.isArray(value)) return value.join(", ");
	if (typeof value === "boolean") return value ? SCORE_YES : SCORE_NO;
	return String(value ?? "");
}

/**
 * The pieces of a pass rule, for a caller to lay out. An "is one of" rule has no symbol:
 * the caller words it.
 */
export function criterionParts(criterion: Criterion): { symbol: string; value: string; isOneOf: boolean } {
	return {
		symbol: operatorSymbol(criterion.operator),
		value: criterionValueText(criterion.value),
		isOneOf: criterion.operator === "is one of",
	};
}

/** The key a provider's evaluations run on, or undefined when the provider has none. */
export function evalKeyFor(keys: EvalKey[], provider: string | undefined): EvalKey | undefined {
	if (!provider) return undefined;
	return keys.find((key) => key.provider === provider);
}

/** Whether an evaluator of this type calls a model when it runs. */
function callsModel(evaluator: EvaluatorSummary): boolean {
	return (evaluator.type === "ai" || evaluator.type === "statistical") && Boolean(evaluator.provider);
}

/** Whether an evaluator needs a model and its provider has no key marked for evaluations. */
export function lacksEvalKey(evaluator: EvaluatorSummary, keys: EvalKey[]): boolean {
	return callsModel(evaluator) && !evalKeyFor(keys, evaluator.provider);
}

/** Providers that evaluators need and that have no eval key, with how many evaluators each blocks. */
export function missingEvalKeyProviders(evaluators: EvaluatorSummary[], keys: EvalKey[]): { provider: string; count: number }[] {
	const counts = new Map<string, number>();
	for (const evaluator of evaluators) {
		if (!lacksEvalKey(evaluator, keys)) continue;
		const provider = evaluator.provider as string;
		counts.set(provider, (counts.get(provider) ?? 0) + 1);
	}
	return [...counts.entries()].map(([provider, count]) => ({ provider, count })).sort((a, b) => a.provider.localeCompare(b.provider));
}

/** The entry rule a score type starts with. A scale passes in its top fifth. */
export function defaultEntryCriterion(style: GradingStyle, scale?: { min: number; max: number }): Criterion {
	switch (style) {
		case "yes_no":
			return { name: "score", operator: "=", value: SCORE_YES };
		case "multiselect":
			return { name: "score", operator: "is one of", value: [], options: [] };
		case "scale": {
			const range = scale ?? DEFAULT_SCALE;
			return { name: "score", operator: ">=", value: Number((range.max * 0.8).toFixed(4)) };
		}
		default:
			return { name: "score", operator: ">=", value: 0.8 };
	}
}

export function defaultRunCriterion(): Criterion {
	return { name: "queries_passed", operator: ">=", value: 80 };
}

function defaultPassCriteria(style: GradingStyle): PassCriteria {
	return { entry_level: defaultEntryCriterion(style), run_level: defaultRunCriterion() };
}

const STARTER_CODE = `def validate(output):
    # Return True when the output passes.
    return len(output) > 0
`;

/** A blank config for a new evaluator of the given type. */
export function defaultConfig(type: EvaluatorType): EvaluatorConfig {
	switch (type) {
		case "ai":
			return { instructions: "", grading_style: "yes_no", provider: "", model: "", pass_criteria: defaultPassCriteria("yes_no") };
		case "programmatic":
			return { code: STARTER_CODE, response_type: "yes_no", pass_criteria: defaultPassCriteria("yes_no") };
		case "api":
			return {
				method: "POST",
				url: "",
				headers: [],
				payload: '{\n  "output": "{{output}}"\n}',
				output_mapping: { score: "score", reasoning: "" },
				pass_criteria: defaultPassCriteria("number"),
			};
		case "human":
			return { instructions: "", evaluation_type: "yes_no", pass_criteria: defaultPassCriteria("yes_no") };
		default:
			return { pass_criteria: defaultPassCriteria("number") };
	}
}

/** The config field that holds the score type, for the types that let a user choose it. */
function gradingStyleField(type: EvaluatorType): "grading_style" | "response_type" | "evaluation_type" | undefined {
	if (type === "ai") return "grading_style";
	if (type === "programmatic") return "response_type";
	if (type === "human") return "evaluation_type";
	return undefined;
}

/** The score type of a config. API and statistical evaluators always score a number. */
export function gradingStyleOf(type: EvaluatorType, config: object): GradingStyle {
	const field = gradingStyleField(type);
	const style = field ? (config as Record<string, unknown>)[field] : undefined;
	return GRADING_STYLES.includes(style as GradingStyle) ? (style as GradingStyle) : "number";
}

/**
 * Returns the config with a new score type. The entry rule is reset, because a rule
 * written for one score type does not fit another. The run rule is kept.
 */
export function withGradingStyle(type: EvaluatorType, config: object, style: GradingStyle): EvaluatorConfig {
	const field = gradingStyleField(type);
	if (!field) return config as EvaluatorConfig;
	const current = config as Record<string, unknown>;
	const next: Record<string, unknown> = { ...current, [field]: style };
	if (style === "scale" && type !== "programmatic") {
		next.scale = (current.scale as { min: number; max: number } | undefined) ?? DEFAULT_SCALE;
	} else {
		delete next.scale;
	}
	const criteria = current.pass_criteria as PassCriteria | undefined;
	next.pass_criteria = {
		entry_level: defaultEntryCriterion(style, next.scale as { min: number; max: number } | undefined),
		run_level: criteria?.run_level ?? defaultRunCriterion(),
	};
	return next as unknown as EvaluatorConfig;
}

export type ExecutionOutcome = "passed" | "failed" | "scored" | "na" | "error";

/** What a finished run amounts to. "scored" is a score with no pass rule applied. */
export function executionOutcome(result: EvaluationResult): ExecutionOutcome {
	if (result.score.status === "error") return "error";
	if (result.score.status === "na") return "na";
	if (result.pass === undefined || result.pass === null) return "scored";
	return result.pass ? "passed" : "failed";
}

/** A score as text. Empty when the evaluator produced none. */
export function scoreText(score: EvaluationScore): string {
	if (score.status !== "ok") return "";
	if (score.bool !== undefined && score.bool !== null) return score.bool ? SCORE_YES : SCORE_NO;
	if (score.number !== undefined && score.number !== null) return String(Number(score.number.toFixed(4)));
	return score.string ?? "";
}

export type CatalogModelNeed = "judge" | "embedding" | "none";

export const CATALOG_MODEL_NEEDS: CatalogModelNeed[] = ["judge", "embedding", "none"];

export function catalogModelNeed(entry: CatalogEntry): CatalogModelNeed {
	if (entry.needs_judge_model) return "judge";
	if (entry.needs_embedding_model) return "embedding";
	return "none";
}

export interface CatalogFilters {
	search: string;
	categories: string[];
	types: string[];
	models: string[];
}

/** Filters the catalog in the browser. Each facet is a union; the facets intersect. */
export function filterCatalog(entries: CatalogEntry[], filters: CatalogFilters): CatalogEntry[] {
	const term = filters.search.trim().toLowerCase();
	return entries.filter((entry) => {
		if (term && !entry.name.toLowerCase().includes(term) && !entry.description.toLowerCase().includes(term)) return false;
		if (filters.categories.length > 0 && !filters.categories.includes(entry.category)) return false;
		if (filters.types.length > 0 && !filters.types.includes(entry.type)) return false;
		if (filters.models.length > 0 && !filters.models.includes(catalogModelNeed(entry))) return false;
		return true;
	});
}

/** Groups entries by category, keeping the order in which categories first appear. */
export function groupByCategory(entries: CatalogEntry[]): [string, CatalogEntry[]][] {
	const groups = new Map<string, CatalogEntry[]>();
	for (const entry of entries) {
		const group = groups.get(entry.category);
		if (group) group.push(entry);
		else groups.set(entry.category, [entry]);
	}
	return [...groups.entries()];
}

// pageOffset reads the offset a list's URL carries. The URL is typed by hand as well
// as written by the pager: a negative or broken value is the first page, not a request
// the API refuses.
export function pageOffset(value: number): number {
	return Number.isFinite(value) && value > 0 ? Math.floor(value) : 0;
}