import { describe, expect, it } from "vitest";
import { getPasswordPolicyFailures, hasCopilotApiToken, isRedacted } from "./validation";

describe("isRedacted", () => {
	it.each(["<redacted>", "<REDACTED>", "[redacted]", "[REDACTED]"])("recognizes the backend sentinel %s", (value) => {
		expect(isRedacted(value)).toBe(true);
	});

	it("does not classify a real password as redacted", () => {
		expect(isRedacted("StrongPassword1!")).toBe(false);
	});
});

describe("getPasswordPolicyFailures", () => {
	it.each([
		["<redacted>", ["at least 12 characters", "one uppercase letter", "one number"]],
		["[REDACTED]", ["at least 12 characters", "one lowercase letter", "one number"]],
	])("validates a newly entered sentinel %s", (password, expectedFailures) => {
		expect(getPasswordPolicyFailures(password, false)).toEqual(expectedFailures);
	});

	it.each(["<redacted>", "[REDACTED]"])("skips an unchanged server sentinel %s", (password) => {
		expect(getPasswordPolicyFailures(password, true)).toEqual([]);
	});

	it("accepts a strong newly entered password", () => {
		expect(getPasswordPolicyFailures("StrongPassword1!", false)).toEqual([]);
	});
});
describe("hasCopilotApiToken", () => {
	// key.value is read as a bare string in some places in the provider form and as a
	// SecretVar object in others, so a helper that only understands one shape reports "no
	// token" for a key that has one, and the App-credential labels then contradict the
	// section note telling the operator they can leave those fields blank.
	it("recognises a bare string token", () => {
		expect(hasCopilotApiToken("tid=abc")).toBe(true);
	});

	it("recognises a SecretVar literal token", () => {
		expect(hasCopilotApiToken({ value: "tid=abc", ref: "" })).toBe(true);
	});

	it("recognises a SecretVar reference token", () => {
		expect(hasCopilotApiToken({ value: "", ref: "COPILOT_TOKEN", type: "env" })).toBe(true);
	});

	it.each([undefined, null, "", "   ", { value: "", ref: "" }, { value: "  ", ref: "  " }, {}])(
		"treats %p as no token",
		(input) => {
			expect(hasCopilotApiToken(input as never)).toBe(false);
		},
	);
});
