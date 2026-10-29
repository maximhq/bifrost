import type { CustomProviderConfig, ModelProvider, PassthroughPolicy } from "@/lib/types/config";
import { passthroughFormSchema, passthroughPathIssue, passthroughPolicySchema, type PassthroughFormSchema } from "@/lib/types/schemas";
import { describe, expect, it } from "vitest";
import {
	emptyRuleRow,
	passthroughChanged,
	passthroughModeOf,
	passthroughModeOptions,
	passthroughRoutePrefix,
	passthroughUpdates,
	rulesForCustomMode,
	savedPassthroughFormValues,
	toPassthroughPolicy,
	withPassthroughRequestsAllowed,
} from "./passthroughForm.utils";

const custom = (base: string) => ({ base_provider_type: base }) as CustomProviderConfig;
const provider = (name: string, customConfig?: CustomProviderConfig) =>
	({ name, custom_provider_config: customConfig }) as Pick<ModelProvider, "name" | "custom_provider_config">;

const allRoutes: PassthroughPolicy = { allowed_routes: [{ path: "/**" }] };
const files: PassthroughPolicy = { allowed_routes: [{ methods: ["GET", "POST"], path: "/v1/files" }, { path: "/v1/batches/**" }] };

describe("passthroughRoutePrefix", () => {
	it("names the route for built-in providers with a passthrough endpoint", () => {
		expect(passthroughRoutePrefix(provider("openai"))).toBe("/openai_passthrough");
		expect(passthroughRoutePrefix(provider("anthropic"))).toBe("/anthropic_passthrough");
		expect(passthroughRoutePrefix(provider("azure"))).toBe("/azure_passthrough");
		expect(passthroughRoutePrefix(provider("gemini"))).toBe("/genai_passthrough");
		expect(passthroughRoutePrefix(provider("vertex"))).toBe("/genai_passthrough");
	});

	it("uses the base provider's route for a custom provider", () => {
		expect(passthroughRoutePrefix(provider("my-oai", custom("openai")))).toBe("/openai_passthrough");
		expect(passthroughRoutePrefix(provider("my-claude", custom("anthropic")))).toBe("/anthropic_passthrough");
		expect(passthroughRoutePrefix(provider("my-gemini", custom("gemini")))).toBe("/genai_passthrough");
		expect(passthroughRoutePrefix(provider("my-bedrock", custom("bedrock")))).toBe("/bedrock_passthrough");
	});

	it("has no route, and so no tab, for other providers", () => {
		expect(passthroughRoutePrefix(provider("groq"))).toBeUndefined();
		// Built-in Bedrock already limits passthrough to its own few operations.
		expect(passthroughRoutePrefix(provider("bedrock"))).toBeUndefined();
		expect(passthroughRoutePrefix(provider("my-cohere", custom("cohere")))).toBeUndefined();
	});
});

describe("passthroughModeOf", () => {
	it("reads an absent policy as the provider kind's default", () => {
		expect(passthroughModeOf(undefined, false)).toBe("all");
		expect(passthroughModeOf(null, false)).toBe("all");
		expect(passthroughModeOf(null, true)).toBe("off");
	});

	it("reads an empty list as off", () => {
		expect(passthroughModeOf({ allowed_routes: [] }, false)).toBe("off");
		expect(passthroughModeOf({ allowed_routes: [] }, true)).toBe("off");
	});

	it("reads the single /** rule as all routes", () => {
		expect(passthroughModeOf(allRoutes, true)).toBe("all");
		expect(passthroughModeOf(allRoutes, false)).toBe("all");
	});

	it("reads everything else as custom", () => {
		expect(passthroughModeOf(files, false)).toBe("custom");
		expect(passthroughModeOf({ allowed_routes: [{ methods: ["GET"], path: "/**" }] }, true)).toBe("custom");
		expect(passthroughModeOf({ allowed_routes: [{ path: "/**" }, { path: "/v1/files" }] }, false)).toBe("custom");
	});
});

describe("passthroughModeOptions", () => {
	it("puts the provider kind's default first", () => {
		expect(passthroughModeOptions(false).map((o) => o.value)).toEqual(["all", "custom", "off"]);
		expect(passthroughModeOptions(true).map((o) => o.value)).toEqual(["off", "all", "custom"]);
	});
});

