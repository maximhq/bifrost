import { describe, expect, it } from "vitest";
import type { WarpBackfillJob } from "@/lib/types/warp";
import { requireFiniteNumber, retainedWarpBackfillForSpace, retainFinishedWarpBackfill, warpSavedSpaceKey } from "./warpView.utils";

describe("requireFiniteNumber", () => {
	// Clearing a number input is the case that matters: valueAsNumber gives NaN,
	// React Hook Form skips min/max because the DOM value is empty, and NaN
	// serializes to null in the request body.
	it("rejects NaN from a cleared input", () => {
		expect(requireFiniteNumber(Number.NaN, "A value is required")).toBe("A value is required");
	});

	it("rejects the non-finite results of a bad parse", () => {
		expect(requireFiniteNumber(Number.POSITIVE_INFINITY, "nope")).toBe("nope");
		expect(requireFiniteNumber(Number.NEGATIVE_INFINITY, "nope")).toBe("nope");
	});

	// undefined and null are what an absent field looks like; they are not a
	// number either, so the same message applies.
	it("rejects values that are not numbers at all", () => {
		expect(requireFiniteNumber(undefined, "nope")).toBe("nope");
		expect(requireFiniteNumber(null, "nope")).toBe("nope");
		expect(requireFiniteNumber("8", "nope")).toBe("nope");
	});

	// Zero must pass this rule. It is out of range for both fields, but that is
	// min's job to say - reporting "a value is required" for a value the user
	// actually typed would be a confusing error.
	it("accepts any finite number, including zero and negatives", () => {
		expect(requireFiniteNumber(0, "nope")).toBe(true);
		expect(requireFiniteNumber(-1, "nope")).toBe(true);
		expect(requireFiniteNumber(8, "nope")).toBe(true);
	});
});

const completedJob: WarpBackfillJob = {
	id: "warp-backfill-1",
	status: "completed",
	total: 120,
	scanned: 120,
	indexed: 118,
	skipped: 2,
	failed: 0,
};

describe("warpSavedSpaceKey", () => {
	const saved = {
		embedding_provider: "openai",
		embedding_model: "text-embedding-3-small",
		embedding_dimension: 1536,
		log_vector_store_namespace: "BifrostWarpLogs",
	};

	it("changes when any of the four space fields change", () => {
		const key = warpSavedSpaceKey(saved);
		expect(warpSavedSpaceKey({ ...saved })).toBe(key);
		expect(warpSavedSpaceKey({ ...saved, embedding_provider: "cohere" })).not.toBe(key);
		expect(warpSavedSpaceKey({ ...saved, embedding_model: "text-embedding-3-large" })).not.toBe(key);
		expect(warpSavedSpaceKey({ ...saved, embedding_dimension: 3072 })).not.toBe(key);
		expect(warpSavedSpaceKey({ ...saved, log_vector_store_namespace: "BifrostWarpLogsV2" })).not.toBe(key);
	});

	// Before the config query lands there is no space to compare against; the
	// key still has to be a value, and a distinct one from any real config.
	it("gives an unloaded config a key no saved space can equal", () => {
		expect(warpSavedSpaceKey(undefined)).not.toBe(warpSavedSpaceKey(saved));
	});
});

describe("retainFinishedWarpBackfill", () => {
	const spaceA = warpSavedSpaceKey({ embedding_dimension: 1536, log_vector_store_namespace: "BifrostWarpLogs" });
	const spaceB = warpSavedSpaceKey({ embedding_dimension: 3072, log_vector_store_namespace: "BifrostWarpLogsV2" });

	it("keeps a job that finished under the space still saved", () => {
		expect(retainFinishedWarpBackfill(completedJob, spaceA, spaceA)).toEqual({ job: completedJob, spaceKey: spaceA });
	});

	// The id-pinned poll answers for the old job after the space was saved
	// over; that result must not survive as "Completed" under the new space.
	it("drops a job whose space was saved over while its terminal status was in flight", () => {
		expect(retainFinishedWarpBackfill(completedJob, spaceA, spaceB)).toBeNull();
	});

	// A pinned id only ever comes from starting or discovering a running job,
	// both under the configured space, so an unobserved space is the saved one.
	it("assumes the saved space when the run's space was never observed", () => {
		expect(retainFinishedWarpBackfill(completedJob, null, spaceB)).toEqual({ job: completedJob, spaceKey: spaceB });
	});
});

describe("retainedWarpBackfillForSpace", () => {
	const spaceA = warpSavedSpaceKey({ embedding_dimension: 1536 });
	const spaceB = warpSavedSpaceKey({ embedding_dimension: 3072 });

	it("returns the job while the saved space is the one it ran under", () => {
		expect(retainedWarpBackfillForSpace({ job: completedJob, spaceKey: spaceA }, spaceA)).toBe(completedJob);
	});

	// Revalidated on every read: a one-shot clear on space change misses a
	// terminal status that lands after the clear has already fired.
	it("hides the job once the saved space moves off it", () => {
		expect(retainedWarpBackfillForSpace({ job: completedJob, spaceKey: spaceA }, spaceB)).toBeNull();
		expect(retainedWarpBackfillForSpace(null, spaceB)).toBeNull();
	});
});