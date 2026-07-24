import { describe, expect, it } from "vitest";
import { applyRedactionMapping, applyRedactionMappingToValue, hasRedactionMappingEntries, mergeRedactionMappings } from "./redaction";

describe("redaction reveal helpers", () => {
	it("requires at least one phase mapping", () => {
		expect(hasRedactionMappingEntries()).toBe(false);
		expect(hasRedactionMappingEntries({ input: {}, output: {} })).toBe(false);
		expect(hasRedactionMappingEntries({ input: { "EMAIL-1": "private@example.com" } })).toBe(true);
	});

	it("reveals placeholders without mutating structured log data", () => {
		const source = { owner: "[EMAIL-1]", nested: ["hello [NAME-1]"] };
		const revealed = applyRedactionMappingToValue(source, {
			"EMAIL-1": "private@example.com",
			"NAME-1": "Madhu",
		});

		expect(revealed).toEqual({ owner: "private@example.com", nested: ["hello Madhu"] });
		expect(source).toEqual({ owner: "[EMAIL-1]", nested: ["hello [NAME-1]"] });
	});

	it("reveals each source placeholder once without reprocessing replacement text", () => {
		expect(applyRedactionMapping("[A] [B]", { A: "[B]", B: "$&" })).toBe("[B] $&");
	});

	it("leaves conflicting phase placeholders redacted in mixed fields", () => {
		const merged = mergeRedactionMappings({
			input: { "SECRET-1": "input", "INPUT-ONLY": "request" },
			output: { "SECRET-1": "output", "OUTPUT-ONLY": "response" },
		});

		expect(applyRedactionMapping("[SECRET-1] [INPUT-ONLY] [OUTPUT-ONLY]", merged)).toBe("[SECRET-1] request response");
	});

	it("keeps identical phase mappings revealable", () => {
		const merged = mergeRedactionMappings({ input: { "SECRET-1": "same" }, output: { "SECRET-1": "same" } });
		expect(applyRedactionMapping("[SECRET-1]", merged)).toBe("same");
	});
});