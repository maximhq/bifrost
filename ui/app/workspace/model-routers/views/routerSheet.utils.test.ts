import type { ModelRouter, ModelRouterRouting } from "@/lib/types/complexityRouter";
import type { RuleGroupType } from "react-querybuilder";
import { describe, expect, it } from "vitest";
import {
	hasRouterSheetChanges,
	type RouterCondition,
	routerSheetChanges,
	routerSheetDefaults,
	withSavedRouting,
} from "./routerSheet.utils";

const EMPTY_QUERY: RuleGroupType = { combinator: "and", rules: [] };

const router = (routing: Partial<ModelRouterRouting> = {}, rest: Partial<ModelRouter> = {}) =>
	({
		id: "support",
		name: "Support",
		description: "Tickets",
		routing: { enabled: false, scope: "global", priority: 3, cel_expression: "", ...routing },
		...rest,
	}) as unknown as ModelRouter;

const condition = (celExpression = "", mode: RouterCondition["mode"] = "builder"): RouterCondition => ({
	mode,
	celExpression,
	query: EMPTY_QUERY,
});

describe("routerSheetDefaults", () => {
	it("fills the sheet from the saved router, with the priority as text", () => {
		expect(routerSheetDefaults(router({ scope: "team", scope_id: "team-1", priority: 7 }))).toEqual({
			name: "Support",
			description: "Tickets",
			enabled: false,
			scope: "team",
			scope_id: "team-1",
			priority: "7",
		});
	});

	it("opens with the switch on for the Turn on button, leaving the rest as saved", () => {
		const values = routerSheetDefaults(router(), true);
		expect(values.enabled).toBe(true);
		expect(values.scope).toBe("global");
	});

	it("reads a router without a description or scope id as empty fields", () => {
		const values = routerSheetDefaults(router({ scope_id: null }, { description: undefined }));
		expect(values.description).toBe("");
		expect(values.scope_id).toBe("");
	});
});

