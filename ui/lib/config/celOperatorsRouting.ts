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
  { name: '=', label: 'equals', celSyntax: '==' },
  { name: '!=', label: 'does not equal', celSyntax: '!=' },
  { name: '>', label: 'greater than', celSyntax: '>' },
  { name: '<', label: 'less than', celSyntax: '<' },
  { name: '>=', label: 'greater than or equal', celSyntax: '>=' },
  { name: '<=', label: 'less than or equal', celSyntax: '<=' },

  // List operators
  { name: 'in', label: 'is in list', celSyntax: 'in' },
  { name: 'notIn', label: 'is not in list', celSyntax: '!in' },

  // String operators
  { name: 'contains', label: 'contains', celSyntax: 'contains' },
  { name: 'beginsWith', label: 'begins with', celSyntax: 'startsWith' },
  { name: 'endsWith', label: 'ends with', celSyntax: 'endsWith' },
  { name: 'matches', label: 'matches (regex)', celSyntax: 'matches' },

  // Existence operators
  { name: 'null', label: 'does not exist', celSyntax: '!has' },
  { name: 'notNull', label: 'exists', celSyntax: 'has' },
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
