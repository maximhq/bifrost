import type { RelayLog } from "@/lib/types/relays";
import { describe, expect, it } from "vitest";
import { relayJobSettledAt } from "./relayJob";

describe("relayJobSettledAt", () => {
	it("reads a job row's end from its settlement, and nothing for other rows", () => {
		const job_debug = { settled_at: "2026-11-24T10:00:05Z", polls: 3 } as RelayLog["job_debug"];
		expect(relayJobSettledAt({ kind: "job", job_debug })?.toISOString()).toBe("2026-11-24T10:00:05.000Z");
		expect(relayJobSettledAt({ kind: "call", job_debug })).toBeNull();
		expect(relayJobSettledAt({ kind: "job", job_debug: undefined })).toBeNull();
		expect(relayJobSettledAt({ kind: "job", job_debug: { ...job_debug!, settled_at: "not a date" } })).toBeNull();
	});
});