describe("toPassthroughPolicy", () => {
	const form = (mode: PassthroughFormSchema["mode"], rules: PassthroughFormSchema["rules"] = []): PassthroughFormSchema => ({
		mode,
		rules,
	});

	it("writes null for the provider kind's default", () => {
		expect(toPassthroughPolicy(form("all"), false)).toBeNull();
		expect(toPassthroughPolicy(form("off"), true)).toBeNull();
	});

	it("writes the explicit shape for every other mode", () => {
		expect(toPassthroughPolicy(form("off"), false)).toEqual({ allowed_routes: [] });
		expect(toPassthroughPolicy(form("all"), true)).toEqual(allRoutes);
	});

	it("writes custom rows as rules, methods in canonical order and omitted when empty", () => {
		const policy = toPassthroughPolicy(
			form("custom", [
				{ methods: ["POST", "GET"], path: "/v1/files" },
				{ methods: [], path: "/v1/batches/**" },
			]),
			false,
		);
		expect(policy).toEqual(files);
		expect(JSON.stringify(policy?.allowed_routes[0])).toBe('{"methods":["GET","POST"],"path":"/v1/files"}');
	});
});

describe("savedPassthroughFormValues", () => {
	it("loads rows only for a custom policy", () => {
		expect(savedPassthroughFormValues(allRoutes, true)).toEqual({ mode: "all", rules: [] });
		expect(savedPassthroughFormValues(files, false)).toEqual({
			mode: "custom",
			rules: [
				{ methods: ["GET", "POST"], path: "/v1/files" },
				{ methods: [], path: "/v1/batches/**" },
			],
		});
	});

	it("normalizes stored method case so the picker shows them", () => {
		const values = savedPassthroughFormValues({ allowed_routes: [{ methods: ["post", "get"], path: "/v1/files" }] }, false);
		expect(values.rules[0].methods).toEqual(["GET", "POST"]);
	});

	it("round-trips a custom policy unchanged", () => {
		expect(toPassthroughPolicy(savedPassthroughFormValues(files, true), true)).toEqual(files);
	});
});

describe("rulesForCustomMode", () => {
	it("starts an empty editor with one empty rule", () => {
		expect(rulesForCustomMode([])).toEqual([emptyRuleRow()]);
	});

	it("keeps rows already in the editor, by reference", () => {
		const rules = [{ methods: [], path: "/v1/files" }];
		expect(rulesForCustomMode(rules)).toBe(rules);
	});
});

describe("passthroughChanged", () => {
	it("ignores rows left behind by a mode that does not use them", () => {
		const saved = savedPassthroughFormValues(null, false);
		expect(passthroughChanged({ mode: "all", rules: [{ methods: [], path: "/v1/files" }] }, saved, false)).toBe(false);
	});

	it("treats custom rows equal to a named mode as no change", () => {
		const saved = savedPassthroughFormValues(allRoutes, true);
		expect(passthroughChanged({ mode: "custom", rules: [{ methods: [], path: "/**" }] }, saved, true)).toBe(false);
	});

	it("flags a real change", () => {
		const saved = savedPassthroughFormValues(null, false);
		expect(passthroughChanged({ mode: "custom", rules: [{ methods: [], path: "/v1/files" }] }, saved, false)).toBe(true);
		expect(passthroughChanged({ mode: "off", rules: [] }, saved, false)).toBe(true);
	});
});

describe("withPassthroughRequestsAllowed", () => {
	const allowed = { chat_completion: true, passthrough: false } as CustomProviderConfig["allowed_requests"];

	it("turns both passthrough request types on and keeps the rest of the block", () => {
		const config = { ...custom("openai"), is_key_less: true, allowed_requests: allowed };
		expect(withPassthroughRequestsAllowed(config)).toEqual({
			...config,
			allowed_requests: { chat_completion: true, passthrough: true, passthrough_stream: true },
		});
	});

	it("changes nothing when allowed_requests is absent or both flags are already on", () => {
		expect(withPassthroughRequestsAllowed(undefined)).toBeUndefined();
		expect(withPassthroughRequestsAllowed(custom("openai"))).toBeUndefined();
		expect(
			withPassthroughRequestsAllowed({
				...custom("openai"),
				allowed_requests: { ...allowed!, passthrough: true, passthrough_stream: true },
			}),
		).toBeUndefined();
	});
});

