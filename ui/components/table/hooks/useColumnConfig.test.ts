import { describe, expect, it } from "vitest";
import { mergeColumnEntries } from "./useColumnConfig";

const ids = ["timestamp", "relay_name", "method", "request", "app", "cost"];
const entry = (id: string, visible = true) => ({ id, visible, pinned: false as const });

describe("mergeColumnEntries", () => {
	it("lays out a fresh table in the default order, hiding the default-hidden columns", () => {
		expect(mergeColumnEntries([], ids, new Set(["app"]))).toEqual([
			entry("timestamp"),
			entry("relay_name"),
			entry("method"),
			entry("request"),
			entry("app", false),
			entry("cost"),
		]);
	});

	it("puts a column added since the config was saved next to its default neighbour, not at the end", () => {
		const saved = [entry("timestamp"), entry("relay_name"), entry("request"), entry("app"), entry("cost")];
		expect(mergeColumnEntries(saved, ids, new Set()).map((e) => e.id)).toEqual([
			"timestamp",
			"relay_name",
			"method",
			"request",
			"app",
			"cost",
		]);
	});

	it("keeps the saved order and flags and drops columns that no longer exist", () => {
		const saved = [
			entry("cost"),
			entry("gone"),
			entry("timestamp", false),
			entry("relay_name"),
			entry("method"),
			entry("request"),
			entry("app"),
		];
		expect(mergeColumnEntries(saved, ids, new Set())).toEqual([
			entry("cost"),
			entry("timestamp", false),
			entry("relay_name"),
			entry("method"),
			entry("request"),
			entry("app"),
		]);
	});
});