import { describe, expect, it } from "vitest";
import { agentEndpointOrigin, configuredAgentEndpointOrigin, suggestedAgentEndpoints } from "./agentCard";

describe("configured agent endpoint origins", () => {
	it.each([
		"https://api.example.com/agent",
		"https://api.example.com/",
		"https://api.example.com?",
		"https://api.example.com#",
		"https://api.example.com\\agent",
		"https://user:secret@api.example.com",
		"https:api.example.com",
		"api.example.com:443",
		"",
	])("does not approve an invalid configured entry: %s", (value) => {
		expect(configuredAgentEndpointOrigin(value)).toBeUndefined();
	});

	it("canonicalizes valid origins while extracting origins from advertised paths", () => {
		expect(configuredAgentEndpointOrigin(" HTTPS://API.EXAMPLE.COM:443 ")).toBe("https://api.example.com");
		expect(configuredAgentEndpointOrigin("http://[::1]:9000")).toBe("http://[::1]:9000");
		expect(agentEndpointOrigin("https://api.example.com/agent")).toBe("https://api.example.com");
		expect(agentEndpointOrigin("api.example.com:9000", "GRPC")).toBe("https://api.example.com:9000");
	});

	it("keeps invalid configured entries from hiding an advertised suggestion", () => {
		const interfaces = [{ url: "https://api.example.com/agent", protocol_binding: "JSONRPC", protocol_version: "1.0" }];
		expect(suggestedAgentEndpoints("https://cards.example.com/card", interfaces, ["https://api.example.com/agent"])).toEqual([
			"https://api.example.com",
		]);
		expect(suggestedAgentEndpoints("https://cards.example.com/card", interfaces, [" https://api.example.com:443 "])).toEqual([]);
	});
});
