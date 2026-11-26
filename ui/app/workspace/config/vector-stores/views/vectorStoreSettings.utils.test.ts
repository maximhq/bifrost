import { MAX_VECTOR_MAX_SIZE_MB, MAX_VECTOR_RETENTION_DAYS } from "@/lib/types/schemas";
import { describe, expect, it } from "vitest";
import {
	buildVectorStoreUpdate,
	draftFromStore,
	followStoredValues,
	maxSizeMBError,
	parseFieldText,
	retentionDaysError,
	toFieldText,
} from "./vectorStoreSettings.utils";

describe("toFieldText / parseFieldText", () => {
	it("renders 0 as blank and reads blank as 0", () => {
		expect(toFieldText(0)).toBe("");
		expect(toFieldText(30)).toBe("30");
		expect(parseFieldText("")).toBe(0);
		expect(parseFieldText("  ")).toBe(0);
		expect(parseFieldText(" 14 ")).toBe(14);
	});

	it("reads a fraction whole instead of cutting it to an integer", () => {
		expect(parseFieldText("1.5")).toBe(1.5);
		expect(Number.isNaN(parseFieldText("abc"))).toBe(true);
	});

	it("builds the draft from a store", () => {
		expect(draftFromStore({ retention_days: 0, max_size_mb: 2048 })).toEqual({ retentionDays: "", maxSizeMB: "2048" });
	});
});

describe("retentionDaysError", () => {
	it("accepts blank, 0 and the server maximum", () => {
		expect(retentionDaysError("")).toBeNull();
		expect(retentionDaysError("0")).toBeNull();
		expect(retentionDaysError(String(MAX_VECTOR_RETENTION_DAYS))).toBeNull();
	});

	it("refuses values the server refuses", () => {
		expect(MAX_VECTOR_RETENTION_DAYS).toBe(106751);
		expect(retentionDaysError("106752")).not.toBeNull();
		expect(retentionDaysError("-1")).not.toBeNull();
		expect(retentionDaysError("7.5")).not.toBeNull();
		expect(retentionDaysError("abc")).not.toBeNull();
	});
});

describe("maxSizeMBError", () => {
	it("accepts blank, 0 and the server maximum", () => {
		expect(maxSizeMBError("")).toBeNull();
		expect(maxSizeMBError("0")).toBeNull();
		expect(maxSizeMBError(String(MAX_VECTOR_MAX_SIZE_MB))).toBeNull();
	});

	it("refuses values the server refuses", () => {
		expect(MAX_VECTOR_MAX_SIZE_MB).toBe(8796093022207);
		expect(maxSizeMBError("8796093022208")).not.toBeNull();
		expect(maxSizeMBError("-5")).not.toBeNull();
		expect(maxSizeMBError("1.5")).not.toBeNull();
	});
});

describe("buildVectorStoreUpdate", () => {
	const store = { retention_days: 30, max_size_mb: 0 };

	it("returns null when nothing changed", () => {
		expect(buildVectorStoreUpdate(store, { retentionDays: "30", maxSizeMB: "" })).toBeNull();
		expect(buildVectorStoreUpdate(store, { retentionDays: " 30 ", maxSizeMB: "0" })).toBeNull();
	});

	it("sends only the changed fields", () => {
		expect(buildVectorStoreUpdate(store, { retentionDays: "14", maxSizeMB: "" })).toEqual({ retention_days: 14 });
		expect(buildVectorStoreUpdate(store, { retentionDays: "30", maxSizeMB: "4096" })).toEqual({ max_size_mb: 4096 });
		expect(buildVectorStoreUpdate(store, { retentionDays: "7", maxSizeMB: "1024" })).toEqual({ retention_days: 7, max_size_mb: 1024 });
	});

	it("sends a cleared field as 0", () => {
		expect(buildVectorStoreUpdate(store, { retentionDays: "", maxSizeMB: "" })).toEqual({ retention_days: 0 });
	});

	it("returns null while a field is invalid", () => {
		expect(buildVectorStoreUpdate(store, { retentionDays: "106752", maxSizeMB: "" })).toBeNull();
		expect(buildVectorStoreUpdate(store, { retentionDays: "14", maxSizeMB: "1.5" })).toBeNull();
	});
});
describe("followStoredValues", () => {
	const stored = (retention_days: number, max_size_mb = 0) => ({ retention_days, max_size_mb });

	it("handles store names that are names of object properties", () => {
		const stores = ["constructor", "toString", "__proto__", "hasOwnProperty"].map((name) => ({ name, ...stored(7) }));
		const next = followStoredValues(stores, new Map(), new Map(), new Map());
		for (const store of stores) {
			expect(next.get(store.name)).toEqual({ retentionDays: "7", maxSizeMB: "" });
		}
	});

	it("keeps what is typed into a store whose stored values did not change", () => {
		const typing = { retentionDays: "14", maxSizeMB: "" };
		const next = followStoredValues([{ name: "a", ...stored(7) }], new Map([["a", typing]]), new Map([["a", stored(7)]]), new Map());
		expect(next.get("a")).toBe(typing);
	});

	it("takes the new stored values, unless the store was edited after its save was sent", () => {
		const sent = { retentionDays: "30", maxSizeMB: "" };
		const saved = followStoredValues(
			[{ name: "a", ...stored(30) }],
			new Map([["a", sent]]),
			new Map([["a", stored(7)]]),
			new Map([["a", sent]]),
		);
		expect(saved.get("a")).toEqual({ retentionDays: "30", maxSizeMB: "" });

		const editedSince = { retentionDays: "45", maxSizeMB: "" };
		const kept = followStoredValues(
			[{ name: "a", ...stored(30) }],
			new Map([["a", editedSince]]),
			new Map([["a", stored(7)]]),
			new Map([["a", sent]]),
		);
		expect(kept.get("a")).toBe(editedSince);
	});
});