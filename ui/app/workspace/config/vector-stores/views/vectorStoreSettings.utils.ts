import type { UpdateVectorStorePayload, VectorStoreInfo } from "@/lib/types/config";
import { vectorMaxSizeMBSchema, vectorRetentionDaysSchema } from "@/lib/types/schemas";
import type { z } from "zod";

// The two inputs of one store card, as typed.
export interface VectorStoreDraft {
	retentionDays: string;
	maxSizeMB: string;
}

// A stored 0 renders as a blank field, since blank means 0.
export const toFieldText = (value: number): string => (value ? String(value) : "");

// Blank is 0. Anything else is read whole, so "1.5" stays a fraction (and fails
// validation) instead of being cut to its leading integer, and junk reads as NaN.
export const parseFieldText = (text: string): number => {
	const raw = text.trim();
	return raw === "" ? 0 : Number(raw);
};

export const draftFromStore = (store: Pick<VectorStoreInfo, "retention_days" | "max_size_mb">): VectorStoreDraft => ({
	retentionDays: toFieldText(store.retention_days),
	maxSizeMB: toFieldText(store.max_size_mb),
});

const fieldError = (schema: z.ZodType<number>, text: string): string | null => {
	const result = schema.safeParse(parseFieldText(text));
	return result.success ? null : (result.error.issues[0]?.message ?? null);
};

// The inline error for each field, or null. Messages resolve in the UI's language at call time.
export const retentionDaysError = (text: string): string | null => fieldError(vectorRetentionDaysSchema, text);
export const maxSizeMBError = (text: string): string | null => fieldError(vectorMaxSizeMBSchema, text);

// The PUT body for a store: only the fields that changed, blank sent as 0.
// Null when nothing changed or a field is invalid, so there is nothing to save.
export const buildVectorStoreUpdate = (
	store: Pick<VectorStoreInfo, "retention_days" | "max_size_mb">,
	draft: VectorStoreDraft,
): UpdateVectorStorePayload | null => {
	if (retentionDaysError(draft.retentionDays) || maxSizeMBError(draft.maxSizeMB)) {
		return null;
	}
	const payload: UpdateVectorStorePayload = {};
	const retentionDays = parseFieldText(draft.retentionDays);
	if (retentionDays !== store.retention_days) {
		payload.retention_days = retentionDays;
	}
	const maxSizeMB = parseFieldText(draft.maxSizeMB);
	if (maxSizeMB !== store.max_size_mb) {
		payload.max_size_mb = maxSizeMB;
	}
	return Object.keys(payload).length > 0 ? payload : null;
};

export type StoredVectorStoreValues = Pick<VectorStoreInfo, "retention_days" | "max_size_mb">;

export const sameDraft = (a: VectorStoreDraft, b: VectorStoreDraft) => a.retentionDays === b.retentionDays && a.maxSizeMB === b.maxSizeMB;

// Whether a store's stored values are the ones the last read held.
export const storedValuesUnchanged = (before: StoredVectorStoreValues | undefined, store: StoredVectorStoreValues): boolean =>
	!!before && before.retention_days === store.retention_days && before.max_size_mb === store.max_size_mb;

// The drafts once the stored values are read again, store by store: a store whose stored values
// did not change keeps what is being typed into it, and one whose values did change takes them,
// unless it was edited after its save was sent, which keeps that edit. stored is what the last
// read held and submitted the draft each save sent. Everything is keyed by Map, since a store's
// name is free-form and may be "constructor".
export const followStoredValues = (
	stores: ReadonlyArray<StoredVectorStoreValues & Pick<VectorStoreInfo, "name">>,
	drafts: ReadonlyMap<string, VectorStoreDraft>,
	stored: ReadonlyMap<string, StoredVectorStoreValues>,
	submitted: ReadonlyMap<string, VectorStoreDraft>,
): Map<string, VectorStoreDraft> => {
	const next = new Map<string, VectorStoreDraft>();
	for (const store of stores) {
		const draft = drafts.get(store.name);
		const sent = submitted.get(store.name);
		const editedSinceSave = !!sent && !!draft && !sameDraft(draft, sent);
		next.set(
			store.name,
			(storedValuesUnchanged(stored.get(store.name), store) || editedSinceSave) && draft ? draft : draftFromStore(store),
		);
	}
	return next;
};