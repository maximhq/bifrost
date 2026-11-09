import type { CatalogEntry, Criterion, EvalKey, EvaluationResult, EvaluatorSummary } from "@/lib/types/evals";
import { describe, expect, it } from "vitest";
import {
	catalogModelNeed,
	criterionParts,
	defaultConfig,
	defaultEntryCriterion,
	evalKeyFor,
	executionOutcome,
	filterCatalog,
	gradingStyleOf,
	groupByCategory,
	missingEvalKeyProviders,
	scoreText,
	withGradingStyle,
	pageOffset,
} from "./evaluatorUtils";

const key = (provider: string, name = "k", concurrency = 4): EvalKey => ({ key_id: `${provider}-${name}`, name, provider, concurrency });

const summary = (over: Partial<EvaluatorSummary>): EvaluatorSummary => ({
	id: "e1",
	name: "e",
	description: "",
	type: "ai",
	current_version: 1,
	created_at: "",
	updated_at: "",
	variables: [],
	lower_is_better: false,
	...over,
});

const entry = (over: Partial<CatalogEntry>): CatalogEntry => ({
	key: "k",
	name: "Faithfulness",
	description: "claims the context supports",
	category: "Retrieval",
	type: "ai",
	variables: ["input", "output"],
	lower_is_better: false,
	needs_judge_model: true,
	needs_embedding_model: false,
	pass_criteria: {
		entry_level: { name: "score", operator: ">=", value: 0.8 },
		run_level: { name: "queries_passed", operator: ">=", value: 80 },
	},
	scale: "0 to 1",
	...over,
});

describe("criterionParts", () => {
	it("renders a comparison with its symbol", () => {
		expect(criterionParts({ name: "score", operator: ">=", value: 0.8 })).toEqual({ symbol: "≥", value: "0.8", isOneOf: false });
		expect(criterionParts({ name: "score", operator: "=", value: "Yes" })).toEqual({ symbol: "=", value: "Yes", isOneOf: false });
		expect(criterionParts({ name: "score", operator: "!=", value: true })).toEqual({ symbol: "≠", value: "Yes", isOneOf: false });
	});

	it("lists the labels of an is-one-of rule", () => {
		const criterion: Criterion = { name: "score", operator: "is one of", value: ["good", "great"] };
		expect(criterionParts(criterion)).toEqual({ symbol: "", value: "good, great", isOneOf: true });
	});
});

describe("eval keys", () => {
	it("finds the key a provider's evaluations run on", () => {
		const keys = [key("openai", "evals-key", 8), key("anthropic")];
		expect(evalKeyFor(keys, "openai")?.name).toBe("evals-key");
		expect(evalKeyFor(keys, "bedrock")).toBeUndefined();
		expect(evalKeyFor(keys, "")).toBeUndefined();
	});

	it("counts the evaluators that cannot run, per provider", () => {
		const evaluators = [
			summary({ id: "a", provider: "anthropic" }),
			summary({ id: "b", provider: "anthropic" }),
			summary({ id: "c", provider: "openai" }),
			summary({ id: "d", type: "programmatic" }),
			summary({ id: "e", type: "human", provider: "bedrock" }),
		];
		expect(missingEvalKeyProviders(evaluators, [key("openai")])).toEqual([{ provider: "anthropic", count: 2 }]);
		expect(missingEvalKeyProviders(evaluators, [key("openai"), key("anthropic")])).toEqual([]);
	});
});

describe("config defaults", () => {
	it("starts every type with a config the gateway accepts once filled in", () => {
		expect(defaultConfig("ai")).toMatchObject({ grading_style: "yes_no", instructions: "", provider: "", model: "" });
		expect(defaultConfig("programmatic")).toMatchObject({ response_type: "yes_no" });
		expect((defaultConfig("programmatic") as { code: string }).code).toContain("def validate(");
		expect(defaultConfig("api")).toMatchObject({ method: "POST", output_mapping: { score: "score" } });
		expect(defaultConfig("human")).toMatchObject({ evaluation_type: "yes_no" });
	});

	it("picks an entry rule that fits the score type", () => {
		expect(defaultEntryCriterion("yes_no")).toEqual({ name: "score", operator: "=", value: "Yes" });
		expect(defaultEntryCriterion("number")).toEqual({ name: "score", operator: ">=", value: 0.8 });
		expect(defaultEntryCriterion("scale", { min: 1, max: 5 })).toEqual({ name: "score", operator: ">=", value: 4 });
		expect(defaultEntryCriterion("multiselect")).toEqual({ name: "score", operator: "is one of", value: [], options: [] });
	});

	it("reads the score type from whichever field the type uses", () => {
		expect(gradingStyleOf("ai", { grading_style: "scale" })).toBe("scale");
		expect(gradingStyleOf("programmatic", { response_type: "number" })).toBe("number");
		expect(gradingStyleOf("human", { evaluation_type: "multiselect" })).toBe("multiselect");
		expect(gradingStyleOf("api", {})).toBe("number");
		expect(gradingStyleOf("statistical", {})).toBe("number");
	});

	it("resets the entry rule and scale when the score type changes", () => {
		const next = withGradingStyle("ai", defaultConfig("ai"), "scale") as Record<string, unknown>;
		expect(next.grading_style).toBe("scale");
		expect(next.scale).toEqual({ min: 1, max: 5 });
		expect((next.pass_criteria as { entry_level: Criterion }).entry_level).toEqual({ name: "score", operator: ">=", value: 4 });

		const back = withGradingStyle("ai", next, "yes_no") as Record<string, unknown>;
		expect(back.scale).toBeUndefined();
		expect((back.pass_criteria as { entry_level: Criterion }).entry_level.value).toBe("Yes");
		expect((back.pass_criteria as { run_level: Criterion }).run_level.name).toBe("queries_passed");
	});
});

