import type { AnalyzerConfig, ModelRouterRouting, ModelRouterTarget } from "@/lib/types/complexityRouter";
import { configureStore } from "@reduxjs/toolkit";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { baseApi, getErrorMessage } from "./baseApi";
import { governanceApi } from "./governanceApi";

// The API base is relative in a browser; a request built outside one needs a host.
vi.mock("@/lib/utils/port", async (importOriginal) => ({
	...(await importOriginal<typeof import("@/lib/utils/port")>()),
	getApiBaseUrl: () => "http://gateway.test/api",
}));

interface SentRequest {
	method: string;
	// Path and query string, as the gateway receives them.
	path: string;
	// The body as sent, and parsed; undefined when the request has none.
	text: string | undefined;
	body: unknown;
}

const endpoints = governanceApi.endpoints;

describe("model router endpoints", () => {
	let sent: SentRequest[];
	// What the gateway answers the next requests with; a 200 with an empty object unless set.
	let answer: (request: SentRequest) => Response;
	let store: ReturnType<typeof newStore>;

	const newStore = () =>
		configureStore({
			reducer: { [baseApi.reducerPath]: baseApi.reducer },
			middleware: (getDefaultMiddleware) => getDefaultMiddleware().concat(baseApi.middleware),
		});
	const json = (value: unknown, status = 200) =>
		new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
	const requests = (method: string, path: string) => sent.filter((request) => request.method === method && request.path === path);
	const last = () => {
		const request = sent.at(-1);
		if (!request) throw new Error("nothing was sent");
		return request;
	};

	beforeEach(() => {
		sent = [];
		answer = () => json({});
		store = newStore();
		vi.stubGlobal(
			"fetch",
			vi.fn(async (input: Request) => {
				const url = new URL(input.url);
				const text = (await input.clone().text()) || undefined;
				const request: SentRequest = {
					method: input.method,
					path: url.pathname + url.search,
					text,
					body: text ? JSON.parse(text) : undefined,
				};
				sent.push(request);
				return answer(request);
			}),
		);
	});

	afterEach(() => {
		vi.unstubAllGlobals();
	});

	describe("reads", () => {
		test("the list, one router, a new router's defaults and a router's status each have their own address", async () => {
			await store.dispatch(endpoints.getModelRouters.initiate());
			await store.dispatch(endpoints.getModelRouter.initiate("support-desk"));
			await store.dispatch(endpoints.getModelRouterDefaults.initiate());
			await store.dispatch(endpoints.getModelRouterStatus.initiate("support-desk"));
			expect(sent.map(({ method, path, text }) => [method, path, text])).toEqual([
				["GET", "/api/routing/model-routers", undefined],
				["GET", "/api/routing/model-routers/support-desk", undefined],
				["GET", "/api/routing/model-routers/defaults", undefined],
				["GET", "/api/routing/model-routers/support-desk/status", undefined],
			]);
		});

		test("a router id is escaped, so it cannot reach another address", async () => {
			await store.dispatch(endpoints.getModelRouter.initiate("team a/defaults?x=1"));
			expect(last().path).toBe("/api/routing/model-routers/team%20a%2Fdefaults%3Fx%3D1");
		});
	});

	describe("creating a router", () => {
		const config: AnalyzerConfig = {
			classifier: "semantic",
			keywords: { simple_keywords: ["hi"], medium_keywords: ["summarize this"], complex_keywords: ["prove this"] },
		};

		test("posts the name, description and classifier as given, with no id, targets or routing of its own", async () => {
			await store.dispatch(endpoints.createModelRouter.initiate({ ...config, name: "Support", description: "Customer tickets" })).unwrap();
			expect(last()).toMatchObject({ method: "POST", path: "/api/routing/model-routers" });
			expect(last().body).toEqual({ ...config, name: "Support", description: "Customer tickets" });
		});

		test("gives the caller the router the gateway made, id included", async () => {
			answer = () => json({ id: "b7f3c2", name: "Support", ...config, routing: { enabled: false, scope: "global", priority: 4 } });
			const created = await store.dispatch(endpoints.createModelRouter.initiate({ ...config, name: "Support" })).unwrap();
			expect(created.id).toBe("b7f3c2");
			expect(created.routing.enabled).toBe(false);
		});
	});

	describe("saving a router's details and classifier", () => {
		test("a rename sends only the name and description, so the stored classifier is kept", async () => {
			await store.dispatch(endpoints.updateModelRouter.initiate({ id: "support-desk", name: "Support EU", description: "" })).unwrap();
			expect(last()).toMatchObject({ method: "PUT", path: "/api/routing/model-routers/support-desk" });
			expect(last().text).toBe('{"name":"Support EU","description":""}');
		});

		test("a classifier save sends the classifier settings without the id in the body", async () => {
			const classifier = {
				classifier: "llm" as const,
				keywords: { simple_keywords: ["hi"], medium_keywords: ["summarize this"], complex_keywords: ["prove this"] },
				llm: { provider: "openai", model: "gpt-4o-mini" },
			};
			await store.dispatch(endpoints.updateModelRouter.initiate({ ...classifier, id: "support-desk" })).unwrap();
			expect(last().path).toBe("/api/routing/model-routers/support-desk");
			expect(last().body).toEqual(classifier);
		});
	});

	describe("saving a router's models", () => {
		const targets: ModelRouterTarget[] = [
			{ tier: "SIMPLE", provider: "openai", model: "gpt-4o-mini", weight: 0.6, fallbacks: ["anthropic/claude-haiku"] },
			{ tier: "SIMPLE", model: "gpt-4o", weight: 0.4, fallbacks: [] },
			{
				tier: "COMPLEX",
				provider: "anthropic",
				model: "claude-opus",
				key_id: "key-9",
				weight: 1,
				fallbacks: [{ provider: "azure", key_id: "key-2" }],
			},
		];

		test("puts the targets, and nothing else, on the targets address", async () => {
			await store.dispatch(endpoints.setModelRouterTargets.initiate({ id: "support-desk", targets })).unwrap();
			expect(last()).toMatchObject({ method: "PUT", path: "/api/routing/model-routers/support-desk/targets" });
			expect(last().body).toEqual({ targets });
		});

		test("a provider, model or key left unset is not on the wire at all", async () => {
			const unset: ModelRouterTarget[] = [
				{ tier: "MEDIUM", provider: undefined, model: "gpt-4o", key_id: undefined, weight: 1, fallbacks: [] },
			];
			await store.dispatch(endpoints.setModelRouterTargets.initiate({ id: "support-desk", targets: unset })).unwrap();
			expect(last().text).toBe('{"targets":[{"tier":"MEDIUM","model":"gpt-4o","weight":1,"fallbacks":[]}]}');
		});

		test("clearing them sends an empty list, which the gateway reads as clear, not a missing field", async () => {
			await store.dispatch(endpoints.setModelRouterTargets.initiate({ id: "support-desk", targets: [] })).unwrap();
			expect(last().text).toBe('{"targets":[]}');
		});
	});

	describe("saving a router's routing", () => {
		const builderQuery = { combinator: "and", rules: [{ field: "model", operator: "=", value: "gpt-4o" }] };

		test("the on/off switch sends enabled alone, so the gateway keeps the scope, condition and priority", async () => {
			await store.dispatch(endpoints.setModelRouterRouting.initiate({ id: "support-desk", routing: { enabled: false } })).unwrap();
			expect(last()).toMatchObject({ method: "PUT", path: "/api/routing/model-routers/support-desk/routing" });
			expect(last().text).toBe('{"enabled":false}');
			await store.dispatch(endpoints.setModelRouterRouting.initiate({ id: "support-desk", routing: { enabled: true } })).unwrap();
			expect(last().text).toBe('{"enabled":true}');
		});

		test("sends the routing fields themselves, not wrapped in another object", async () => {
			const routing: Omit<ModelRouterRouting, "enabled"> = {
				scope: "team",
				scope_id: "team-1",
				cel_expression: "model == 'gpt-4o'",
				query: builderQuery,
				priority: 3,
			};
			await store.dispatch(endpoints.setModelRouterRouting.initiate({ id: "support-desk", routing })).unwrap();
			expect(last().body).toEqual(routing);
			expect(last().body, "the sheet leaves on/off to the switch").not.toHaveProperty("enabled");
		});

		test("a global scope leaves scope_id off the wire", async () => {
			const routing: Omit<ModelRouterRouting, "enabled"> = {
				scope: "global",
				scope_id: undefined,
				cel_expression: "",
				query: builderQuery,
				priority: 0,
			};
			await store.dispatch(endpoints.setModelRouterRouting.initiate({ id: "support-desk", routing })).unwrap();
			expect(last().body).not.toHaveProperty("scope_id");
			expect(last().body).toMatchObject({ scope: "global", cel_expression: "", priority: 0 });
		});

		test("a hand-written CEL condition goes without builder state, which is what clears the stored one", async () => {
			const routing: Omit<ModelRouterRouting, "enabled"> = {
				scope: "global",
				scope_id: undefined,
				cel_expression: "headers['x-tier'] == 'gold'",
				query: undefined,
				priority: 2,
			};
			await store.dispatch(endpoints.setModelRouterRouting.initiate({ id: "support-desk", routing })).unwrap();
			expect(last().text).toBe(`{"scope":"global","cel_expression":"headers['x-tier'] == 'gold'","priority":2}`);
		});

		test("a priority of zero and an emptied condition are sent, not dropped as blank", async () => {
			await store
				.dispatch(endpoints.setModelRouterRouting.initiate({ id: "support-desk", routing: { cel_expression: "", priority: 0 } }))
				.unwrap();
			expect(last().text).toBe('{"cel_expression":"","priority":0}');
		});

		test("a refused save reaches the caller with the gateway's own reason", async () => {
			answer = () => json({ error: { message: "priority 3 is taken in this scope; use a different priority" } }, 409);
			const refused = await store
				.dispatch(endpoints.setModelRouterRouting.initiate({ id: "support-desk", routing: { priority: 3 } }))
				.unwrap()
				.catch((error: unknown) => error);
			expect(refused).toMatchObject({ status: 409 });
			expect(getErrorMessage(refused)).toBe("Priority 3 is taken in this scope; use a different priority");
		});
	});

	describe("a router's other actions", () => {
		test("deleting, restoring its phrases and retrying its warmup each send no body", async () => {
			await store.dispatch(endpoints.deleteModelRouter.initiate("support-desk")).unwrap();
			await store.dispatch(endpoints.resetModelRouter.initiate("support-desk")).unwrap();
			await store.dispatch(endpoints.retryModelRouterWarmup.initiate("support-desk")).unwrap();
			expect(sent.map(({ method, path, text }) => [method, path, text])).toEqual([
				["DELETE", "/api/routing/model-routers/support-desk", undefined],
				["POST", "/api/routing/model-routers/support-desk/reset", undefined],
				["POST", "/api/routing/model-routers/support-desk/status/retry", undefined],
			]);
		});
	});

	describe("what a save refreshes", () => {
		const LIST = "/api/routing/model-routers";
		const ROUTER = "/api/routing/model-routers/support-desk";
		const STATUS = "/api/routing/model-routers/support-desk/status";
		const DEFAULTS = "/api/routing/model-routers/defaults";
		const SUGGESTIONS = "/api/routing/model-routers/support-desk/suggestions?status=open&limit=1";

		// Opens the reads a routers page keeps on screen, as mounted components do.
		const openPage = async () => {
			await Promise.all([
				store.dispatch(endpoints.getModelRouters.initiate()),
				store.dispatch(endpoints.getModelRouter.initiate("support-desk")),
				store.dispatch(endpoints.getModelRouterStatus.initiate("support-desk")),
				store.dispatch(endpoints.getModelRouterDefaults.initiate()),
				store.dispatch(endpoints.getComplexitySuggestions.initiate({ statuses: ["open"], routerId: "support-desk", limit: 1 })),
			]);
			expect(sent).toHaveLength(5);
		};
		const reads = () => ({
			list: requests("GET", LIST).length,
			router: requests("GET", ROUTER).length,
			status: requests("GET", STATUS).length,
			defaults: requests("GET", DEFAULTS).length,
			suggestions: requests("GET", SUGGESTIONS).length,
		});
		const settled = (expected: ReturnType<typeof reads>) => vi.waitFor(() => expect(reads()).toEqual(expected));

		test("turning a router on or off re-reads the list, the router and its status", async () => {
			await openPage();
			await store.dispatch(endpoints.setModelRouterRouting.initiate({ id: "support-desk", routing: { enabled: true } })).unwrap();
			await settled({ list: 2, router: 2, status: 2, defaults: 1, suggestions: 1 });
		});

		test("saving the models re-reads the list and the router", async () => {
			await openPage();
			await store.dispatch(endpoints.setModelRouterTargets.initiate({ id: "support-desk", targets: [] })).unwrap();
			await settled({ list: 2, router: 2, status: 2, defaults: 1, suggestions: 1 });
		});

		test("saving the classifier also re-reads the suggestions, which follow its settings", async () => {
			await openPage();
			await store.dispatch(endpoints.updateModelRouter.initiate({ id: "support-desk", name: "Support EU" })).unwrap();
			await settled({ list: 2, router: 2, status: 2, defaults: 1, suggestions: 2 });
		});

		test("creating and deleting a router re-read the list", async () => {
			await openPage();
			const keywords = { simple_keywords: ["hi"], medium_keywords: ["summarize this"], complex_keywords: ["prove this"] };
			await store.dispatch(endpoints.createModelRouter.initiate({ classifier: "semantic", keywords, name: "Billing" })).unwrap();
			await settled({ list: 2, router: 2, status: 2, defaults: 1, suggestions: 1 });
			await store.dispatch(endpoints.deleteModelRouter.initiate("billing")).unwrap();
			await settled({ list: 3, router: 3, status: 3, defaults: 1, suggestions: 1 });
		});
	});
});
describe("model config lists", () => {
	const MCP_PAGE_1 = "/api/governance/model-configs?limit=2&offset=0&scope=global%2Cvirtual_key&applies_to=mcp";
	const MCP_PAGE_2 = "/api/governance/model-configs?limit=2&offset=2&scope=global%2Cvirtual_key&applies_to=mcp";
	const MODELS_PAGE = "/api/governance/model-configs?limit=25&offset=0";
	const holder = (id: string, scope: string) => ({
		id,
		model_name: "*",
		scope: "virtual_key",
		scope_id: id,
		budgets: [{ id: `${id}-b`, scope }],
	});

	let gets: string[];
	let rows: Record<string, unknown>;
	let store: ReturnType<typeof newStore>;
	const newStore = () =>
		configureStore({
			reducer: { [baseApi.reducerPath]: baseApi.reducer },
			middleware: (getDefaultMiddleware) => getDefaultMiddleware().concat(baseApi.middleware),
		});
	const json = (value: unknown) => new Response(JSON.stringify(value), { status: 200, headers: { "Content-Type": "application/json" } });
	const page = (...ids: string[]) => ({ model_configs: ids.map((id) => rows[id]), count: ids.length, total_count: 4 });
	const listed = (args: Parameters<typeof endpoints.getModelConfigs.select>[0]) =>
		(endpoints.getModelConfigs.select(args)(store.getState()).data?.model_configs ?? []).map((mc) => mc.id);
	const MCP_ARGS_1 = { applies_to: "mcp" as const, limit: 2, offset: 0, scope: "global,virtual_key" };
	const MCP_ARGS_2 = { ...MCP_ARGS_1, offset: 2 };
	const MODELS_ARGS = { limit: 25, offset: 0 };

	beforeEach(async () => {
		gets = [];
		rows = { a: holder("a", "mcp"), b: holder("b", "mcp"), c: holder("c", "mcp"), d: holder("d", "shared"), e: holder("e", "model") };
		store = newStore();
		vi.stubGlobal(
			"fetch",
			vi.fn(async (input: Request) => {
				const url = new URL(input.url);
				const path = url.pathname + url.search;
				if (input.method === "GET") gets.push(path);
				if (path === MCP_PAGE_1) return json(page("a", "b"));
				if (path === MCP_PAGE_2) return json(page("c", "d"));
				if (path === MODELS_PAGE) return json({ ...page("a", "b", "c", "d", "e"), total_count: 5 });
				const body = (await input.clone().text()) || "{}";
				const id = url.pathname.split("/").at(-1) ?? "";
				return json({ model_config: { ...(rows[id] as object), ...JSON.parse(body), id: input.method === "POST" ? "new" : id } });
			}),
		);
		await Promise.all([
			store.dispatch(endpoints.getModelConfigs.initiate(MCP_ARGS_1)),
			store.dispatch(endpoints.getModelConfigs.initiate(MCP_ARGS_2)),
			store.dispatch(endpoints.getModelConfigs.initiate(MODELS_ARGS)),
		]);
		expect(gets).toHaveLength(3);
	});

	afterEach(() => {
		vi.unstubAllGlobals();
	});

	const reads = () => ({ mcp1: gets.filter((g) => g === MCP_PAGE_1).length, mcp2: gets.filter((g) => g === MCP_PAGE_2).length });

	test("an edit that keeps a row on the MCP page patches the page it is on, and no other", async () => {
		await store
			.dispatch(
				endpoints.updateModelConfig.initiate({ id: "c", data: { budgets: [{ max_limit: 9, reset_duration: "1d", scope: "mcp" }] } }),
			)
			.unwrap();
		expect(listed(MCP_ARGS_1)).toEqual(["a", "b"]);
		expect(listed(MCP_ARGS_2)).toEqual(["c", "d"]);
		expect(endpoints.getModelConfigs.select(MCP_ARGS_1)(store.getState()).data?.total_count).toBe(4);
		// Give a refetch the chance to start, so the count shows that none was asked for.
		await new Promise((resolve) => setTimeout(resolve, 50));
		expect(reads()).toEqual({ mcp1: 1, mcp2: 1 });
	});

	test("a row joining the MCP page re-reads its pages instead of adding itself to each", async () => {
		await store
			.dispatch(
				endpoints.updateModelConfig.initiate({ id: "e", data: { budgets: [{ max_limit: 9, reset_duration: "1d", scope: "mcp" }] } }),
			)
			.unwrap();
		await vi.waitFor(() => expect(reads()).toEqual({ mcp1: 2, mcp2: 2 }));
		expect(listed(MCP_ARGS_1)).toEqual(["a", "b"]);
		expect(gets.filter((g) => g === MODELS_PAGE)).toHaveLength(1);
	});

	test("a row leaving the MCP page re-reads its pages", async () => {
		await store
			.dispatch(
				endpoints.updateModelConfig.initiate({ id: "a", data: { budgets: [{ max_limit: 9, reset_duration: "1d", scope: "model" }] } }),
			)
			.unwrap();
		await vi.waitFor(() => expect(reads()).toEqual({ mcp1: 2, mcp2: 2 }));
	});

	test("a row narrowed off the MCP page re-reads the pages that held it", async () => {
		await store.dispatch(endpoints.updateModelConfig.initiate({ id: "a", data: { provider: "openai" } })).unwrap();
		await vi.waitFor(() => expect(reads()).toEqual({ mcp1: 2, mcp2: 2 }));
	});

	test("a new row with an MCP line re-reads the MCP pages and is not added to a later one", async () => {
		await store
			.dispatch(
				endpoints.createModelConfig.initiate({
					model_name: "*",
					scope: "global",
					budgets: [{ max_limit: 5, reset_duration: "1M", scope: "mcp" }],
				}),
			)
			.unwrap();
		expect(listed(MCP_ARGS_2)).not.toContain("new");
		await vi.waitFor(() => expect(reads()).toEqual({ mcp1: 2, mcp2: 2 }));
	});

	test("the holder lookup asks for the holder-level row alone", async () => {
		await store.dispatch(endpoints.getModelConfigs.initiate({ scope: "virtual_key", scope_id: "vk-1", holder_level: true }));
		expect(gets.at(-1)).toBe("/api/governance/model-configs?scope=virtual_key&scope_id=vk-1&holder_level=true");
	});
});