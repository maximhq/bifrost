import { TooltipProvider } from "@/components/ui/tooltip";
import type { Budget } from "@/lib/types/governance";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { BudgetDisplay } from "./budgetDisplay";

const budget: Budget = { id: "b1", max_limit: 1000, current_usage: 19.37, reset_duration: "1w", last_reset: "2026-11-16T00:00:00Z" };

const render = (sharedNote?: string) =>
	renderToStaticMarkup(
		<TooltipProvider>
			<BudgetDisplay budgets={[budget]} sharedNote={sharedNote} />
		</TooltipProvider>,
	);

describe("BudgetDisplay shared tag", () => {
	it("tags the line when the budget is shared", () => {
		expect(render("Shared by every key assigned to Ann.")).toContain('data-testid="budget-shared-tag"');
	});

	it("leaves a key's own budget untagged", () => {
		expect(render()).not.toContain("budget-shared-tag");
		expect(render("")).not.toContain("budget-shared-tag");
	});

	it("lets keyboard users focus the line to open its tooltip", () => {
		expect(render()).toMatch(/<div[^>]*tabindex="0"/);
	});
});