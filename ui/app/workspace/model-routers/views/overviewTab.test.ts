import type { ModelRouter, ModelRouterTarget } from "@/lib/types/complexityRouter";
import { describe, expect, it } from "vitest";
import { DEFAULT_DECISION_CONFIG } from "@/lib/types/complexityRouter";
import { classifierSummary, modelCount, overviewStatus, tiersWithModels, trafficPeriodLabel } from "./overviewTab";

const router = (targets: ModelRouterTarget[]) => ({ targets }) as unknown as ModelRouter;

describe("tiersWithModels", () => {
	it("counts each tier that has a target with a model once", () => {
		expect(
			tiersWithModels(
				router([
					{ tier: "SIMPLE", model: "gpt-4o-mini", weight: 0.5 },
					{ tier: "SIMPLE", provider: "openai", model: "gpt-4o", weight: 0.5 },
					{ tier: "COMPLEX", model: "claude-opus", weight: 1 },
				]),
			),
		).toBe(2);
	});

	it("does not count a tier whose targets only change the provider", () => {
		expect(tiersWithModels(router([{ tier: "MEDIUM", provider: "anthropic", weight: 1 }]))).toBe(0);
	});

	it("counts nothing for a router without targets", () => {
		expect(tiersWithModels(router([]))).toBe(0);
	});

	it("counts nothing for a router the gateway sent no targets for", () => {
		expect(tiersWithModels({} as unknown as ModelRouter)).toBe(0);
	});

	it("does not count a tier whose model is only spaces", () => {
		expect(tiersWithModels(router([{ tier: "SIMPLE", provider: "openai", model: "  ", weight: 1 }]))).toBe(0);
	});

	it("counts a tier once any of its targets sets a model, and all three when each does", () => {
		expect(
			tiersWithModels(
				router([
					{ tier: "MEDIUM", provider: "anthropic", weight: 0.5 },
					{ tier: "MEDIUM", model: "gpt-4o", weight: 0.5 },
				]),
			),
		).toBe(1);
		expect(
			tiersWithModels(
				router([
					{ tier: "COMPLEX", model: "claude-opus", weight: 1 },
					{ tier: "SIMPLE", model: "gpt-4o-mini", weight: 1 },
					{ tier: "MEDIUM", model: "gpt-4o", weight: 1 },
				]),
			),
		).toBe(3);
	});
});

describe("modelCount", () => {
	it("counts every model, so two on one tier are two", () => {
		expect(
			modelCount(
				router([
					{ tier: "SIMPLE", model: "gpt-4o-mini", weight: 0.5 },
					{ tier: "SIMPLE", provider: "openai", model: "gpt-4o", weight: 0.5 },
					{ tier: "MEDIUM", model: "gpt-4o", weight: 1 },
					{ tier: "COMPLEX", model: "claude-opus", weight: 1 },
				]),
			),
		).toBe(4);
	});

	it("does not count a target that names no model", () => {
		expect(
			modelCount(
				router([
					{ tier: "MEDIUM", provider: "anthropic", weight: 0.5 },
					{ tier: "MEDIUM", provider: "openai", model: "  ", weight: 0.25 },
					{ tier: "MEDIUM", model: "gpt-4o", weight: 0.25 },
				]),
			),
		).toBe(1);
	});

	it("counts nothing for a router without targets", () => {
		expect(modelCount(router([]))).toBe(0);
		expect(modelCount({} as unknown as ModelRouter)).toBe(0);
	});
});

describe("trafficPeriodLabel", () => {
	const t = (key: string) => `t(${key})`;

	it("names a preset as the picker names it", () => {
		expect(trafficPeriodLabel({ period: "7d" }, t, "UTC")).toBe("t(timePeriods.7d)");
		expect(trafficPeriodLabel({ period: "this_month" }, t, "UTC")).toBe("t(timePeriods.this_month)");
	});

	it("names a custom range by its dates, in the picker's timezone", () => {
		// 23:30 on March 3 to 00:30 on March 4 in Los Angeles, which is all March 4 in UTC.
		const range = { start: Date.UTC(2026, 2, 4, 7, 30) / 1000, end: Date.UTC(2026, 2, 4, 8, 30) / 1000 };
		expect(trafficPeriodLabel(range, t, "America/Los_Angeles")).toContain("Mar 3");
		expect(trafficPeriodLabel(range, t, "UTC")).not.toContain("Mar 3");
	});
});

