import { describe, expect, test } from "vitest";
import { getLiveToggleState } from "./timeRange";

describe("getLiveToggleState", () => {
	test("turning live on from an absolute range switches to the default period", () => {
		expect(getLiveToggleState(true, "")).toEqual({ polling: true, period: "1h", offset: 0 });
		expect(getLiveToggleState(true, "", "24h")).toEqual({ polling: true, period: "24h", offset: 0 });
	});

	test("turning live on keeps an active relative period", () => {
		expect(getLiveToggleState(true, "6h")).toEqual({ polling: true });
	});

	test("turning live off never touches the time range", () => {
		expect(getLiveToggleState(false, "")).toEqual({ polling: false });
		expect(getLiveToggleState(false, "7d")).toEqual({ polling: false });
	});
});