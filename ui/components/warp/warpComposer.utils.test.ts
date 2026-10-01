import { describe, expect, it } from "vitest";
import { hasProviderIcon, warpModelLabel } from "./warpComposer.utils";

describe("warpModelLabel", () => {
	it("leaves the model alone when the provider has an icon", () => {
		expect(hasProviderIcon("openai")).toBe(true);
		expect(warpModelLabel("openai", "gpt-4o")).toBe("gpt-4o");
	});

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