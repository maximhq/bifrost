import { afterEach, describe, expect, it, vi } from "vitest";
import { relayPath } from "./relayPath";

afterEach(() => {
	vi.unstubAllGlobals();
});

describe("relayPath", () => {
	it("is root-relative at the domain root", () => {
		vi.stubGlobal("window", { __BIFROST_BASE_PATH__: undefined });
		expect(relayPath("voice")).toBe("/relay/voice");
		expect(relayPath("<name>")).toBe("/relay/<name>");
	});

	it("carries server.base_path when Bifrost is mounted under a prefix", () => {
		vi.stubGlobal("window", { __BIFROST_BASE_PATH__: "/bifrost" });
		expect(relayPath("voice")).toBe("/bifrost/relay/voice");
		expect(relayPath("<name>")).toBe("/bifrost/relay/<name>");
	});

	it("ignores an invalid injected prefix", () => {
		vi.stubGlobal("window", { __BIFROST_BASE_PATH__: "//evil.com" });
		expect(relayPath("voice")).toBe("/relay/voice");
	});
});