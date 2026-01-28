/**
 * Routing Rules Type Definitions
 * Defines all TypeScript interfaces for routing rules feature
 */

import { RuleGroupType } from "react-querybuilder";

export interface RoutingRule {
  id: string
  name: string
  description: string
  cel_expression: string
  provider: string
  model?: string
  fallbacks?: string[]
  scope: "global" | "team" | "customer" | "virtual_key"
  scope_id?: string
  priority: number
  enabled: boolean
  query?: RuleGroupType
  created_at: string
  updated_at: string
}

export interface CreateRoutingRuleRequest {
  name: string
  description?: string
  cel_expression: string
  provider: string
  model?: string
  fallbacks?: string[]
  scope: string
  scope_id?: string
  priority: number
  enabled?: boolean
  query?: RuleGroupType
}

/** Partial update: only sent fields are applied; allows clearing fields by sending "" or []. */
export type UpdateRoutingRuleRequest = Partial<CreateRoutingRuleRequest>;

export interface GetRoutingRulesResponse {
  rules: RoutingRule[]
  count: number
}

export interface GetRoutingRuleResponse {
  rule: RoutingRule
}

export interface RoutingRuleFormData {
  id?: string
  name: string
  description: string
  cel_expression: string
  provider: string
  model: string
  fallbacks: string[]
  scope: string
  scope_id: string
  priority: number
  enabled: boolean
  query?: RuleGroupType
  isDirty?: boolean
}

export enum RoutingRuleScope {
  Global = "global",
  Team = "team",
  Customer = "customer",
  VirtualKey = "virtual_key",
}

export const ROUTING_RULE_SCOPES = [
  { value: RoutingRuleScope.Global, label: "Global" },
  { value: RoutingRuleScope.Team, label: "Team" },
  { value: RoutingRuleScope.Customer, label: "Customer" },
  { value: RoutingRuleScope.VirtualKey, label: "Virtual Key" },
]

export const DEFAULT_ROUTING_RULE_FORM_DATA: RoutingRuleFormData = {
  name: "",
  description: "",
  cel_expression: "",
  provider: "",
  model: "",
  fallbacks: [],
  scope: RoutingRuleScope.Global,
  scope_id: "",
  priority: 0,
  enabled: true,
  isDirty: false,
}
