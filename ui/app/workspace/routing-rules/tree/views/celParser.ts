/**
 * CEL (Common Expression Language) parsing and evaluation helpers.
 *
 * Only handles the subset of CEL actually used in routing rule conditions:
 * equality/inequality, startsWith, contains, in-list, header access, and
 * simple numeric comparisons.
 */

/**
 * Evaluate a single normalised CEL clause against a resolved variable map.
 * Only handles simple equality/inequality patterns (field == "v", "v" == field,
 * field != "v", "v" != field). Returns null when too complex to evaluate.
 */
export function evalChainCondition(cond: string, vars: Record<string, string>): boolean | null {
	const s = cond.trim();
	let m: RegExpMatchArray | null;

	// Simple equality: field == "v" or "v" == field
	m = s.match(/^(\w+)\s*==\s*["']([^"']*)["']$/);
	if (m && m[1] in vars) return vars[m[1]] === m[2];
	m = s.match(/^["']([^"']*)['"]\s*==\s*(\w+)$/);
	if (m && m[2] in vars) return vars[m[2]] === m[1];

	// Inequality: field != "v" or "v" != field
	m = s.match(/^(\w+)\s*!=\s*["']([^"']*)["']$/);
	if (m && m[1] in vars) return vars[m[1]] !== m[2];
	m = s.match(/^["']([^"']*)['"]\s*!=\s*(\w+)$/);
	if (m && m[2] in vars) return vars[m[2]] !== m[1];

	// startsWith: field.startsWith("prefix")
	m = s.match(/^(\w+)\.startsWith\(["']([^"']*)["']\)$/);
	if (m && m[1] in vars) return vars[m[1]].startsWith(m[2]);

	// contains: field.contains("sub")
	m = s.match(/^(\w+)\.contains\(["']([^"']*)["']\)$/);
	if (m && m[1] in vars) return vars[m[1]].includes(m[2]);

	// in list: field in ["a","b","c"]
	m = s.match(/^(\w+)\s+in\s+\[([^\]]*)\]$/);
	if (m && m[1] in vars) {
		const items = m[2].split(",").map((x) => x.trim().replace(/^["']|["']$/g, ""));
		return items.includes(vars[m[1]]);
	}

	// headers["key"] == "value"
	m = s.match(/^headers\[["']([^"']*)["']\]\s*==\s*["']([^"']*)["']$/);
	if (m) {
		const hVal = vars[`headers.${m[1]}`] ?? vars[`header_${m[1]}`];
		if (hVal !== undefined) return hVal === m[2];
	}

	// Numeric comparisons: field >= n, field <= n, field > n, field < n
	m = s.match(/^(\w+)\s*(>=|<=|>|<)\s*(\d+(?:\.\d+)?)$/);
	if (m && m[1] in vars) {
		const lv = parseFloat(vars[m[1]]);
		const rv = parseFloat(m[3]);
		if (!isNaN(lv)) {
			if (m[2] === ">") return lv > rv;
			if (m[2] === "<") return lv < rv;
			if (m[2] === ">=") return lv >= rv;
			if (m[2] === "<=") return lv <= rv;
		}
	}

	return null; // too complex — skip
}

function isWrappedInParens(s: string): boolean {
	if (!s.startsWith("(") || !s.endsWith(")")) return false;
	let d = 0;
	for (let i = 0; i < s.length; i++) {
		if (s[i] === "(") d++;
		else if (s[i] === ")") d--;
		if (d === 0 && i < s.length - 1) return false;
	}
	return true;
}

function splitOn(expr: string, op: "&&" | "||"): string[] {
	const trimmed = expr.trim();
	const s = isWrappedInParens(trimmed) ? trimmed.slice(1, -1) : trimmed;
	const parts: string[] = [];
	let depth = 0, current = "";
	for (let i = 0; i < s.length; i++) {
		const ch = s[i];
		if (ch === "(" || ch === "[") depth++;
		else if (ch === ")" || ch === "]") depth--;
		else if (depth === 0 && s.slice(i, i + 2) === op) {
			const p = current.trim();
			if (p) parts.push(p);
			current = "";
			i++;
			continue;
		}
		current += ch;
	}
	const last = current.trim();
	if (last) parts.push(last);
	if (parts.length < 2) return [expr.trim()];
	return parts;
}

/** Cartesian product of two arrays of string arrays. */
function cartesian(a: string[][], b: string[][]): string[][] {
	const result: string[][] = [];
	for (const x of a) for (const y of b) result.push([...x, ...y]);
	return result;
}

/** Expand a CEL string into one or more condition lists, fanning out on OR.
 *  Handles nested disjunctions such as `a && (b || c)` → [["a","b"],["a","c"]].
 */
export function expandCEL(cel: string): string[][] {
	const trimmed = cel?.trim() || "";
	if (!trimmed) return [[]];
	// OR has lower precedence than AND → split on || first (outer level)
	const orBranches = splitOn(trimmed, "||");
	const result: string[][] = [];
	for (const branch of orBranches) {
		const andParts = splitOn(branch.trim(), "&&").map((p) => p.trim()).filter(Boolean);
		if (!andParts.length) { result.push([branch.trim()]); continue; }

		// For each AND part, check if it is a parenthesized OR — expand recursively
		// and Cartesian-product with the accumulated combinations so far.
		let combinations: string[][] = [[]];
		for (const part of andParts) {
			if (isWrappedInParens(part)) {
				const inner = part.slice(1, -1).trim();
				const innerBranches = splitOn(inner, "||");
				if (innerBranches.length > 1) {
					const innerExpanded = innerBranches.flatMap((b) => expandCEL(b.trim()));
					combinations = cartesian(combinations, innerExpanded);
					continue;
				}
			}
			combinations = combinations.map((c) => [...c, part]);
		}
		result.push(...combinations);
	}
	return result.length ? result : [[]];
}

/**
 * Normalize a CEL condition token for trie key comparison.
 * Collapses whitespace around operators so "a == b" and "a==b" are the same key.
 */
export function normalizeCond(cond: string): string {
	return cond.trim()
		.replace(/\s*(==|!=|>=|<=|>|<)\s*/g, (_, op) => ` ${op} `)
		.replace(/\s+/g, " ");
}
