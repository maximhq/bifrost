import "@/lib/i18n";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { RateLimitDisplay } from "./rateLimitDisplay";

describe("RateLimitDisplay", () => {
	it("lists every request limit after the token limit, marking scopes only when asked", () => {
		const requestLimits = [
			{ id: "a", request_max_limit: 100, request_reset_duration: "1h", request_current_usage: 4, scope: "shared" as const },
			{ id: "b", request_max_limit: 7, request_reset_duration: "1d", request_current_usage: 7, scope: "mcp" as const },
		];
		const rateLimits = { token_max_limit: 5000, token_reset_duration: "1h", token_current_usage: 0 };
		const marked = renderToStaticMarkup(<RateLimitDisplay rateLimits={rateLimits} requestLimits={requestLimits} showScope />);
		expect(marked).toContain("5K");
		expect(marked).toContain("100");
		expect(marked).toContain("7");
		expect(marked).toContain('data-testid="budget-scope-badge-shared"');
		expect(marked).toContain('data-testid="budget-scope-badge-mcp"');
		// An exhausted rule draws a red bar.
		expect(marked).toContain("bg-red-500/70");

		const unmarked = renderToStaticMarkup(<RateLimitDisplay rateLimits={null} requestLimits={requestLimits} />);
		expect(unmarked).not.toContain("budget-scope-badge");
		expect(unmarked).toContain("100");
	});

	it("renders scopeLabel on every limit in limitOnly mode as well as bar mode", () => {
		const rateLimits = { token_max_limit: 5000, token_reset_duration: "1h", token_current_usage: 0 };
		const requestLimits = [{ id: "a", request_max_limit: 100, request_reset_duration: "1h", request_current_usage: 0 }];
		for (const limitOnly of [true, false]) {
			const markup = renderToStaticMarkup(
				<RateLimitDisplay rateLimits={rateLimits} requestLimits={requestLimits} limitOnly={limitOnly} scopeLabel="per key" />,
			);
			expect(markup.match(/per key/g)).toHaveLength(2);
		}
	});

	it("shows a dash when nothing is limited", () => {
		expect(renderToStaticMarkup(<RateLimitDisplay rateLimits={null} requestLimits={[]} />)).toContain("-");
	});
});