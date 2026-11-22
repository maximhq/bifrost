import { describe, expect, it } from "vitest";
import { guardrailMatchAction, guardrailMatchActionHint } from "./guardrailMatchAction";

describe("guardrailMatchAction", () => {
	it("treats every intervention as a block", () => {
		expect(guardrailMatchAction({ action: "redact" }, true)).toBe("blocked");
	});

	it("labels detections by their action", () => {
		expect(guardrailMatchAction({ action: "redact", enforced: true }, false)).toBe("redacted");
		expect(guardrailMatchAction({ action: "transform" }, false)).toBe("transformed");
		expect(guardrailMatchAction({ action: "block", enforced: true }, false)).toBe("blocked");
		expect(guardrailMatchAction({ action: "detect" }, false)).toBe("detected");
	});

	it("labels detections without an action as detected, as older rows have none", () => {
		expect(guardrailMatchAction({ rule_name: "pii-audit" }, false)).toBe("detected");
	});

	it("labels unenforced matches as monitored whatever they would have done", () => {
		expect(guardrailMatchAction({ action: "block", enforced: false }, false)).toBe("monitored");
		expect(guardrailMatchAction({ action: "redact", enforced: false }, false)).toBe("monitored");
	});
});

describe("guardrailMatchActionHint", () => {
	it("says what a monitored rule would have done", () => {
		expect(guardrailMatchActionHint({ action: "block", enforced: false }, "monitored")).toContain("would have blocked");
		expect(guardrailMatchActionHint({ enforced: false }, "monitored")).not.toContain("would have");
	});
});