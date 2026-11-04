import { afterEach, describe, expect, it } from "vitest";
import { registerEntityGovernanceProbe, resetEntityGovernanceProbeForTests, useEntityGovernanceProbe } from "./entityGovernance";

describe("entity governance registry", () => {
	afterEach(() => resetEntityGovernanceProbeForTests());

	it("answers that nothing governs an entity when no probe is registered", () => {
		expect(useEntityGovernanceProbe("customer", "cust-1", true)).toEqual({ isLoading: false });
	});

	it("hands the question to the registered probe", () => {
		const calls: Array<[string, string, boolean]> = [];
		registerEntityGovernanceProbe((kind, id, enabled) => {
			calls.push([kind, id, enabled]);
			return { governedBy: "Platform Access", isLoading: false };
		});
		expect(useEntityGovernanceProbe("team", "team-1", false)).toEqual({ governedBy: "Platform Access", isLoading: false });
		expect(calls).toEqual([["team", "team-1", false]]);
	});

	it("goes back to the base answer once reset", () => {
		registerEntityGovernanceProbe(() => ({ governedBy: "x", isLoading: true }));
		resetEntityGovernanceProbeForTests();
		expect(useEntityGovernanceProbe("business_unit", "bu-1", true)).toEqual({ isLoading: false });
	});
});