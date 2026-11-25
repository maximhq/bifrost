import type { RelayLog } from "@/lib/types/relays";
import { describe, expect, it } from "vitest";
import { relayJobSettledAt, relayJobStatusLabel, relayJobUntracked } from "./relayJob";

describe("relayJobSettledAt", () => {
	it("reads a job row's end from its settlement, and nothing for other rows", () => {
		const job_debug = { settled_at: "2026-11-24T10:00:05Z", polls: 3 } as RelayLog["job_debug"];
		expect(relayJobSettledAt({ kind: "job", job_debug })?.toISOString()).toBe("2026-11-24T10:00:05.000Z");
		expect(relayJobSettledAt({ kind: "call", job_debug })).toBeNull();
		expect(relayJobSettledAt({ kind: "job", job_debug: undefined })).toBeNull();
		expect(relayJobSettledAt({ kind: "job", job_debug: { ...job_debug!, settled_at: "not a date" } })).toBeNull();
	});
});
describe("relayJobStatusLabel", () => {
	it("shows an upstream's own status as it reported it", () => {
		expect(relayJobStatusLabel("SUCCEEDED")).toBe("SUCCEEDED");
	});
	it("translates Bifrost's internal end codes", () => {
		expect(relayJobStatusLabel("relay_job_timeout")).toBe("Did not finish before its timeout");
		expect(relayJobStatusLabel("relay_job_relay_deleted")).toBe("The relay was deleted before the job finished");
	});
	it("is empty when there is no status", () => {
		expect(relayJobStatusLabel(undefined)).toBe("");
		expect(relayJobStatusLabel(null)).toBe("");
	});
});

describe("relayJobUntracked", () => {
	it("says why a start call's job was not tracked", () => {
		expect(relayJobUntracked({ kind: "call", job_debug: { outcome: "untracked", polls: 0, settled_at: "", last_error: "no id" } })).toBe(
			"no id",
		);
	});
	it("is null for tracked calls and for job rows", () => {
		expect(relayJobUntracked({ kind: "call" })).toBeNull();
		expect(relayJobUntracked({ kind: "job", job_debug: { outcome: "untracked", polls: 0, settled_at: "" } })).toBeNull();
	});
});