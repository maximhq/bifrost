import { describe, expect, it } from "vitest";
import { latestGraceDeadline } from "./virtualKeysTable.utils";

describe("latestGraceDeadline", () => {
	it("returns null when no rotated key has a grace window", () => {
		expect(latestGraceDeadline([])).toBeNull();
		expect(latestGraceDeadline([{ previous_value_expires_at: null }, {}])).toBeNull();
	});

	it("returns the sole deadline when only one key carries one", () => {
		expect(latestGraceDeadline([{ previous_value_expires_at: null }, { previous_value_expires_at: "2026-08-28T10:00:00Z" }])).toBe(
			"2026-08-28T10:00:00Z",
		);
	});

	// The regression: the server stamps each key from its own time.Now(), so a bulk
	// response carries slightly different deadlines. "Previous keys remain valid
	// until X" must name a time after which NO retired value works, so X has to be
	// the latest deadline - not whichever key happened to come back first.
	it("returns the latest deadline when keys differ, regardless of response order", () => {
		const keys = [
			{ previous_value_expires_at: "2026-08-28T10:00:00.100Z" },
			{ previous_value_expires_at: "2026-08-28T10:00:00.350Z" },
			{ previous_value_expires_at: "2026-08-28T10:00:00.225Z" },
		];
		expect(latestGraceDeadline(keys)).toBe("2026-08-28T10:00:00.350Z");
		expect(latestGraceDeadline([...keys].reverse())).toBe("2026-08-28T10:00:00.350Z");
	});

	it("ignores keys without a grace window when picking the latest", () => {
		expect(
			latestGraceDeadline([{}, { previous_value_expires_at: "2026-08-28T10:00:00.500Z" }, { previous_value_expires_at: null }]),
		).toBe("2026-08-28T10:00:00.500Z");
	});
});
