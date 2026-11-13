import type { ModelRouter, ModelRouterTarget } from "@/lib/types/complexityRouter";
import { describe, expect, it } from "vitest";
import { DEFAULT_DECISION_CONFIG } from "@/lib/types/complexityRouter";
import { classifierSummary, overviewStatus, tiersWithModels } from "./overviewTab";

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
});