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

	it("pairs each choice with the label the editor passed", () => {
		expect(contentLoggingOptions({ inherit: "Inherit", off: "Off for this team", on: "On for this team" })).toEqual([
			{ value: "inherit", label: "Inherit" },
			{ value: "disabled", label: "Off for this team" },
			{ value: "enabled", label: "On for this team" },
		]);
	});
});