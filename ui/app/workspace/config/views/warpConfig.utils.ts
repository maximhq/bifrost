import { EmbeddingSupportedProviders } from "@/lib/constants/logs";
import i18n from "@/lib/i18n";
import type { ModelProvider } from "@/lib/types/config";

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