import type { ModelRouter, ModelRouterRouting } from "@/lib/types/complexityRouter";
import { RoutingRuleScope } from "@/lib/types/routingRules";
import type { ModelRouterSheetFormSchema } from "@/lib/types/schemas";
import type { RuleGroupType } from "react-querybuilder";
import type { ConditionMode } from "./formParts";

// RouterCondition is the routing condition as the condition builder holds it.
export interface RouterCondition {
	mode: ConditionMode;
	celExpression: string;
	query: RuleGroupType;
}

// RouterSheetChanges is what a save sends: the routing fields on the routing
// endpoint, the name and description on the router's own. A part or field
// left out is unchanged and is not sent, so the gateway keeps what it has.
export interface RouterSheetChanges {
	routing?: Partial<ModelRouterRouting>;
	identity?: { name?: string; description?: string };
}

// routerSheetDefaults fills the router sheet from the saved router. turnOn
// opens it with the switch already on, for the "Turn on" button, so the save
// that follows turns the router on once its routing has been looked over.
export function routerSheetDefaults(router: ModelRouter, turnOn = false): ModelRouterSheetFormSchema {
	return {
		name: router.name,
		description: router.description ?? "",
		enabled: turnOn || router.routing.enabled,
		scope: router.routing.scope,
		scope_id: router.routing.scope_id ?? "",
		priority: String(router.routing.priority),
	};
}

// routerSheetChanges compares the sheet with its baseline: the router as it
// was when the sheet opened, with any routing this sheet has since saved
// (withSavedRouting), never a copy refreshed from elsewhere. Only what
// differs is sent, field group by field group, and the routing endpoint keeps
// every field a body leaves out, so a change saved elsewhere meanwhile is not
// overwritten: the scope with its entity, the condition (its expression and
// the builder query it came from, which the gateway stores together), the
// priority, and `enabled` when the switch moved. The condition counts as
// changed when its expression or its editor mode did: the builder's query is
// only how the expression was built. The name and description are each sent
// only when edited.
export function routerSheetChanges(
	baseline: ModelRouter,
	values: ModelRouterSheetFormSchema,
	condition: RouterCondition,
	baselineMode: ConditionMode,
): RouterSheetChanges {
	const saved = baseline.routing;
	const isGlobal = values.scope === RoutingRuleScope.Global;
	const scopeId = isGlobal ? "" : values.scope_id.trim();
	const priority = Number(values.priority);
	const celExpression = condition.celExpression.trim();

	const routing: Partial<ModelRouterRouting> = {};
	if (values.scope !== saved.scope || scopeId !== (isGlobal ? "" : (saved.scope_id ?? ""))) {
		routing.scope = values.scope as ModelRouterRouting["scope"];
		routing.scope_id = isGlobal ? undefined : scopeId;
	}
	if (celExpression !== (saved.cel_expression ?? "").trim() || condition.mode !== baselineMode) {
		routing.cel_expression = celExpression;
		routing.query = condition.mode === "builder" ? condition.query : undefined;
	}
	if (priority !== saved.priority) routing.priority = priority;
	if (values.enabled !== saved.enabled) routing.enabled = values.enabled;

	const changes: RouterSheetChanges = {};
	if (Object.keys(routing).length > 0) changes.routing = routing;
	const identity: NonNullable<RouterSheetChanges["identity"]> = {};
	const name = values.name.trim();
	const description = values.description.trim();
	if (name !== baseline.name) identity.name = name;
	if (description !== (baseline.description ?? "")) identity.description = description;
	if (identity.name !== undefined || identity.description !== undefined) changes.identity = identity;
	return changes;
}

// withSavedRouting is baseline with a routing update the gateway accepted
// applied as the gateway applies it: the fields sent replace their saved
// values, and a condition sent replaces the stored query too, even when it
// sends none. baseline is not modified.
export function withSavedRouting(baseline: ModelRouter, sent: Partial<ModelRouterRouting>): ModelRouter {
	const routing: ModelRouterRouting = { ...baseline.routing };
	if (sent.scope !== undefined) {
		routing.scope = sent.scope;
		routing.scope_id = sent.scope_id;
	}
	if (sent.cel_expression !== undefined) {
		routing.cel_expression = sent.cel_expression;
		routing.query = sent.query;
	}
	if (sent.priority !== undefined) routing.priority = sent.priority;
	if (sent.enabled !== undefined) routing.enabled = sent.enabled;
	return { ...baseline, routing };
}

// hasRouterSheetChanges reports whether a save would send anything.
export function hasRouterSheetChanges(changes: RouterSheetChanges): boolean {
	return changes.routing !== undefined || changes.identity !== undefined;
}