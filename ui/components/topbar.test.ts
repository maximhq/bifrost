import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const read = (relative: string) => readFileSync(fileURLToPath(new URL(relative, import.meta.url)), "utf8");

/**
 * Tailwind v4 generates a `text-<size>` utility only where a matching `--text-<size>`
 * token exists, and it fails silently otherwise: the class lands in the markup, no
 * font-size rule is emitted, and the element quietly inherits its parent's size.
 * `md` is the trap, because `rounded-md` and `shadow-md` are real while `text-md`
 * has never been part of the scale.
 */
function definedTextSizes(): Set<string> {
	const names = new Set<string>();
	for (const css of [read("../node_modules/tailwindcss/theme.css"), read("../app/globals.css")]) {
		for (const [, name] of css.matchAll(/--text-([a-z0-9]+):/g)) names.add(name);
	}
	return names;
}

// text-* is also the colour namespace, so only suffixes shaped like a size are
// checked - text-muted-foreground is not a broken font-size.
const sizeShaped = /^(\d?xs|sm|md|base|lg|\d?xl)$/;

describe("topbar text utilities", () => {
	it("uses only text sizes Tailwind actually generates", () => {
		const defined = definedTextSizes();
		// Only className attributes: prose in a comment may name text-md precisely
		// because it is the thing being warned about.
		const source = read("./topbar.tsx");
		const classNames = [...source.matchAll(/className=(?:"([^"]*)"|\{cn\(([^)]*)\))/g)].map(([, quoted, call]) => quoted ?? call);
		const used = classNames
			.flatMap((value) => [...value.matchAll(/\btext-([a-z0-9]+)\b/g)].map(([, name]) => name))
			.filter((name) => sizeShaped.test(name));

		expect(used.length).toBeGreaterThan(0);
		expect(used.filter((name) => !defined.has(name))).toEqual([]);
	});
});
