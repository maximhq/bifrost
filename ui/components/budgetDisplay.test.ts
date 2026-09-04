import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

// vitest has no React plugin here, so the component cannot be rendered; this
// pins the markup contract at the source level instead (same approach as
// topbar.test.ts).
const source = readFileSync(fileURLToPath(new URL("./budgetDisplay.tsx", import.meta.url)), "utf8");

describe("BudgetDisplay overflow trigger", () => {
	it('renders the "+N more" tooltip trigger as a focusable button so keyboard users can open it', () => {
		const trigger = source.match(/<TooltipTrigger asChild>\s*<(\w+)[^>]*>\s*\+\{hidden\.length\} more/);
		expect(trigger, "could not find the +N more trigger").not.toBeNull();
		expect(trigger![1]).toBe("button");
		expect(trigger![0]).toMatch(/type="button"/);
	});
});
