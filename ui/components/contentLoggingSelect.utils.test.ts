import { describe, expect, it } from "vitest";
import { contentLoggingChoice, contentLoggingOptions, contentLoggingValue } from "./contentLoggingSelect.utils";

describe("content logging choice", () => {
	it("maps the wire value to a choice", () => {
		expect(contentLoggingChoice(true)).toBe("disabled");
		expect(contentLoggingChoice(false)).toBe("enabled");
		expect(contentLoggingChoice(null)).toBe("inherit");
		expect(contentLoggingChoice(undefined)).toBe("inherit");
	});

	it("maps a choice back to the wire value, with inherit as null so an update clears it", () => {
		expect(contentLoggingValue("disabled")).toBe(true);
		expect(contentLoggingValue("enabled")).toBe(false);
		expect(contentLoggingValue("inherit")).toBeNull();
	});

	it("round-trips every wire value", () => {
		for (const value of [true, false, null]) {
			expect(contentLoggingValue(contentLoggingChoice(value))).toBe(value);
		}
	});

	it("labels the choices for the entity", () => {
		expect(contentLoggingOptions("team").map((o) => o.label)).toEqual(["Inherit", "Off for this team", "On for this team"]);
	});
});