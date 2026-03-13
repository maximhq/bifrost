/**
 * Jinja2 variable utilities for prompt messages.
 *
 * Extracts `{{ variable_name }}` patterns from message content and provides
 * substitution at execution time. Supports basic Jinja2 variable syntax:
 *   {{ name }}
 *   {{ user_name }}
 *   {{ some.nested }}  (treated as a flat key "some.nested")
 *
 * Filters ({{ x | upper }}) and expressions are NOT evaluated — only
 * simple variable references are extracted and replaced.
 */

/** Matches {{ variable_name }} with optional whitespace inside braces */
export const JINJA_VAR_REGEX = /\{\{\s*([a-zA-Z_][a-zA-Z0-9_.]*)\s*\}\}/g


/**
 * Highlight patterns for Jinja2 variables in rich textareas
 */
export const JINJA_VAR_HIGHLIGHT_PATTERNS = [
	{
		pattern: /{{.*?}}/g,
		className: "outline-content-brand-light text-sm cursor-pointer bg-green-500/20",
		validate: (part: string) => {
			return (
				part.startsWith?.("{{") &&
				part.endsWith?.("}}") &&
				!part.slice(2, -2).includes("{") &&
				!part.slice(2, -2).includes("}") &&
				!part.slice(2, -2).includes("'") &&
				!part.slice(2, -2).includes('"')
			);
		},
		enableVariableClickEdit: true,
	},
];