import { describe, expect, it } from "vitest";
import { hasProviderIcon, warpModelLabel } from "./warpComposer.utils";

describe("warpModelLabel", () => {
	// A provider with a mark needs no text: the icon already says who is
	// answering, and this row truncates.
	it("leaves the model alone when the provider has an icon", () => {
		expect(hasProviderIcon("openai")).toBe(true);
		expect(warpModelLabel("openai", "gpt-4o")).toBe("gpt-4o");
	});

	// The case that was silently losing information: a custom or self-hosted
	// provider renders no icon, so without this the row shows a bare model name
	// and nothing about who is serving it.
	it("names the provider when there is no icon for it", () => {
		expect(hasProviderIcon("my-internal-llm")).toBe(false);
		expect(warpModelLabel("my-internal-llm", "llama-3.1-70b")).toBe("my-internal-llm · llama-3.1-70b");
	});

	it("falls back to the provider alone when no model is set", () => {
		expect(warpModelLabel("my-internal-llm", undefined)).toBe("my-internal-llm");
	});

	it("renders nothing extra when no provider is configured", () => {
		expect(warpModelLabel(undefined, "gpt-4o")).toBe("gpt-4o");
		expect(warpModelLabel("", "gpt-4o")).toBe("gpt-4o");
	});
});