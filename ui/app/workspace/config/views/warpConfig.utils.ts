import { EmbeddingSupportedProviders } from "@/lib/constants/logs";
import i18n from "@/lib/i18n";
import type { ModelProvider } from "@/lib/types/config";
import {
	WARP_MANAGEMENT_WRITES,
	type WarpConfig,
	type WarpConfigInput,
	type WarpManagementToolSize,
	type WarpManagementWrite,
} from "@/lib/types/warp";
import { v4 as uuid } from "uuid";

export interface WarpEmbeddingFields {
	embeddingProvider: string;
	embeddingModel: string;
	embeddingDimension: number;
	namespace: string;
	threshold: number;
	searchLimit: number;
}

// supportsWarpEmbedding reports a provider that can serve Warp's embeddings: its
// type (a custom provider's base) is listed in EmbeddingSupportedProviders, and a
// custom provider's allowed_requests allows embedding. A custom provider with no
// allowed_requests block at all is unrestricted, as the gateway reads it.
export const supportsWarpEmbedding = (provider: ModelProvider): boolean => {
	const providerType = provider.custom_provider_config?.base_provider_type ?? provider.name;
	if (!(EmbeddingSupportedProviders as readonly string[]).includes(providerType)) return false;
	const allowed = provider.custom_provider_config?.allowed_requests;
	return !allowed || allowed.embedding === true;
};

export const validateWarpEmbedding = (fields: WarpEmbeddingFields, enabled: boolean, vectorStoreConnected: boolean): string | null => {
	if (!enabled) return null;
	if (!vectorStoreConnected) return i18n.t("warp.validation.vectorStore", { ns: "config" });
	if (!fields.embeddingProvider) return i18n.t("warp.validation.embeddingProvider", { ns: "config" });
	if (!fields.embeddingModel.trim()) return i18n.t("warp.validation.embeddingModel", { ns: "config" });
	if (fields.embeddingDimension <= 0) return i18n.t("warp.validation.dimension", { ns: "config" });
	if (!fields.namespace.trim()) return i18n.t("warp.validation.namespace", { ns: "config" });
	if (fields.threshold <= 0 || fields.threshold > 1) return i18n.t("warp.validation.threshold", { ns: "config" });
	if (fields.searchLimit < 1 || fields.searchLimit > 25) return i18n.t("warp.validation.searchLimit", { ns: "config" });
	return null;
};

/**
 * The namespace as the save path sends it. The dirty check compared the raw
 * form value while onSubmit sent `.trim()`, so typing a space after the saved
 * name satisfied "you must rename the namespace" and then submitted the old
 * name anyway. Mirrors normalizedNamespace on the server.
 */
export const normalizeWarpNamespace = (value: string): string => value.trim();

export const embeddingSpaceChanged = (current: WarpEmbeddingFields, saved: WarpEmbeddingFields): boolean => {
	// A space that was never saved cannot have changed. The rename is demanded
	// because switching spaces invalidates everything already indexed under the
	// old one - and before the first save there is nothing indexed, so applying
	// the rule there just blocks the initial setup on a rule about data that
	// does not exist. Mirrors the same guard on the server.
	if (!saved.embeddingProvider || !saved.embeddingModel || saved.embeddingDimension <= 0) return false;
	// Trimmed on both sides, because onSubmit sends `embedding_model.trim()` and
	// `embedding_provider.trim()`. Comparing the raw field made " model-a " read
	// as a different model from "model-a", so the namespace guard demanded a
	// rename for a payload that carried the identical space - the same mistake
	// the namespace comparison itself had, one field over.
	return (
		current.embeddingProvider.trim() !== saved.embeddingProvider.trim() ||
		current.embeddingModel.trim() !== saved.embeddingModel.trim() ||
		current.embeddingDimension !== saved.embeddingDimension
	);
};

/** Mirrors schemas.WarpMaxAdditionalModels. */
export const WARP_MAX_ADDITIONAL_MODELS = 20;

/**
 * One model in the settings form. The first row is Warp's default; the rest are
 * the additional models the panel may switch to. `id` only keys the row in
 * React and never leaves the form.
 */
export interface WarpModelRow {
	id: string;
	provider: string;
	model: string;
	apiKeyID: string;
}

export const newWarpModelRow = (): WarpModelRow => ({ id: uuid(), provider: "", model: "", apiKeyID: "" });

type WarpModelFields = Pick<WarpConfig, "provider" | "model" | "api_key_id" | "additional_models">;
type WarpModelValues = Omit<WarpModelRow, "id">;

