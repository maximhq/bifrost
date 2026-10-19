import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { CalendarAlignmentNotice } from "./calendarAlignmentNotice";
import { calendarWindowDurations, isMultiUnitDuration, supportsCalendarAlignment } from "@/lib/constants/governance";

describe("calendar alignment notices", () => {
	it("only shows affected durations while alignment is enabled", () => {
		const durations = ["7d", "2w", "6h", "7d", "1M"];
		const markup = renderToStaticMarkup(<CalendarAlignmentNotice enabled durations={durations} />);
		expect(markup).toContain("7d, 2w, 6h will use rolling windows");
		expect(markup).toContain('role="alert"');
		expect(renderToStaticMarkup(<CalendarAlignmentNotice enabled={false} durations={durations} />)).toBe("");
		expect(renderToStaticMarkup(<CalendarAlignmentNotice enabled durations={["1d", "1w", "1M", "1Q", "1Y"]} />)).toBe("");
	});

	it("includes active provider and model windows without reporting unset limits", () => {
		expect(
			calendarWindowDurations([
				{
					budgets: [{ max_limit: 0, reset_duration: "7d" }, { reset_duration: "3M" }],
					rate_limit: { request_max_limit: 10, request_reset_duration: "2w" },
					model_budgets: [{ rate_limit: { token_max_limit: 100, token_reset_duration: "6h" } }],
				},
			]),
		).toEqual(["7d", "2w", "6h"]);
	});

	it("keeps calendar labels consistent with backend single-unit alignment", () => {
		for (const duration of ["7d", "2w", "3M", "2Q", "2Y", "30m", "6h"]) {
			expect(isMultiUnitDuration(duration)).toBe(true);
			expect(supportsCalendarAlignment(duration)).toBe(false);
		}
		for (const duration of ["1d", "1w", "1M", "1Q", "1Y"]) expect(supportsCalendarAlignment(duration)).toBe(true);
		for (const duration of ["1.5d", "2h30m", "2q", "0d", ""]) expect(isMultiUnitDuration(duration)).toBe(false);
	});
});