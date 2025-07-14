// Routing rule types

export type ConditionOperator = 
  | "equals"
  | "not_equals"
  | "contains"
  | "not_contains"
  | "starts_with"
  | "ends_with"
  | "exists"
  | "not_exists"
  | "regex_match"

export type ConditionType = 
  | "header"
  | "path"
  | "method"
  | "query_param"
  | "body_contains"

export interface RoutingCondition {
  id: string
  type: ConditionType
  field: string // header name, query param name, etc.
  operator: ConditionOperator
  value: string
}

export type LogicalOperator = "AND" | "OR"

export interface ConditionGroup {
  id: string
  operator: LogicalOperator
  conditions: RoutingCondition[]
}

export type ActionType = "route_to_provider" | "route_to_model" | "set_header" | "reject"

export interface RoutingAction {
  id: string
  type: ActionType
  provider?: string
  model?: string
  headers?: Record<string, string>
  priority?: number
}

export interface RoutingRule {
  id: string
  name: string
  description?: string
  enabled: boolean
  priority: number
  condition_groups: ConditionGroup[]
  group_operator: LogicalOperator // How to combine condition groups
  actions: RoutingAction[]
  created_at: string
  updated_at: string
}

export interface CreateRoutingRuleRequest {
  name: string
  description?: string
  enabled: boolean
  priority: number
  condition_groups: ConditionGroup[]
  group_operator: LogicalOperator
  actions: RoutingAction[]
}

export interface UpdateRoutingRuleRequest extends CreateRoutingRuleRequest {
  id: string
}

export interface ListRoutingRulesResponse {
  rules: RoutingRule[]
  total: number
} 