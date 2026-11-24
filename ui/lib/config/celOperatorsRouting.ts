import i18n from "@/lib/i18n";
import type { ParseKeys } from "i18next";

/**
 * CEL Operators Configuration for Routing Rules
 * Maps UI operators to CEL syntax
 */

export interface CELOperatorDefinition {
	name: string;
	label: string;
	celSyntax: string;
}

// Labels resolve through i18n when read (getters) so the shared list follows the active locale.
const operator = (name: string, key: ParseKeys<"models">, celSyntax: string): CELOperatorDefinition => ({
	name,
	get label(): string {
		return i18n.t(key, { ns: "models" });
	},
	celSyntax,
});

export const celOperatorsRouting: CELOperatorDefinition[] = [
	// Comparison operators
	operator("=", "routing.cel.operators.equals", "=="),
	operator("!=", "routing.cel.operators.notEquals", "!="),
	operator(">", "routing.cel.operators.greaterThan", ">"),
	operator("<", "routing.cel.operators.lessThan", "<"),
	operator(">=", "routing.cel.operators.greaterThanOrEqual", ">="),
	operator("<=", "routing.cel.operators.lessThanOrEqual", "<="),

	// List operators
	operator("in", "routing.cel.operators.in", "in"),
	operator("notIn", "routing.cel.operators.notIn", "!in"),

	// String operators
	operator("contains", "routing.cel.operators.contains", "contains"),
	operator("beginsWith", "routing.cel.operators.beginsWith", "startsWith"),
	operator("endsWith", "routing.cel.operators.endsWith", "endsWith"),
	operator("matches", "routing.cel.operators.matches", "matches"),

	// Existence operators
	operator("null", "routing.cel.operators.null", "!has"),
	operator("notNull", "routing.cel.operators.notNull", "has"),
];

/**
 * Get CEL syntax for a given operator name
 */
export function getOperatorCELSyntax(operatorName: string): string {
	const operator = celOperatorsRouting.find((op) => op.name === operatorName);
	return operator ? operator.celSyntax : operatorName;
}

/**
 * Get operator label for display
 */
export function getOperatorLabel(operatorName: string): string {
	const operator = celOperatorsRouting.find((op) => op.name === operatorName);
	return operator ? operator.label : operatorName;
}