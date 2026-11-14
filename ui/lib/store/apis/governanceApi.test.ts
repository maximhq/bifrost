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