describe("execution result", () => {
	const result = (over: Partial<EvaluationResult>): EvaluationResult => ({ score: { status: "ok", number: 1 }, latency_ms: 1, ...over });

	it("names the outcome", () => {
		expect(executionOutcome(result({ pass: true }))).toBe("passed");
		expect(executionOutcome(result({ pass: false }))).toBe("failed");
		expect(executionOutcome(result({}))).toBe("scored");
		expect(executionOutcome(result({ score: { status: "na" } }))).toBe("na");
		expect(executionOutcome(result({ score: { status: "error" }, pass: false }))).toBe("error");
	});

	it("prints a score of any kind", () => {
		expect(scoreText({ status: "ok", bool: true })).toBe("Yes");
		expect(scoreText({ status: "ok", bool: false })).toBe("No");
		expect(scoreText({ status: "ok", number: 0.75 })).toBe("0.75");
		expect(scoreText({ status: "ok", number: 0.123456 })).toBe("0.1235");
		expect(scoreText({ status: "ok", string: "great" })).toBe("great");
		expect(scoreText({ status: "na" })).toBe("");
	});
});

describe("catalog", () => {
	const entries = [
		entry({ key: "faithfulness" }),
		entry({ key: "toxicity", name: "Toxicity", category: "Safety", description: "toxic statements", lower_is_better: true }),
		entry({ key: "bleu", name: "BLEU", category: "Text similarity", type: "statistical", needs_judge_model: false }),
		entry({
			key: "cosine",
			name: "Cosine",
			category: "Embedding distance",
			type: "statistical",
			needs_judge_model: false,
			needs_embedding_model: true,
		}),
	];
	const none = { search: "", categories: [], types: [], models: [] };

	it("says what model an entry needs", () => {
		expect(entries.map(catalogModelNeed)).toEqual(["judge", "judge", "none", "embedding"]);
	});

	it("filters by every facet, each a union", () => {
		expect(filterCatalog(entries, none)).toHaveLength(4);
		expect(filterCatalog(entries, { ...none, search: "TOXIC" }).map((e) => e.key)).toEqual(["toxicity"]);
		expect(filterCatalog(entries, { ...none, categories: ["Safety", "Retrieval"] }).map((e) => e.key)).toEqual([
			"faithfulness",
			"toxicity",
		]);
		expect(filterCatalog(entries, { ...none, types: ["statistical"] }).map((e) => e.key)).toEqual(["bleu", "cosine"]);
		expect(filterCatalog(entries, { ...none, models: ["none", "embedding"] }).map((e) => e.key)).toEqual(["bleu", "cosine"]);
		expect(filterCatalog(entries, { ...none, types: ["ai"], models: ["none"] })).toEqual([]);
	});

	it("groups in the order the catalog arrives", () => {
		expect(groupByCategory(entries).map(([category, group]) => [category, group.length])).toEqual([
			["Retrieval", 1],
			["Safety", 1],
			["Text similarity", 1],
			["Embedding distance", 1],
		]);
	});
});
describe("pageOffset", () => {
	it("reads a negative or broken offset from the URL as the first page", () => {
		expect(pageOffset(-1)).toBe(0);
		expect(pageOffset(-250)).toBe(0);
		expect(pageOffset(Number.NaN)).toBe(0);
	});

	it("keeps a valid offset, as a whole number", () => {
		expect(pageOffset(0)).toBe(0);
		expect(pageOffset(50)).toBe(50);
		expect(pageOffset(25.9)).toBe(25);
	});
});