describe("passthroughUpdates", () => {
	const blocked = { ...custom("openai"), allowed_requests: { chat_completion: true } as CustomProviderConfig["allowed_requests"] };

	it("sends only the policy for a built-in provider", () => {
		expect(passthroughUpdates({ mode: "off", rules: [] }, false, undefined)).toEqual({ passthrough: { allowed_routes: [] } });
	});

	it("turns on a custom provider's passthrough request types when it forwards anything", () => {
		expect(passthroughUpdates({ mode: "all", rules: [] }, true, blocked)).toEqual({
			passthrough: allRoutes,
			custom_provider_config: { ...blocked, allowed_requests: { chat_completion: true, passthrough: true, passthrough_stream: true } },
		});
	});

	it("leaves a custom provider's request types alone when passthrough is off", () => {
		expect(passthroughUpdates({ mode: "off", rules: [] }, true, blocked)).toEqual({ passthrough: null });
	});
});

describe("passthroughPathIssue", () => {
	it("accepts the documented shapes", () => {
		for (const path of ["/", "/**", "/v1/files", "/v1/batches/**", "/v1beta/models/*:generateContent", "/v1/*/content"]) {
			expect(passthroughPathIssue(path), path).toBeUndefined();
		}
	});

	it("rejects what the server rejects", () => {
		expect(passthroughPathIssue("")).toBe("Enter a path");
		expect(passthroughPathIssue("v1/files")).toBe("Path must start with /");
		for (const path of ["/v1/files?x=1", "/v1#a", "/v1/%2e", "/v1\\files", "/v1/[a]"]) {
			expect(passthroughPathIssue(path), path).toBe("Path must not contain ?, #, %, \\, [ or ]");
		}
		for (const path of ["/v1//files", "/v1/files/", "/v1/./files", "/v1/../admin"]) {
			expect(passthroughPathIssue(path), path).toBe("Path must not contain empty, . or .. segments");
		}
		for (const path of ["/**/files", "/v1/a**", "/v1/**x"]) {
			expect(passthroughPathIssue(path), path).toBe("** is only allowed as the last path segment");
		}
	});
});

describe("passthroughPolicySchema", () => {
	const issues = (policy: unknown) => {
		const result = passthroughPolicySchema.safeParse(policy);
		return result.success ? [] : result.error.issues.map((issue) => `${issue.path.join(".")}: ${issue.message}`);
	};

	it("accepts path rules and an empty list", () => {
		expect(issues(files)).toEqual([]);
		expect(issues(allRoutes)).toEqual([]);
		expect(issues({ allowed_routes: [] })).toEqual([]);
	});

	it("rejects a rule without a path", () => {
		expect(issues({ allowed_routes: [{}] })).toEqual(["allowed_routes.0.path: Enter a path"]);
		expect(issues({ allowed_routes: [{ methods: ["POST"] }] })).toEqual(["allowed_routes.0.path: Enter a path"]);
	});

	it("rejects an unknown method", () => {
		expect(issues({ allowed_routes: [{ methods: ["CONNECT"], path: "/v1" }] })).toEqual([
			"allowed_routes.0.methods.0: Unsupported method CONNECT",
		]);
	});
});

describe("passthroughFormSchema", () => {
	const issuePaths = (values: PassthroughFormSchema) => {
		const result = passthroughFormSchema.safeParse(values);
		return result.success ? [] : result.error.issues.map((issue) => issue.path.join("."));
	};

	it("checks rows only in custom mode", () => {
		const bad = [{ methods: [], path: "v1" }];
		expect(issuePaths({ mode: "all", rules: bad })).toEqual([]);
		expect(issuePaths({ mode: "custom", rules: bad })).toEqual(["rules.0.path"]);
	});

	it("needs at least one rule in custom mode", () => {
		expect(issuePaths({ mode: "custom", rules: [] })).toEqual(["rules"]);
		expect(issuePaths({ mode: "off", rules: [] })).toEqual([]);
	});

	it("needs a path on every rule in custom mode", () => {
		expect(issuePaths({ mode: "custom", rules: [emptyRuleRow()] })).toEqual(["rules.0.path"]);
	});
});