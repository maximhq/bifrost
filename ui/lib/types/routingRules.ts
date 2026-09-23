/**
 * Routing Rules Type Definitions
 * Defines all TypeScript interfaces for routing rules feature
 */

import { RuleGroupType } from "react-querybuilder";

/** A fallback that may pin a provider key. The API accepts and returns the legacy "provider/model" string for unpinned entries. */
/** Wire object form of a fallback, used only when it pins a provider key. */
export interface RoutingFallbackObject {
	provider?: string;
	model?: string;
	key_id?: string;
}

export type RoutingFallbackWire = string | RoutingFallbackObject;

/** Form state, split so the sheet can drive separate provider and model selects. */
export interface RoutingFallbackFormData {
	provider: string;
	model: string;
	key_id: string;
}

export interface RoutingTarget {
	provider?: string;
	model?: string;
	key_id?: string;
	weight: number;
}

export interface RoutingRule {
	id: string;
	name: string;
	description: string;
	cel_expression: string;
	targets: RoutingTarget[];
	fallbacks?: RoutingFallbackWire[];
	scope: "global" | "team" | "customer" | "virtual_key" | "user";
	scope_id?: string;
	priority: number;
	enabled: boolean;
	chain_rule: boolean;
	query?: RuleGroupType;
	created_at: string;
	updated_at: string;
}

export interface CreateRoutingRuleRequest {
	name: string;
	description?: string;
	cel_expression?: string;
	targets: RoutingTarget[];
	fallbacks?: RoutingFallbackWire[];
	scope: string;
	scope_id?: string;
	priority: number;
	enabled?: boolean;
	chain_rule?: boolean;
	query?: RuleGroupType;
}

/** Partial update: only sent fields are applied; allows clearing fields by sending "" or []. */
export type UpdateRoutingRuleRequest = Partial<CreateRoutingRuleRequest>;

export interface GetRoutingRulesParams {
	limit?: number;
	offset?: number;
	search?: string;
}

export interface GetRoutingRulesResponse {
	rules: RoutingRule[];
	count: number;
	total_count: number;
	limit: number;
	offset: number;
}

export interface GetRoutingRuleResponse {
	rule: RoutingRule;
}

export interface RoutingTargetFormData {
	provider: string;
	model: string;
	key_id: string;
	weight: number;
}

export interface RoutingRuleFormData {
	id?: string;
	name: string;
	description: string;
	cel_expression: string;
	targets: RoutingTargetFormData[];
	fallbacks: RoutingFallbackFormData[];
	scope: string;
	scope_id: string;
	priority: number;
	enabled: boolean;
	chain_rule: boolean;
	query?: RuleGroupType;
	isDirty?: boolean;
}

export enum RoutingRuleScope {
	Global = "global",
	Team = "team",
	Customer = "customer",
	VirtualKey = "virtual_key",
	// Not part of ROUTING_RULE_SCOPES: the sheet offers it only when a user
	// picker is registered (builds with a user directory).
	User = "user",
}

export const ROUTING_RULE_SCOPES = [
	{ value: RoutingRuleScope.Global, label: "Global" },
	{ value: RoutingRuleScope.Team, label: "Team" },
	{ value: RoutingRuleScope.Customer, label: "Customer" },
	{ value: RoutingRuleScope.VirtualKey, label: "Virtual Key" },
];

export const DEFAULT_ROUTING_FALLBACK: RoutingFallbackFormData = {
	provider: "",
	model: "",
	key_id: "",
};

export const DEFAULT_ROUTING_TARGET: RoutingTargetFormData = {
	provider: "",
	model: "",
	key_id: "",
	weight: 1,
};

export const DEFAULT_ROUTING_RULE_FORM_DATA: RoutingRuleFormData = {
	name: "",
	description: "",
	cel_expression: "",
	targets: [DEFAULT_ROUTING_TARGET],
	fallbacks: [],
	scope: RoutingRuleScope.Global,
	scope_id: "",
	priority: 0,
	enabled: true,
	chain_rule: false,
	isDirty: false,
};