/** The default first, blank if unset, then the additional models. */
const storedWarpModels = (config: WarpModelFields): WarpModelValues[] =>
	[{ provider: config.provider ?? "", model: config.model ?? "", api_key_id: config.api_key_id }, ...(config.additional_models ?? [])].map(
		(model) => ({ provider: model.provider, model: model.model, apiKeyID: model.api_key_id ?? "" }),
	);

/** The form's rows for a stored config: always the default row, then one per additional model. */
export const warpModelRowsFromConfig = (config: WarpModelFields): WarpModelRow[] =>
	storedWarpModels(config).map((model) => ({ id: uuid(), ...model }));

/** The rows as the write body carries them: the first is the default, the rest additional_models. */
export const warpModelsPayload = (
	rows: WarpModelValues[],
): Pick<WarpConfigInput, "provider" | "model" | "api_key_id" | "additional_models"> => {
	const [first, ...rest] = rows;
	return {
		provider: first?.provider.trim() ?? "",
		model: first?.model.trim() ?? "",
		api_key_id: first?.apiKeyID ?? "",
		additional_models: rest.map((row) => ({
			provider: row.provider.trim(),
			model: row.model.trim(),
			// Omitted rather than "", matching how the server returns an unpinned entry.
			...(row.apiKeyID ? { api_key_id: row.apiKeyID } : {}),
		})),
	};
};

/** Whether the rows differ from what is stored. Compared as the payload, so row ids and padding never read as an edit. */
export const warpModelRowsChanged = (rows: WarpModelValues[], config: WarpModelFields): boolean =>
	JSON.stringify(warpModelsPayload(rows)) !== JSON.stringify(warpModelsPayload(storedWarpModels(config)));

/**
 * One message per row, null where the row is fine. Mirrors validateAdditionalModels on the server.
 *
 * The default may be left blank while Warp is off, since the form can be filled
 * in over several sittings. An additional row is never a draft of anything: it
 * is either a complete choice or it should not be in the list.
 */
export const validateWarpModelRows = (rows: WarpModelValues[], enabled: boolean): (string | null)[] => {
	const seen = new Set<string>();
	return rows.map((row, index) => {
		const provider = row.provider.trim();
		const model = row.model.trim();
		if (!provider || !model) {
			if (index === 0) return enabled ? i18n.t("warp.validation.modelRequired", { ns: "config" }) : null;
			return i18n.t("warp.validation.modelOrRemove", { ns: "config" });
		}
		// The pair is how a question names its model, so a repeat would be ambiguous between two keys.
		const pair = `${provider}\u0000${model}`;
		if (seen.has(pair)) return i18n.t("warp.validation.modelDuplicate", { ns: "config" });
		seen.add(pair);
		return null;
	});
};
/** Turns one kind of configuration change on or off, keeping WARP_MANAGEMENT_WRITES order so equal sets compare equal. */
export const setWarpManagementWrite = (
	writes: readonly WarpManagementWrite[],
	kind: WarpManagementWrite,
	on: boolean,
): WarpManagementWrite[] => WARP_MANAGEMENT_WRITES.filter((candidate) => (candidate === kind ? on : writes.includes(candidate)));

/** Whether the chosen kinds differ from the saved ones, order aside. */
export const warpManagementWritesChanged = (
	current: readonly WarpManagementWrite[],
	saved: readonly WarpManagementWrite[] | undefined,
): boolean => {
	const stored = saved ?? [];
	return current.length !== stored.length || current.some((kind) => !stored.includes(kind));
};

/**
 * Roughly how many tokens the management tools add to every Warp request with
 * these kinds on: reads always, plus each chosen kind. Null while the sizes are
 * unknown (the Bifrost MCP is not loaded).
 */
export const warpManagementTokens = (
	sizes: readonly WarpManagementToolSize[] | undefined,
	writes: readonly WarpManagementWrite[],
): number | null => {
	if (!sizes || sizes.length === 0) return null;
	return sizes.reduce((total, size) => (size.kind === "read" || writes.includes(size.kind) ? total + size.estimated_tokens : total), 0);
};
/**
 * Whether a configuration-tool switch is locked. Without access every switch is.
 * Otherwise only turning a kind on needs the Bifrost MCP to serve writes and the
 * kind to exist in its catalog: a kind that is on can always be turned off, so a
 * deployment whose writes were switched off at the ceiling can still clear it.
 */
export const warpManagementWriteSwitchDisabled = ({
	disabled,
	checked,
	writesAvailable,
	loaded,
	offered,
}: {
	disabled: boolean;
	checked: boolean;
	writesAvailable: boolean;
	loaded: boolean;
	offered: boolean;
}): boolean => disabled || (!checked && (!writesAvailable || (loaded && !offered)));