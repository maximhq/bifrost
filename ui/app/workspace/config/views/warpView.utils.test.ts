import { describe, expect, it } from "vitest";
import type { WarpBackfillJob } from "@/lib/types/warp";
import {
	requireFiniteNumber,
	retainedWarpBackfillForSpace,
	retainFinishedWarpBackfill,
	validateWarpMaxIterations,
	warpSavedSpaceKey,
	warpUnavailableMessage,
	warpVectorStoreBadge,
	warpVectorStoreRequirementMet,
} from "./warpView.utils";

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

describe("validateWarpMaxIterations", () => {
	// The server refuses 1: the last step is reserved for answering, so a budget
	// of one never produces an answer.
	it("rejects 1, which the server refuses", () => {
		expect(validateWarpMaxIterations(1)).not.toBe(true);
	});

	it("accepts the server's range, 2 through 20", () => {
		expect(validateWarpMaxIterations(2)).toBe(true);
		expect(validateWarpMaxIterations(8)).toBe(true);
		expect(validateWarpMaxIterations(20)).toBe(true);
	});

	it("rejects values outside the range or not a whole number", () => {
		expect(validateWarpMaxIterations(0)).not.toBe(true);
		expect(validateWarpMaxIterations(21)).not.toBe(true);
		expect(validateWarpMaxIterations(2.5)).not.toBe(true);
		expect(validateWarpMaxIterations(Number.NaN)).not.toBe(true);
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

describe("warpVectorStoreBadge", () => {
	it("names the connected store and its type", () => {
		expect(
			warpVectorStoreBadge({
				vector_store_connected: true,
				connected_vector_store: { name: "warp-qdrant", type: "qdrant", primary: false },
			}),
		).toEqual({ connected: true, name: "warp-qdrant", type: "qdrant" });
	});

	it("gives the server's reason when Warp has no store", () => {
		const error = 'Warp\'s vector store "warp-qdrant" could not be reached at startup';
		expect(warpVectorStoreBadge({ vector_store_connected: false, connected_vector_store: null, vector_store_error: error })).toEqual({
			connected: false,
			error,
		});
		expect(warpVectorStoreBadge({ vector_store_connected: false, connected_vector_store: null })).toEqual({
			connected: false,
			error: null,
		});
	});

	// A server from before the store choice sends only vector_store_connected.
	it("falls back to vector_store_connected when connected_vector_store is absent", () => {
		expect(warpVectorStoreBadge({ vector_store_connected: true })).toEqual({ connected: true, name: null, type: null });
		expect(warpVectorStoreBadge(undefined)).toEqual({ connected: false, error: null });
	});
});

describe("warpUnavailableMessage", () => {
	it("reads the message of a {reason, message} refusal", () => {
		expect(
			warpUnavailableMessage({
				status: 503,
				data: { reason: "no_vector_store", message: 'Warp\'s vector store "warp-qdrant" is disabled in config.json' },
			}),
		).toBe('Warp\'s vector store "warp-qdrant" is disabled in config.json');
	});

	it("returns null for the {error: {message}} shape and anything else", () => {
		expect(warpUnavailableMessage({ status: 400, data: { error: { message: "bad" } } })).toBeNull();
		expect(warpUnavailableMessage({ status: 409, data: { error: { message: "a backfill is in progress" } } })).toBeNull();
		expect(warpUnavailableMessage(undefined)).toBeNull();
		expect(warpUnavailableMessage(new Error("x"))).toBeNull();
	});
});

describe("warpVectorStoreRequirementMet", () => {
	it("needs a connected store only to turn Warp on", () => {
		expect(warpVectorStoreRequirementMet({ vector_store_connected: true, enabled: false })).toBe(true);
		expect(warpVectorStoreRequirementMet({ vector_store_connected: false, enabled: false })).toBe(false);
		expect(warpVectorStoreRequirementMet({ vector_store_connected: false, enabled: true })).toBe(true);
		expect(warpVectorStoreRequirementMet(undefined)).toBe(false);
	});
});