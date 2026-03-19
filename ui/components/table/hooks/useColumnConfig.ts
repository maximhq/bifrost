import { ColumnPinningState, VisibilityState } from "@tanstack/react-table";
import { parseAsString, useQueryState } from "nuqs";
import { useCallback, useMemo } from "react";

export interface ColumnConfigEntry {
	id: string;
	visible: boolean;
	pinned: "left" | "right" | false;
}

interface UseColumnConfigOptions {
	/** All available column IDs in their default order */
	columnIds: string[];
	/** URL query param name for persistence */
	paramName?: string;
	/** Columns excluded from configuration (always visible, always in position) */
	fixedColumns?: { left?: string[]; right?: string[] };
}

// URL format: col1,col2:h,col3:l,col4:r
// no suffix = visible & unpinned, :h = hidden, :l = pinned left, :r = pinned right
function serialize(entries: ColumnConfigEntry[]): string {
	return entries
		.map((e) => {
			let flags = "";
			if (!e.visible) flags += "h";
			if (e.pinned === "left") flags += "l";
			if (e.pinned === "right") flags += "r";
			const encoded = encodeURIComponent(e.id);
			return flags ? `${encoded}:${flags}` : encoded;
		})
		.join(",");
}

function deserialize(str: string): ColumnConfigEntry[] {
	if (!str) return [];
	return str.split(",").map((part) => {
		const [rawId, flags = ""] = part.split(":");
		const id = decodeURIComponent(rawId);
		return {
			id,
			visible: !flags.includes("h"),
			pinned: flags.includes("l") ? ("left" as const) : flags.includes("r") ? ("right" as const) : false,
		};
	});
}

export function useColumnConfig({ columnIds, paramName = "cols", fixedColumns }: UseColumnConfigOptions) {
	const [raw, setRaw] = useQueryState(paramName, parseAsString.withDefault(""));

	const fixedLeft = fixedColumns?.left ?? [];
	const fixedRight = fixedColumns?.right ?? [];
	const fixedSet = useMemo(() => new Set([...fixedLeft, ...fixedRight]), [fixedLeft, fixedRight]);

	const configurableIds = useMemo(() => columnIds.filter((id) => !fixedSet.has(id)), [columnIds, fixedSet]);

	// Merge URL config with available columns (handles added/removed columns)
	const entries = useMemo(() => {
		const parsed = deserialize(raw);
		const result: ColumnConfigEntry[] = [];
		const seen = new Set<string>();

		// Columns present in URL config that still exist
		for (const entry of parsed) {
			if (configurableIds.includes(entry.id) && !seen.has(entry.id)) {
				result.push(entry);
				seen.add(entry.id);
			}
		}

		// New columns not yet in URL config
		for (const id of configurableIds) {
			if (!seen.has(id)) {
				result.push({ id, visible: true, pinned: false });
			}
		}

		return result;
	}, [raw, configurableIds]);

	// TanStack table state
	const columnOrder = useMemo(() => [...fixedLeft, ...entries.map((e) => e.id), ...fixedRight], [entries, fixedLeft, fixedRight]);

	const columnVisibility = useMemo(() => {
		const vis: VisibilityState = {};
		for (const entry of entries) {
			if (!entry.visible) vis[entry.id] = false;
		}
		return vis;
	}, [entries]);

	const columnPinning = useMemo(
		(): ColumnPinningState => ({
			left: [...fixedLeft, ...entries.filter((e) => e.pinned === "left").map((e) => e.id)],
			right: [...entries.filter((e) => e.pinned === "right").map((e) => e.id), ...fixedRight],
		}),
		[entries, fixedLeft, fixedRight],
	);

	const persist = useCallback(
		(newEntries: ColumnConfigEntry[]) => {
			const serialized = serialize(newEntries);
			setRaw(serialized || null);
		},
		[setRaw],
	);

	const toggleVisibility = useCallback(
		(columnId: string) => {
			persist(entries.map((e) => (e.id === columnId ? { ...e, visible: !e.visible } : e)));
		},
		[entries, persist],
	);

	const togglePin = useCallback(
		(columnId: string, position: "left" | "right") => {
			persist(entries.map((e) => (e.id === columnId ? { ...e, pinned: e.pinned === position ? false : position } : e)));
		},
		[entries, persist],
	);

	const reorder = useCallback(
		(newEntries: ColumnConfigEntry[]) => {
			persist(newEntries);
		},
		[persist],
	);

	const reset = useCallback(() => {
		setRaw(null);
	}, [setRaw]);

	return {
		entries,
		columnOrder,
		columnVisibility,
		columnPinning,
		toggleVisibility,
		togglePin,
		reorder,
		reset,
	};
}
