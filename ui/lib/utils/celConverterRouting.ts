/**
 * CEL Converter for Routing Rules
 * Converts react-querybuilder rules to CEL expressions
 */

import { RuleGroupType, RuleType } from 'react-querybuilder';
import { getOperatorCELSyntax } from '@/lib/config/celOperatorsRouting';

/**
 * RE2-incompatible constructs (not supported by CEL/RE2).
 * Used for syntactic checks so patterns validated here work in CEL regex functions.
 */
const RE2_UNSUPPORTED = {
  lookbehindPositive: '(?<=',
  lookbehindNegative: '(?<!',
  lookaheadPositive: '(?=',
  lookaheadNegative: '(?!',
} as const;

/** Matches numeric backreferences (\1, \2, ... \9, \10, etc.) */
const RE2_BACKREF = /\\[0-9]+/;

/**
 * Validate regex pattern - checks that it is valid and RE2-compatible (for CEL).
 * RE2 does not support lookarounds or backreferences. Returns null if valid,
 * error message if invalid or RE2-incompatible.
 */
export function validateRegexPattern(pattern: string): string | null {
  if (!pattern || typeof pattern !== 'string') {
    return null; // Empty patterns are valid
  }

  // Reject RE2-unsupported constructs
  if (pattern.includes(RE2_UNSUPPORTED.lookbehindPositive)) {
    return 'RE2 incompatible: positive lookbehind (?<=...) is not supported';
  }
  if (pattern.includes(RE2_UNSUPPORTED.lookbehindNegative)) {
    return 'RE2 incompatible: negative lookbehind (?<!...) is not supported';
  }
  if (pattern.includes(RE2_UNSUPPORTED.lookaheadPositive)) {
    return 'RE2 incompatible: positive lookahead (?=...) is not supported';
  }
  if (pattern.includes(RE2_UNSUPPORTED.lookaheadNegative)) {
    return 'RE2 incompatible: negative lookahead (?!...) is not supported';
  }
  if (RE2_BACKREF.test(pattern)) {
    return 'RE2 incompatible: numeric backreferences (e.g. \\1, \\2) are not supported';
  }

  // Basic syntax check via JS RegExp (catches invalid escaping, etc.)
  try {
    new RegExp(pattern);
    return null; // Valid regex
  } catch (error: unknown) {
    const errorMessage = error instanceof Error ? error.message : 'Invalid regex pattern';
    return `Invalid regex: ${errorMessage}`;
  }
}

/**
 * Parse keyValue pair from string format "key:value"
 */
function parseKeyValue(value: string): { key: string; value: string } | null {
  if (!value || typeof value !== 'string') {
    return null;
  }

  // Try parsing as JSON array first (for comma-separated values)
  if (value.startsWith('[')) {
    return null; // This is an array, not a key-value pair
  }

  // Handle "key" format for existence checks
  const colonIndex = value.indexOf(':');
  if (colonIndex > 0) {
    return {
      key: value.substring(0, colonIndex).trim(),
      value: value.substring(colonIndex + 1).trim(),
    };
  }

  // If no colon, treat entire string as key (for existence checks)
  return {
    key: value.trim(),
    value: '',
  };
}

/**
 * Escape special characters in strings
 */
