/**
 * CEL Operators Configuration for Routing Rules
 * Maps UI operators to CEL syntax
 */

export interface CELOperatorDefinition {
	name: string;
	label: string;
	celSyntax: string;
}

export const celOperatorsRouting: CELOperatorDefinition[] = [
	// Comparison operators
	{ name: "=", label: "equals", celSyntax: "==" },
	{ name: "!=", label: "不等于", celSyntax: "!=" },
	{ name: ">", label: "大于", celSyntax: ">" },
	{ name: "<", label: "小于", celSyntax: "<" },
	{ name: ">=", label: "大于等于", celSyntax: ">=" },
	{ name: "<=", label: "小于等于", celSyntax: "<=" },

	// List operators
	{ name: "in", label: "在列表中", celSyntax: "in" },
	{ name: "notIn", label: "不在列表中", celSyntax: "!in" },

	// String operators
	{ name: "contains", label: "contains", celSyntax: "contains" },
	{ name: "beginsWith", label: "以...开头", celSyntax: "startsWith" },
	{ name: "endsWith", label: "以...结尾", celSyntax: "endsWith" },
	{ name: "matches", label: "匹配（正则）", celSyntax: "matches" },

	// Existence operators
	{ name: "null", label: "不存在", celSyntax: "!has" },
	{ name: "notNull", label: "exists", celSyntax: "has" },
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