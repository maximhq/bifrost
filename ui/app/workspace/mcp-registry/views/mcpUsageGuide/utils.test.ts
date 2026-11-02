import { afterEach, describe, expect, it, vi } from "vitest";
import type { CoreConfig } from "@/lib/types/config";
import { getExternalBaseUrl } from "./utils";

/** Stubs a browser `window` at the given origin with the injected server.base_path. */
function stubWindow({ protocol, hostname, port, basePath }: { protocol: string; hostname: string; port: string; basePath?: string }) {
	const host = port ? `${hostname}:${port}` : hostname;
	vi.stubGlobal("window", {
		__BIFROST_BASE_PATH__: basePath,
		location: { protocol, hostname, host, port, origin: `${protocol}//${host}`, pathname: "/", search: "" },
	});
}

afterEach(() => {
	vi.unstubAllGlobals();
	vi.unstubAllEnvs();
});

describe("getExternalBaseUrl", () => {
	it("returns the window origin at the domain root", () => {
		stubWindow({ protocol: "https:", hostname: "abc.com", port: "" });
		expect(getExternalBaseUrl()).toBe("https://abc.com");
	});

	it("appends server.base_path to the window origin", () => {
		stubWindow({ protocol: "https:", hostname: "abc.com", port: "", basePath: "/bifrost" });
		expect(getExternalBaseUrl()).toBe("https://abc.com/bifrost");
	});

	it("keeps the real origin of a production build served on a non-default localhost port", () => {
		// The e2e workers (and any `-port` override) serve the built UI from Go on
		// localhost:18181+; the gateway is that origin, not a guessed :8080.
		stubWindow({ protocol: "http:", hostname: "localhost", port: "18183", basePath: "/bifrost" });
		expect(getExternalBaseUrl()).toBe("http://localhost:18183/bifrost");
	});

	it("points a Vite dev build at the Go server port plus server.base_path", () => {
		vi.stubEnv("NODE_ENV", "development");
		stubWindow({ protocol: "http:", hostname: "localhost", port: "3000", basePath: "/bifrost" });
		expect(getExternalBaseUrl()).toBe("http://localhost:8080/bifrost");
	});

	it("returns a configured external client URL verbatim, without appending the base path", () => {
		stubWindow({ protocol: "https:", hostname: "abc.com", port: "", basePath: "/bifrost" });
		const clientConfig = { mcp_external_client_url: { value: "https://public.example/gw/" } } as unknown as CoreConfig;
		expect(getExternalBaseUrl(clientConfig)).toBe("https://public.example/gw");
	});
});

describe("getExternalBaseUrl for the agent gateway", () => {
	it("reads a2a_external_client_url when asked, ignoring the MCP setting", () => {
		stubWindow({ protocol: "https:", hostname: "abc.com", port: "", basePath: "/bifrost" });
		const clientConfig = {
			mcp_external_client_url: { value: "https://mcp.example" },
			a2a_external_client_url: { value: "https://agents.example/" },
		} as unknown as CoreConfig;
		expect(getExternalBaseUrl(clientConfig, "a2a_external_client_url")).toBe("https://agents.example");
	});

	it("falls back to origin plus base path when a2a_external_client_url is unset", () => {
		stubWindow({ protocol: "https:", hostname: "abc.com", port: "", basePath: "/bifrost" });
		const clientConfig = { mcp_external_client_url: { value: "https://mcp.example" } } as unknown as CoreConfig;
		expect(getExternalBaseUrl(clientConfig, "a2a_external_client_url")).toBe("https://abc.com/bifrost");
	});
});