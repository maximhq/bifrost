import { describe, expect, it } from "vitest";
import { getPasswordPolicyFailures, isRedacted } from "./validation";

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