describe("routerSheetChanges", () => {
	it("sends nothing when nothing changed", () => {
		const saved = router();
		const changes = routerSheetChanges(saved, routerSheetDefaults(saved), condition(), "builder");
		expect(changes).toEqual({});
		expect(hasRouterSheetChanges(changes)).toBe(false);
	});

	it("sends only enabled when only the switch moved, so the routing endpoint keeps the rest", () => {
		const saved = router();
		const changes = routerSheetChanges(saved, routerSheetDefaults(saved, true), condition(), "builder");
		expect(changes).toEqual({ routing: { enabled: true } });
	});

	it("sends only the priority when only the priority changed, leaving scope and condition to the gateway", () => {
		const saved = router();
		const changes = routerSheetChanges(saved, { ...routerSheetDefaults(saved), priority: "5" }, condition(), "builder");
		expect(changes.routing).toEqual({ priority: 5 });
		expect(changes.identity).toBeUndefined();
	});

	it("sends the condition's expression and query together, and nothing else, when only the condition changed", () => {
		const saved = router({ scope: "team", scope_id: "team-1" });
		const changes = routerSheetChanges(saved, routerSheetDefaults(saved), condition('model == "b"'), "builder");
		expect(changes.routing).toEqual({ cel_expression: 'model == "b"', query: EMPTY_QUERY });
	});

	// The routing saves on its own endpoint before the name; when the name then
	// fails, the sheet stays open. What was saved is the new baseline, so
	// switching a field back and retrying sends the reversal.
	it("compares a retry with the routing already saved, so a reversal is sent", () => {
		const opened = router();
		const edited = { ...routerSheetDefaults(opened), priority: "5", name: "Support desk" };
		const first = routerSheetChanges(opened, edited, condition(), "builder");
		expect(first).toEqual({ routing: { priority: 5 }, identity: { name: "Support desk" } });

		// The routing save succeeded; the name save failed.
		const baseline = withSavedRouting(opened, first.routing!);
		const reverted = { ...edited, priority: "3" };
		expect(routerSheetChanges(baseline, reverted, condition(), "builder")).toEqual({
			routing: { priority: 3 },
			identity: { name: "Support desk" },
		});
		// A retry without the reversal resends only the name.
		expect(routerSheetChanges(baseline, edited, condition(), "builder")).toEqual({ identity: { name: "Support desk" } });
	});

	it("takes a saved condition into the baseline as the gateway stores it: the query goes with the expression", () => {
		const opened = router({ cel_expression: 'model == "a"', query: EMPTY_QUERY });
		const saved = withSavedRouting(opened, { cel_expression: 'model == "b"', query: undefined });
		expect(saved.routing.cel_expression).toBe('model == "b"');
		expect(saved.routing.query).toBeUndefined();
		expect(saved.routing.priority).toBe(3);
		expect(opened.routing.cel_expression, "the opening router is not changed").toBe('model == "a"');
	});

	it("sends the routing and enabled together when the router is turned on with a new condition", () => {
		const saved = router();
		const changes = routerSheetChanges(saved, routerSheetDefaults(saved, true), condition('model == "support"'), "builder");
		expect(changes.routing).toEqual({ enabled: true, cel_expression: 'model == "support"', query: EMPTY_QUERY });
	});

	it("drops the builder query and keeps the expression for a CEL-mode condition", () => {
		const saved = router({ cel_expression: 'model == "a"' });
		const changes = routerSheetChanges(saved, routerSheetDefaults(saved), condition('model == "b"', "cel"), "cel");
		expect(changes.routing?.cel_expression).toBe('model == "b"');
		expect(changes.routing?.query).toBeUndefined();
	});

	it("counts a switch of editor mode as a change even when the expression is the same", () => {
		const saved = router({ cel_expression: 'model == "a"' });
		const changes = routerSheetChanges(saved, routerSheetDefaults(saved), condition('model == "a"', "cel"), "builder");
		expect(changes.routing).toBeDefined();
	});

	it("does not count whitespace around the expression as a change", () => {
		const saved = router({ cel_expression: 'model == "a"' });
		const changes = routerSheetChanges(saved, routerSheetDefaults(saved), condition('  model == "a" '), "builder");
		expect(changes).toEqual({});
	});

	it("sends the trimmed scope id for a scoped router, and none for a global one", () => {
		const saved = router();
		const scoped = routerSheetChanges(
			saved,
			{ ...routerSheetDefaults(saved), scope: "team", scope_id: " team-1 " },
			condition(),
			"builder",
		);
		expect(scoped.routing).toMatchObject({ scope: "team", scope_id: "team-1" });

		const teamRouter = router({ scope: "team", scope_id: "team-1" });
		const global = routerSheetChanges(teamRouter, { ...routerSheetDefaults(teamRouter), scope: "global" }, condition(), "builder");
		expect(global.routing).toMatchObject({ scope: "global", scope_id: undefined });
	});

	it("sends the trimmed name and description, and no routing, when only they changed", () => {
		const saved = router();
		const changes = routerSheetChanges(
			saved,
			{ ...routerSheetDefaults(saved), name: " Support desk ", description: "Tickets " },
			condition(),
			"builder",
		);
		expect(changes).toEqual({ identity: { name: "Support desk" } });
	});

	it("sends the description alone when only it changed", () => {
		const saved = router();
		const changes = routerSheetChanges(saved, { ...routerSheetDefaults(saved), description: "Escalations" }, condition(), "builder");
		expect(changes).toEqual({ identity: { description: "Escalations" } });
	});

	// The sheet keeps the router as it opened as its baseline. A refresh of the
	// router while it is open (turned on, re-prioritised, re-described
	// elsewhere) must not be sent back over by a rename.
	it("sends only the rename when the router refreshed while the sheet was open", () => {
		const opened = router();
		const refreshed = router({ enabled: true, priority: 9 }, { description: "Escalations" });
		const values = { ...routerSheetDefaults(opened), name: "Support desk" };
		const changes = routerSheetChanges(opened, values, condition(), "builder");
		expect(changes).toEqual({ identity: { name: "Support desk" } });
		// Compared with the refreshed router instead, the untouched fields would
		// read as edits and undo the refresh.
		const againstRefreshed = routerSheetChanges(refreshed, values, condition(), "builder");
		expect(againstRefreshed.routing).toMatchObject({ enabled: false, priority: 3 });
	});

	it("does not resend a name or description that only differs by surrounding spaces", () => {
		const saved = router();
		const changes = routerSheetChanges(saved, { ...routerSheetDefaults(saved), name: "Support " }, condition(), "builder");
		expect(changes).toEqual({});
	});
});