import { HeadersTable } from "@/components/ui/headersTable";
import type { SecretVar } from "@/lib/types/mcp";
import { useRef } from "react";
import type { FieldError } from "react-hook-form";

type HeaderPair<T> = { name: string; value: T };
type PairErrors = { name?: FieldError; value?: FieldError } | undefined;

/**
 * The pairs the form gets from the table's headers: a row the table holds as a conflict (a duplicate
 * or a second cleared name) carries the name typed there, so the form's validation sees it and blocks
 * the save, instead of silently saving the row's previous name.
 */
export function pairsFromRecord<T>(record: Record<string, T>, conflicts: ReadonlyMap<number, string>): HeaderPair<T>[] {
	return Object.entries(record).map(([name, value], i) => ({ name: conflicts.get(i) ?? name, value }));
}

/** The headers the table shows for the form's pairs: a held row keeps its last valid name. */
export function recordFromPairs<T>(pairs: HeaderPair<T>[], originals: ReadonlyMap<number, string>): Record<string, T> {
	return Object.fromEntries(pairs.map((pair, i) => [originals.get(i) ?? pair.name, pair.value]));
}

/**
 * The conflicts to hand the form, and the last valid name each held row shows, read from the headers
 * the table's indices refer to (after a delete, the remaining rows, so nothing is matched to a stale
 * row). A conflict on the empty add row is dropped: that row has nothing to save yet.
 */
export function heldConflicts<T>(
	conflicts: ReadonlyMap<number, string>,
	headers: Record<string, T>,
): { conflicts: Map<number, string>; originals: Map<number, string> } {
	const names = Object.keys(headers);
	const held = new Map<number, string>();
	const originals = new Map<number, string>();
	conflicts.forEach((typed, i) => {
		if (i >= names.length) return;
		held.set(i, typed);
		originals.set(i, names[i]);
	});
	return { conflicts: held, originals };
}

/**
 * The relay forms keep headers as name/value pairs (so the schema can flag a half-filled row);
 * this edits them through the shared headers table and lists the per-row errors below it.
 */
export function HeaderPairsTable<T extends string | SecretVar>({
	value,
	onChange,
	errors,
	keyPlaceholder,
	valuePlaceholder,
	disabled,
	useSecretVarInput,
	testIdPrefix,
}: {
	value: HeaderPair<T>[];
	onChange: (pairs: HeaderPair<T>[]) => void;
	errors?: unknown;
	keyPlaceholder?: string;
	valuePlaceholder?: string;
	disabled?: boolean;
	useSecretVarInput?: boolean;
	testIdPrefix?: string;
}) {
	// Rows the table holds as conflicts: the name typed there, and the last valid name it still shows.
	// Refs, not state: the table reports a conflict change and then the edit within one event.
	const conflicts = useRef<ReadonlyMap<number, string>>(new Map());
	const originals = useRef<ReadonlyMap<number, string>>(new Map());
	const emitted = useRef<string | undefined>(undefined);
	// A value this adapter did not produce (the form was reset or reloaded) starts with nothing held.
	if (conflicts.current.size > 0 && JSON.stringify(value) !== emitted.current) {
		conflicts.current = new Map();
		originals.current = new Map();
	}
	const record = recordFromPairs(value, originals.current);
	const emit = (next: Record<string, T>) => {
		const pairs = pairsFromRecord(next, conflicts.current);
		emitted.current = JSON.stringify(pairs);
		onChange(pairs);
	};

	const messages = Array.isArray(errors)
		? [...new Set((errors as PairErrors[]).flatMap((e) => [e?.name?.message, e?.value?.message]).filter((m): m is string => !!m))]
		: [];
	return (
		<div className="space-y-2">
			<HeadersTable<T>
				label=""
				value={record}
				onChange={emit}
				onConflictsChange={(next, headers) => {
					const held = heldConflicts(next, headers);
					conflicts.current = held.conflicts;
					originals.current = held.originals;
					emit(headers);
				}}
				keyPlaceholder={keyPlaceholder}
				valuePlaceholder={valuePlaceholder}
				disabled={disabled}
				useSecretVarInput={useSecretVarInput}
				testIdPrefix={testIdPrefix}
				// A cleared name keeps its row and value, so the form flags it rather than saving the
				// relay without that header (or a route without that matcher, which would widen it).
				keepClearedNames
			/>
			{messages.map((m) => (
				<p key={m} className="text-destructive text-sm">
					{m}
				</p>
			))}
		</div>
	);
}