function escapeString(value: string): string {
  return value.replace(/\\/g, '\\\\').replace(/"/g, '\\"').replace(/\n/g, '\\n').replace(/\r/g, '\\r');
}

/**
 * Format value based on operator type
 */
function formatValue(value: any, operator: string): string {
  // Handle array values for 'in' and 'notIn' operators
  if (operator === 'in' || operator === 'notIn') {
    let arrayValue: string[];

    if (typeof value === 'string') {
      try {
        // Try parsing as JSON array
        arrayValue = JSON.parse(value);
        if (!Array.isArray(arrayValue)) {
          arrayValue = [String(value)];
        }
      } catch {
        // Split by comma if not JSON
        arrayValue = value
          .split(',')
          .map((v) => v.trim())
          .filter((v) => v);
      }
    } else if (Array.isArray(value)) {
      arrayValue = value;
    } else {
      arrayValue = [String(value)];
    }

    const formattedValues = arrayValue.map((v) => `"${escapeString(String(v))}"`);
    return `[${formattedValues.join(', ')}]`;
  }

  // Handle numbers
  if (typeof value === 'number') {
    return String(value);
  }

  // Handle booleans
  if (typeof value === 'boolean') {
    return value ? 'true' : 'false';
  }

  // Handle string values (regex patterns for matches operator)
  if (operator === 'matches') {
    // For regex, wrap in forward slashes (CEL format) or quotes
    return `"${escapeString(String(value))}"`;
  }

  // Default: treat as string
  return `"${escapeString(String(value))}"`;
}

/**
 * Convert a single rule to CEL expression
 */
function convertRuleToCEL(rule: RuleType): string {
  const { field, operator, value } = rule;

  if (!field || !operator) {
    return '';
  }

  const celOperator = getOperatorCELSyntax(operator);

  // Handle existence checks (null/notNull)
  if (operator === 'null') {
    // For keyValue fields, check if key exists in map
    const keyValuePair = parseKeyValue(String(value));
    if (keyValuePair && keyValuePair.key) {
      return `!(${formatValue(keyValuePair.key, 'text')} in ${field})`;
    }
    return `!has(${field})`;
  }

  if (operator === 'notNull') {
    // For keyValue fields, check if key exists in map
    const keyValuePair = parseKeyValue(String(value));
    if (keyValuePair && keyValuePair.key) {
      return `${formatValue(keyValuePair.key, 'text')} in ${field}`;
    }
    return `has(${field})`;
  }

  // Handle string method operators (startsWith, endsWith, contains, matches)
  const stringMethods = ['startsWith', 'endsWith', 'contains', 'matches'];
  if (stringMethods.includes(celOperator)) {
    const formattedValue = formatValue(value, operator);

    // Handle keyValue fields (headers, params)
    const isKeyValueField = field === 'headers' || field === 'params';
    if (isKeyValueField) {
      const keyValuePair = parseKeyValue(String(value));
      if (keyValuePair && keyValuePair.key && keyValuePair.value) {
        const fieldPath = `${field}[${formatValue(keyValuePair.key, 'text')}]`;
        const actualValue = formatValue(keyValuePair.value, operator);
        return `${fieldPath}.${celOperator}(${actualValue})`;
      }
    }

    // Regular field handling
    return `${field}.${celOperator}(${formattedValue})`;
  }

  // Handle tokens_used, request, and budget_used
  // Structure: tokens_used > 80.0 or request >= 75.0 or budget_used > 50.0
  // These are simple numeric comparisons against percent_used values from GetBudgetAndRateLimitStatus
  // which already returns the max of model+provider, model-only, and provider-only configs
  const isRateLimitOrBudgetField = field === 'tokens_used' || field === 'request' || field === 'budget_used';
  if (isRateLimitOrBudgetField) {
    const thresholdValue = String(value).trim();
    if (thresholdValue) {
      // Convert to double to match CEL variable type (tokens_used, request, budget_used are all doubles)
      const numValue = parseFloat(thresholdValue);
      let actualValue: string;
      if (!isNaN(numValue)) {
        // Format as double with decimal point
        actualValue = Number.isInteger(numValue) ? `${numValue}.0` : numValue.toString();
      } else {
        actualValue = thresholdValue;
      }
      return `${field} ${celOperator} ${actualValue}`;
    }
  }

  // Handle other keyValue fields (headers, params) for other operators
  const isKeyValueField = field === 'headers' || field === 'params';
  if (isKeyValueField) {
    const keyValuePair = parseKeyValue(String(value));
    if (keyValuePair && keyValuePair.key && keyValuePair.value) {
      const fieldPath = `${field}[${formatValue(keyValuePair.key, 'text')}]`;
      const actualValue = formatValue(keyValuePair.value, operator);

      // For 'notIn' operator, wrap with negation since CEL has no "not in" infix operator
      if (operator === 'notIn') {
        return `!(${fieldPath} in ${actualValue})`;
      }

      // For 'in' operator and others, use standard binary syntax
      return `${fieldPath} ${celOperator} ${actualValue}`;
    }
  }

  // Regular field handling for binary operators
  const formattedValue = formatValue(value, operator);

  // For 'notIn' operator, wrap with negation since CEL has no "not in" infix operator
  if (operator === 'notIn') {
    return `!(${field} in ${formattedValue})`;
  }

  return `${field} ${celOperator} ${formattedValue}`;
}

/**
 * Convert rule group (possibly nested) to CEL expression
 */
export function convertRuleGroupToCEL(ruleGroup: RuleGroupType | undefined): string {
  if (!ruleGroup || !ruleGroup.rules || ruleGroup.rules.length === 0) {
    return '';
  }

  const combinator = ruleGroup.combinator === 'or' ? '||' : '&&';
  const expressions: string[] = [];

  for (const rule of ruleGroup.rules) {
    if ('rules' in rule) {
      // It's a nested group
      const nestedExpression = convertRuleGroupToCEL(rule as RuleGroupType);
      if (nestedExpression) {
        expressions.push(`(${nestedExpression})`);
      }
    } else {
      // It's a rule
      const ruleExpression = convertRuleToCEL(rule as RuleType);
      if (ruleExpression) {
        expressions.push(ruleExpression);
      }
    }
  }

  if (expressions.length === 0) {
    return '';
  }

  if (expressions.length === 1) {
    return expressions[0];
  }

  return expressions.join(` ${combinator} `);
}

/**
 * Validate routing rules for regex pattern errors
 * Returns array of error messages, empty if valid
 */
export function validateRoutingRules(ruleGroup: RuleGroupType | undefined): string[] {
  const errors: string[] = [];

  if (!ruleGroup || !ruleGroup.rules) {
    return errors;
  }

  const validateRule = (rule: RuleType | RuleGroupType) => {
    if ('rules' in rule) {
      // Nested group - recursively validate
      for (const nestedRule of rule.rules) {
        validateRule(nestedRule);
      }
    } else {
      // Regular rule - check if it uses matches operator
      if (rule.operator === 'matches' && rule.value) {
        const regexError = validateRegexPattern(String(rule.value));
        if (regexError) {
          errors.push(`Field "${rule.field}": ${regexError}`);
        }
      }
    }
  };

  for (const rule of ruleGroup.rules) {
    validateRule(rule);
  }

  return errors;
}


/**
 * Validate that rules using rate limits or budgets have a model or provider condition
 * Returns array of error messages, empty if valid
 */
export function validateRateLimitAndBudgetRules(ruleGroup: RuleGroupType | undefined): string[] {
  const errors: string[] = [];

  if (!ruleGroup || !ruleGroup.rules) {
    return errors;
  }

  // Check if rule uses rate limits or budgets
  const hasRateLimitOrBudget = (rule: RuleType | RuleGroupType): boolean => {
    if ('rules' in rule) {
      // Nested group
      return rule.rules.some(r => hasRateLimitOrBudget(r));
    }
    // Regular rule - check if field is rate limit or budget
    return (rule as RuleType).field === 'tokens_used' || (rule as RuleType).field === 'request' || (rule as RuleType).field === 'budget_used';
  };

  // Check if rule has model or provider condition
  const hasModelOrProviderCondition = (rule: RuleType | RuleGroupType): boolean => {
    if ('rules' in rule) {
      // Nested group - check all nested rules
      return rule.rules.some(r => hasModelOrProviderCondition(r));
    }
    // Regular rule - check if field is model or provider
    return (rule as RuleType).field === 'model' || (rule as RuleType).field === 'provider';
  };

  const ruleHasRateLimitOrBudget = ruleGroup.rules.some(r => hasRateLimitOrBudget(r));

  if (ruleHasRateLimitOrBudget) {
    const hasCondition = hasModelOrProviderCondition(ruleGroup);
    if (!hasCondition) {
      errors.push('Rules using rate limits or budget must have a "model" or "provider" condition');
    }
  }

  return errors;
}
