import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

// vitest has no React plugin here, so the component cannot be rendered; this
// pins the prop plumbing at the source level instead (same approach as
// topbar.test.ts).
const source = readFileSync(fileURLToPath(new URL("./rateLimitDisplay.tsx", import.meta.url)), "utf8");

describe("RateLimitDisplay scopeLabel", () => {
	it("renders scopeLabel in limitOnly mode as well as bar mode", () => {
		const limitText = source.slice(source.indexOf("function LimitText("), source.indexOf("function Bar("));
		expect(limitText).toMatch(/scopeLabel\s*\?/);

		// Both limitOnly branches must forward the prop.
		const limitOnlyUses = source.match(/<LimitText[\s\S]*?\/>/g) ?? [];
		expect(limitOnlyUses).toHaveLength(2);
		for (const use of limitOnlyUses) {
			expect(use).toMatch(/scopeLabel=\{scopeLabel\}/);
		}
	});
});
