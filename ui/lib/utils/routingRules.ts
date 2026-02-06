/**
 * Routing Rules Utility Functions
 * Helper functions for CEL validation, formatting, and rule management
 */

/**
 * Validates if a CEL expression has basic correct syntax
 * @param expression - The CEL expression to validate
 * @returns true if expression appears syntactically valid
 */
export function isValidCELExpression(expression: string): boolean {
	if (!expression) {
		return true
	}
	
	const trimmed = expression.trim()
	if (trimmed.length === 0 || trimmed === "true" || trimmed === "false") {
		return true
	}

	// Check for basic syntax issues
	if (trimmed.includes(";;")) {
		return false
	}

	// Check for matching brackets/parentheses
	const openBrackets = (trimmed.match(/[\[\{]/g) || []).length
	const closeBrackets = (trimmed.match(/[\]\}]/g) || []).length
	const openParens = (trimmed.match(/\(/g) || []).length
	const closeParens = (trimmed.match(/\)/g) || []).length

	if (openBrackets !== closeBrackets || openParens !== closeParens) {
		return false
	}

	return true
}

/**
 * Formats a fallback string (provider/model) for display
 * @param fallback - The fallback string (e.g., "openai/gpt-4o")
 * @returns Formatted fallback string
 */
export function formatFallback(fallback: string): string {
	if (!fallback) return ""
	const parts = fallback.split("/")
	return parts.length === 2 ? `${parts[0].toUpperCase()} - ${parts[1]}` : fallback
}

/**
 * Parses a fallback string into provider and model
 * @param fallback - The fallback string (e.g., "openai/gpt-4o")
 * @returns Object with provider and model, or null if invalid
 */
export function parseFallback(fallback: string): { provider: string; model: string } | null {
	if (!fallback) return null
	const parts = fallback.split("/")
	if (parts.length !== 2) return null
	return { provider: parts[0], model: parts[1] }
}

/**
 * Converts fallback array to string format for display/editing
 * @param fallbacks - Array of fallback strings
 * @returns Comma-separated string
 */
export function fallbacksToString(fallbacks?: string[]): string {
	if (!fallbacks || fallbacks.length === 0) return ""
	return fallbacks.join(", ")
}

/**
 * Converts comma-separated string to fallback array
 * @param str - Comma-separated fallback string
 * @returns Array of fallback strings
 */
export function stringToFallbacks(str: string): string[] {
	if (!str || str.trim().length === 0) return []
	return str
		.split(",")
		.map((s) => s.trim())
		.filter((s) => s.length > 0)
}

/**
 * Gets a friendly display name for a scope
 * @param scope - The scope value (global|team|customer|virtual_key)
 * @returns Friendly display name
 */
export function getScopeLabel(scope: string): string {
	const labels: Record<string, string> = {
		global: "Global",
		team: "Team",
		customer: "Customer",
		virtual_key: "Virtual Key",
	}
	return labels[scope] || scope
}

/**
 * Truncates CEL expression for table display
 * @param expression - The CEL expression
 * @param maxLength - Maximum length (default 60)
 * @returns Truncated expression with ellipsis if needed
 */
export function truncateCELExpression(expression: string, maxLength: number = 60): string {
	if (!expression) return ""
	if (expression.length <= maxLength) return expression
	return expression.substring(0, maxLength) + "..."
}

/**
 * Validates a provider/model combination
 * @param provider - The provider name
 * @param model - The model name (optional)
 * @returns Error message if invalid, empty string if valid
 */
export function validateProviderModel(provider: string, model?: string): string {
	if (!provider || provider.trim().length === 0) {
		return "Provider is required"
	}
	return ""
}

/**
 * Generates a CSS class for priority badge color
 * @param priority - The priority value
 * @returns CSS class name for styling
 */
export function getPriorityBadgeClass(priority: number): string {
	return "bg-primary text-primary-foreground"
}

/**
 * Gets a user-friendly CEL operator from the expression
 * @param expression - The CEL expression
 * @returns Array of detected operators
 */
export function detectCELOperators(expression: string): string[] {
	const operators: string[] = []
	if (!expression) return operators

	// Common CEL operators
	const operatorPatterns = [
		{ regex: /==/, label: "Equals" },
		{ regex: /!=/, label: "Not equals" },
		{ regex: />=/,label: "Greater than or equal" },
		{ regex: /<=/,label: "Less than or equal" },
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
	]

	operatorPatterns.forEach(({ regex, label }) => {
		if (regex.test(expression) && !operators.includes(label)) {
			operators.push(label)
		}
	})

	return operators
}
