import { afterEach, describe, expect, it, vi } from "vitest";
import { getApiBaseUrl, getEndpointUrl, getExampleBaseUrl, getWebSocketUrl } from "./port";

/** Stubs a production `window` at https://abc.com with the injected base path. */
function stubWindow(basePath: string | undefined) {
	vi.stubGlobal("window", {
		__BIFROST_BASE_PATH__: basePath,
		location: { protocol: "https:", host: "abc.com", port: "", pathname: "/", search: "" },
	});
}

afterEach(() => {
	vi.unstubAllGlobals();
});

describe("port URLs", () => {
	it("stay at the root without a base path", () => {
		stubWindow(undefined);
		expect(getApiBaseUrl()).toBe("/api");
		expect(getWebSocketUrl("/ws")).toBe("wss://abc.com/ws");
		expect(getExampleBaseUrl()).toBe("https://abc.com");
		expect(getEndpointUrl("/api/version")).toBe("/api/version");
	});

	it("carry the base path when one is injected", () => {
		stubWindow("/bifrost");
		expect(getApiBaseUrl()).toBe("/bifrost/api");
		expect(getWebSocketUrl("/ws")).toBe("wss://abc.com/bifrost/ws");
		expect(getExampleBaseUrl()).toBe("https://abc.com/bifrost");
		expect(getEndpointUrl("/api/version")).toBe("/bifrost/api/version");
	});
});