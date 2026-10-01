import { EmbeddingSupportedProviders } from "@/lib/constants/logs";
import type { ModelProvider } from "@/lib/types/config";
import type { WarpConfig, WarpConfigInput } from "@/lib/types/warp";
import { v4 as uuid } from "uuid";

export interface WarpEmbeddingFields {
	embeddingProvider: string;
	embeddingModel: string;
	embeddingDimension: number;
	namespace: string;
	threshold: number;
	searchLimit: number;
}

export const supportsWarpEmbedding = (provider: ModelProvider): boolean => {
	if (provider.custom_provider_config) {
		return provider.custom_provider_config.allowed_requests?.embedding === true;
	}
	return (EmbeddingSupportedProviders as readonly string[]).includes(provider.name);
};

export const validateWarpEmbedding = (fields: WarpEmbeddingFields, enabled: boolean, vectorStoreConnected: boolean): string | null => {
	if (!enabled) return null;
	if (!vectorStoreConnected) return "Connect a vector store before enabling Warp.";
	if (!fields.embeddingProvider) return "Choose an embedding provider.";
	if (!fields.embeddingModel.trim()) return "Choose an embedding model.";
	if (fields.embeddingDimension <= 0) return "Embedding dimension must be positive.";
	if (!fields.namespace.trim()) return "Vector store namespace is required.";
	if (fields.threshold <= 0 || fields.threshold > 1) return "Similarity threshold must be greater than 0 and at most 1.";
	if (fields.searchLimit < 1 || fields.searchLimit > 25) return "Search limit must be between 1 and 25.";
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
			if (index === 0) return enabled ? "Choose a provider and model to enable Warp." : null;
			return "Choose a provider and model, or remove this model.";
		}
		// The pair is how a question names its model, so a repeat would be ambiguous between two keys.
		const pair = `${provider}\u0000${model}`;
		if (seen.has(pair)) return "This provider and model is already listed.";
		seen.add(pair);
		return null;
	});
};
