import { EmbeddingSupportedProviders } from "@/lib/constants/logs";
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
	if (!vectorStoreConnected) return "Connect a vector store before enabling Warp.";
	if (!fields.embeddingProvider) return "Choose an embedding provider.";
	if (!fields.embeddingModel.trim()) return "Choose an embedding model.";
	if (fields.embeddingDimension <= 0) return "Embedding dimension must be positive.";
	if (!fields.namespace.trim()) return "Vector store namespace is required.";
	if (fields.threshold <= 0 || fields.threshold > 1) return "Similarity threshold must be greater than 0 and at most 1.";
	if (fields.searchLimit < 1 || fields.searchLimit > 25) return "Search limit must be between 1 and 25.";
	return null;
};

export const embeddingSpaceChanged = (current: WarpEmbeddingFields, saved: WarpEmbeddingFields): boolean =>
	current.embeddingProvider !== saved.embeddingProvider ||
	current.embeddingModel !== saved.embeddingModel ||
	current.embeddingDimension !== saved.embeddingDimension;