describe("overviewStatus", () => {
	it("says the router is off whatever its traffic", () => {
		expect(overviewStatus(false, "ready", 12)).toBe("off");
		expect(overviewStatus(false, "pending", 0)).toBe("off");
	});

	it("says only that the router is on until its traffic has been read", () => {
		expect(overviewStatus(true, "pending", 0)).toBe("on");
		expect(overviewStatus(true, "unavailable", 0)).toBe("on");
	});

	it("says whether it took requests once its traffic has been read", () => {
		expect(overviewStatus(true, "ready", 0)).toBe("noTraffic");
		expect(overviewStatus(true, "ready", 12)).toBe("serving");
	});
});

describe("classifierSummary", () => {
	const t = (key: string) => key;
	const decisionRouter = (decision: object | undefined) => ({ classifier: "decision", decision }) as unknown as ModelRouter;

	it("shows the gateway's default decision model when the router saves none", () => {
		expect(classifierSummary(decisionRouter(undefined), t).model).toBe(DEFAULT_DECISION_CONFIG.model);
		expect(classifierSummary(decisionRouter({ timeout: "1s" }), t).model).toBe(DEFAULT_DECISION_CONFIG.model);
	});

	it("shows the decision model the router saves", () => {
		expect(classifierSummary(decisionRouter({ provider: "openrouter", model: "jev-1.14.0" }), t).model).toBe("jev-1.14.0");
	});

	it("shows the gateway's default decision model for a block saved with only a model or only a provider", () => {
		expect(classifierSummary(decisionRouter({ model: "jev-1.14.0" }), t).model).toBe(DEFAULT_DECISION_CONFIG.model);
		expect(classifierSummary(decisionRouter({ provider: "openrouter" }), t).model).toBe(DEFAULT_DECISION_CONFIG.model);
	});

	it("names each classifier, with the model behind it", () => {
		const semantic = { classifier: "semantic", semantic: { provider: "openai", embedding_model: "text-embedding-3-small" } };
		const llm = { classifier: "llm", llm: { provider: "openai", model: "gpt-4o-mini" } };
		expect(classifierSummary(semantic as unknown as ModelRouter, t)).toEqual({
			name: "classifierNameSemantic",
			model: "text-embedding-3-small",
		});
		expect(classifierSummary(llm as unknown as ModelRouter, t)).toEqual({ name: "classifierNameLlm", model: "gpt-4o-mini" });
		expect(classifierSummary(decisionRouter(undefined), t).name).toBe("classifierNameDecision");
	});

	it("has no model to show for a semantic or llm classifier that is not set up yet", () => {
		// The routers list shows "Not configured" in its place.
		expect(classifierSummary({ classifier: "semantic" } as unknown as ModelRouter, t).model).toBeUndefined();
		expect(classifierSummary({ classifier: "llm" } as unknown as ModelRouter, t).model).toBeUndefined();
	});

	it("does not show the llm fallback's model as a semantic router's classifier", () => {
		const router = {
			classifier: "semantic",
			semantic: { provider: "openai", embedding_model: "text-embedding-3-small", fallback: "llm" },
			llm: { provider: "openai", model: "gpt-4o-mini" },
		};
		expect(classifierSummary(router as unknown as ModelRouter, t)).toEqual({
			name: "classifierNameSemantic",
			model: "text-embedding-3-small",
		});
	});

	it("reads a router saved before classifiers had a name as semantic", () => {
		const legacy = { semantic: { provider: "openai", embedding_model: "text-embedding-3-small" } };
		expect(classifierSummary(legacy as unknown as ModelRouter, t).name).toBe("classifierNameSemantic");
	});
});