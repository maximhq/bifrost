import "@/lib/i18n";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import MultiBudgetLines, { pickNewLineDuration, RequestLimitLines } from "./multibudgets";
import { resetDurationOptions } from "@/lib/constants/governance";

const noop = () => {};

describe("MultiBudgetLines", () => {
	it("keeps the budget wording when no labels are passed", () => {
		const empty = renderToStaticMarkup(<MultiBudgetLines data-testid="b" lines={[]} onChange={noop} />);
		expect(empty).toContain("Add Budget");
		expect(empty).toContain("No budget limits configured.");
		const line = renderToStaticMarkup(
			<MultiBudgetLines data-testid="b" lines={[{ max_limit: 5, reset_duration: "1d" }]} onChange={noop} />,
		);
		expect(line).toContain("Maximum Spend (USD)");
		expect(line).toContain('aria-label="Remove budget 1"');
	});

	it("uses the copy override and shows an amount error under its line", () => {
		const markup = renderToStaticMarkup(
			<MultiBudgetLines
				data-testid="b"
				lines={[
					{ max_limit: 0, reset_duration: "1h" },
					{ max_limit: 2, reset_duration: "1h" },
				]}
				onChange={noop}
				copy={{
					amountLabel: "Cap",
					addLabel: "Add cap",
					emptyLabel: "No caps",
					removeAriaLabel: (n) => `Drop cap ${n}`,
					duplicateLabel: "Same window twice",
					duplicateScopeLabel: "Same window and scope twice",
				}}
				amountError={(v) => (v === 0 ? "Zero is not allowed" : undefined)}
			/>,
		);
		expect(markup).toContain("Cap");
		expect(markup).toContain("Add cap");
		expect(markup).toContain('aria-label="Drop cap 2"');
		expect(markup).toContain("Same window twice");
		expect(markup.match(/Zero is not allowed/g)).toHaveLength(1);
	});
});

describe("RequestLimitLines", () => {
	it("uses the request wording", () => {
		const empty = renderToStaticMarkup(<RequestLimitLines data-testid="r" lines={[]} onChange={noop} />);
		expect(empty).toContain("Request Limits");
		expect(empty).toContain("Add Limit");
		expect(empty).toContain("No request limits configured.");
		const markup = renderToStaticMarkup(
			<RequestLimitLines
				data-testid="r"
				lines={[
					{ max_limit: 0, reset_duration: "1h", scope: "mcp" },
					{ max_limit: 3, reset_duration: "1h", scope: "mcp" },
				]}
				onChange={noop}
				showScope
			/>,
		);
		expect(markup).toContain("Maximum Requests");
		expect(markup).toContain('aria-label="Remove request limit 1"');
		expect(markup).toContain("request limits with the same scope must use different intervals");
		expect(markup).toContain("Must be a positive whole number");
	});
});
describe("pickNewLineDuration", () => {
	it("starts a request limit on the preferred hour, then the first unused period, then the first option", () => {
		expect(pickNewLineDuration(resetDurationOptions, () => false, "1h")).toBe("1h");
		expect(pickNewLineDuration(resetDurationOptions, (value) => value === "1h", "1h")).toBe(resetDurationOptions[0].value);
		expect(pickNewLineDuration(resetDurationOptions, () => true, "1h")).toBe(resetDurationOptions[0].value);
		expect(pickNewLineDuration(resetDurationOptions, () => false)).toBe(resetDurationOptions[0].value);
	});
});