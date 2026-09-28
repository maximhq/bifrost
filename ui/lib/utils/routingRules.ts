/**
 * Routing Rules Utility Functions
 * Helper functions for CEL validation, formatting, and rule management
 */

import { RoutingFallbackFormData, RoutingFallbackWire } from "@/lib/types/routingRules";

/**
 * Validates if a CEL expression has basic correct syntax
 * @param expression - The CEL expression to validate
 * @returns true if expression appears syntactically valid
 */
export function isValidCELExpression(expression: string): boolean {
	if (!expression) {
		return true;
	}

	const trimmed = expression.trim();
	if (trimmed.length === 0 || trimmed === "true" || trimmed === "false") {
		return true;
	}

	// Check for basic syntax issues
	if (trimmed.includes(";;")) {
		return false;
	}

	// Check for matching brackets/parentheses
	const openBrackets = (trimmed.match(/[[{]/g) || []).length;
	const closeBrackets = (trimmed.match(/[\]}]/g) || []).length;
	const openParens = (trimmed.match(/\(/g) || []).length;
	const closeParens = (trimmed.match(/\)/g) || []).length;

	if (openBrackets !== closeBrackets || openParens !== closeParens) {
		return false;
	}

	return true;
}

/**
 * Formats a fallback string (provider/model) for display
 * @param fallback - The fallback string (e.g., "openai/gpt-4o")
 * @returns Formatted fallback string
 */
export function formatFallback(fallback: string): string {
	if (!fallback) return "";
	const parts = fallback.split("/");
	return parts.length === 2 ? `${parts[0].toUpperCase()} - ${parts[1]}` : fallback;
}

/**
 * Normalizes a wire fallback into the provider and model the sheet's two selects need. Unpinned
 * fallbacks arrive as the bare "provider/model" string.
 */
export function normalizeFallback(fallback: RoutingFallbackWire): RoutingFallbackFormData {
	if (typeof fallback !== "string") {
		return {
			provider: fallback.provider ?? "",
			model: fallback.model ?? "",
			key_id: fallback.key_id ?? "",
		};
	}
	const separator = fallback.indexOf("/");
	if (separator === -1) {
		return { provider: fallback, model: "", key_id: "" };
	}
	return { provider: fallback.slice(0, separator), model: fallback.slice(separator + 1), key_id: "" };
}

/**
 * Renders a fallback back onto the wire, as the bare string unless it pins a key. Sending the object
 * form for an unpinned fallback would change the rule's config hash on every save. The trailing
 * slash is required when the model is empty: a bare "anthropic" parses to an empty provider.
 */
export function denormalizeFallback(fallback: Partial<RoutingFallbackFormData>): RoutingFallbackWire {
	const provider = (fallback.provider ?? "").trim();
	const model = (fallback.model ?? "").trim();
	const keyId = (fallback.key_id ?? "").trim();
	if (keyId) {
		return model ? { provider, model, key_id: keyId } : { provider, key_id: keyId };
	}
	return `${provider}/${model}`;
}

/**
 * Truncates CEL expression for table display
 * @param expression - The CEL expression
 * @param maxLength - Maximum length (default 60)
 * @returns Truncated expression with ellipsis if needed
 */
export function truncateCELExpression(expression: string, maxLength: number = 60): string {
	if (!expression) return "";
	if (expression.length <= maxLength) return expression;
	return expression.substring(0, maxLength) + "...";
}

/**
 * Validates a provider/model combination
 * @param provider - The provider name
 * @param model - The model name (optional)
 * @returns Error message if invalid, empty string if valid
 */
export function validateProviderModel(provider: string, _model?: string): string {
	if (!provider || provider.trim().length === 0) {
		return "Provider is required";
	}
	return "";
}

/**
 * Generates a CSS class for priority badge color
 * @returns CSS class name for styling
 */
export function getPriorityBadgeClass(): string {
	return "bg-primary text-primary-foreground";
}

/**
 * Gets a user-friendly CEL operator from the expression
 * @param expression - The CEL expression
 * @returns Array of detected operators
 */
export function detectCELOperators(expression: string): string[] {
	const operators: string[] = [];
	if (!expression) return operators;

	// Common CEL operators
	const operatorPatterns = [
		{ regex: /==/, label: "Equals" },
		{ regex: /!=/, label: "Not equals" },
		{ regex: />=/, label: "Greater than or equal" },
		{ regex: /<=/, label: "Less than or equal" },
		{ regex: />/, label: "Greater than" },
		{ regex: /</, label: "Less than" },
		{ regex: /&&/, label: "AND" },
		{ regex: /\|\|/, label: "OR" },
		{ regex: /!(?!=)/, label: "NOT" },
		{ regex: /in\s/, label: "IN" },
		{ regex: /.matches\(/, label: "Regex" },
		{ regex: /.startsWith\(/, label: "StartsWith" },
		{ regex: /.contains\(/, label: "Contains" },
		{ regex: /.endsWith\(/, label: "EndsWith" },
	];

	operatorPatterns.forEach(({ regex, label }) => {
		if (regex.test(expression) && !operators.includes(label)) {
			operators.push(label);
		}
	});

	return operators;
}