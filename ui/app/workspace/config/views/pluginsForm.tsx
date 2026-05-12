import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ModelMultiselect } from "@/components/ui/modelMultiselect";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { Switch } from "@/components/ui/switch";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { EmbeddingSupportedProviders, getProviderLabel } from "@/lib/constants/logs";
import { getErrorMessage, useCreatePluginMutation, useGetPluginsQuery, useGetProvidersQuery, useUpdatePluginMutation } from "@/lib/store";
import { CacheConfig, EditorCacheConfig, ModelProvider, ModelProviderName } from "@/lib/types/config";
import { SEMANTIC_CACHE_PLUGIN } from "@/lib/types/plugins";
import { cacheConfigSchema } from "@/lib/types/schemas";
import { Loader2 } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";

// Semantic caching needs an embedding-capable provider. Built-in providers are
// gated by EmbeddingSupportedProviders; custom providers expose support via
// custom_provider_config.allowed_requests.embedding.
const supportsEmbedding = (provider: ModelProvider): boolean => {
	if (provider.custom_provider_config) {
		return provider.custom_provider_config.allowed_requests?.embedding === true;
	}
	return (EmbeddingSupportedProviders as readonly string[]).includes(provider.name);
};

const defaultCacheConfig: EditorCacheConfig = {
	ttl: 300,
	threshold: 0.8,
	conversation_history_threshold: 3,
	exclude_system_prompt: false,
	cache_by_model: true,
	cache_by_provider: true,
};

const toEditorCacheConfig = (config?: Partial<CacheConfig> & { ttl_seconds?: number }): EditorCacheConfig => {
	const { ttl_seconds, ...rest } = config ?? {};
	const merged: EditorCacheConfig = { ...defaultCacheConfig, ...rest };
	// Migration: older saves stored TTL under `ttl_seconds`; the Go plugin only
	// reads `ttl`, so adopt the legacy value if the new field isn't present.
	if (rest.ttl === undefined && typeof ttl_seconds === "number") {
		merged.ttl = ttl_seconds;
	}
	return merged;
};

const normalizeCacheConfigForSave = (config: EditorCacheConfig) => {
	const normalized: Record<string, unknown> = {
		ttl: config.ttl,
		threshold: config.threshold,
		cache_by_model: config.cache_by_model,
		cache_by_provider: config.cache_by_provider,
	};

	if (config.conversation_history_threshold !== undefined) {
		normalized.conversation_history_threshold = config.conversation_history_threshold;
	}
	if (config.exclude_system_prompt !== undefined) {
		normalized.exclude_system_prompt = config.exclude_system_prompt;
	}
	if (config.created_at !== undefined) {
		normalized.created_at = config.created_at;
	}
	if (config.updated_at !== undefined) {
		normalized.updated_at = config.updated_at;
	}

	const provider = config.provider?.trim();
	const embeddingModel = config.embedding_model?.trim();
	const namespace = config.vector_store_namespace?.trim();
	const defaultKey = config.default_cache_key?.trim();

	if (provider) {
		normalized.provider = provider;
	}
	if (embeddingModel) {
		normalized.embedding_model = embeddingModel;
	}
	if (config.dimension !== undefined) {
		normalized.dimension = config.dimension;
	}
	if (namespace) {
		normalized.vector_store_namespace = namespace;
	}
	if (defaultKey) {
		normalized.default_cache_key = defaultKey;
	}

	return normalized;
};

interface PluginsFormProps {
	isVectorStoreEnabled: boolean;
}

export default function PluginsForm({ isVectorStoreEnabled }: PluginsFormProps) {
	const [cacheConfig, setCacheConfig] = useState<EditorCacheConfig>(defaultCacheConfig);
	const [originalCacheEnabled, setOriginalCacheEnabled] = useState<boolean>(false);
	const [serverCacheConfig, setServerCacheConfig] = useState<EditorCacheConfig>(defaultCacheConfig);
	const [serverCacheEnabled, setServerCacheEnabled] = useState<boolean>(false);

	const { data: providersData, error: providersError, isLoading: providersLoading } = useGetProvidersQuery();

	const providers = useMemo(() => providersData || [], [providersData]);
	const embeddingProviders = useMemo(() => providers.filter(supportsEmbedding), [providers]);

	useEffect(() => {
		if (providersError) {
			toast.error(`Failed to load providers: ${getErrorMessage(providersError as any)}`);
		}
	}, [providersError]);

	// RTK Query hooks
	const { data: plugins, isLoading: loading } = useGetPluginsQuery();
	const [updatePlugin, { isLoading: isUpdating }] = useUpdatePluginMutation();
	const [createPlugin, { isLoading: isCreating }] = useCreatePluginMutation();

	// Get semantic cache plugin and its config
	const semanticCachePlugin = useMemo(() => plugins?.find((plugin) => plugin.name === SEMANTIC_CACHE_PLUGIN), [plugins]);

	const isSemanticCacheEnabled = Boolean(semanticCachePlugin?.enabled);
	const loadedDirectOnlyConfig = serverCacheConfig.dimension === 1 && !serverCacheConfig.provider;
	const hasInvalidProviderBackedDimension = cacheConfig.dimension === 1 && Boolean(cacheConfig.provider?.trim());

	// Initialize cache config from plugin data
	useEffect(() => {
		if (semanticCachePlugin?.config) {
			const config = toEditorCacheConfig(semanticCachePlugin.config as Partial<CacheConfig>);
			setCacheConfig(config);
			setServerCacheConfig(config);
			setOriginalCacheEnabled(semanticCachePlugin.enabled);
			setServerCacheEnabled(semanticCachePlugin.enabled);
		}
	}, [semanticCachePlugin]);

	// Seed default provider/model/dimension when the providers list loads, but
	// only for new configs that haven't picked a provider yet — re-running this
	// effect on subsequent embeddingProviders changes would otherwise clobber
	// an in-progress user selection.
	useEffect(() => {
		if (embeddingProviders.length > 0 && !semanticCachePlugin?.config) {
			setCacheConfig((prev) => {
				if (prev.provider) return prev;
				return {
					...prev,
					provider: embeddingProviders[0].name as ModelProviderName,
					embedding_model: prev.embedding_model ?? "text-embedding-3-small",
					dimension: prev.dimension ?? 1536,
				};
			});
		}
	}, [embeddingProviders, semanticCachePlugin?.config]);

	const hasChanges = useMemo(() => {
		if (originalCacheEnabled !== serverCacheEnabled) return true;

		return (
			cacheConfig.provider !== serverCacheConfig.provider ||
			cacheConfig.embedding_model !== serverCacheConfig.embedding_model ||
			cacheConfig.dimension !== serverCacheConfig.dimension ||
			cacheConfig.ttl !== serverCacheConfig.ttl ||
			cacheConfig.threshold !== serverCacheConfig.threshold ||
			cacheConfig.conversation_history_threshold !== serverCacheConfig.conversation_history_threshold ||
			cacheConfig.exclude_system_prompt !== serverCacheConfig.exclude_system_prompt ||
			cacheConfig.cache_by_model !== serverCacheConfig.cache_by_model ||
			cacheConfig.cache_by_provider !== serverCacheConfig.cache_by_provider ||
			(cacheConfig.vector_store_namespace ?? "") !== (serverCacheConfig.vector_store_namespace ?? "") ||
			(cacheConfig.default_cache_key ?? "") !== (serverCacheConfig.default_cache_key ?? "")
		);
	}, [cacheConfig, serverCacheConfig, originalCacheEnabled, serverCacheEnabled]);

	// Handle semantic cache toggle (create or update)
	const handleSemanticCacheToggle = (enabled: boolean) => {
		setOriginalCacheEnabled(enabled);
	};

	// Update cache config locally
	const updateCacheConfigLocal = (updates: Partial<EditorCacheConfig>) => {
		setCacheConfig((prev) => ({ ...prev, ...updates }));
	};

	// Save all changes
	const handleSave = async () => {
		if (hasInvalidProviderBackedDimension) {
			toast.error(
				"Provider-backed semantic cache requires the embedding model's real dimension. Use a value greater than 1, or remove the provider to keep direct-only mode.",
			);
			return;
		}

		const parseResult = cacheConfigSchema.safeParse(normalizeCacheConfigForSave(cacheConfig));
		if (!parseResult.success) {
			const firstIssue = parseResult.error.issues[0]?.message ?? "Semantic cache configuration is invalid.";
			toast.error(firstIssue);
			return;
		}

		const savedConfig = parseResult.data as CacheConfig;

		try {
			if (semanticCachePlugin) {
				// Update existing plugin
				await updatePlugin({
					name: SEMANTIC_CACHE_PLUGIN,
					data: { enabled: originalCacheEnabled, config: savedConfig },
				}).unwrap();
			} else {
				// Create new plugin
				await createPlugin({
					name: SEMANTIC_CACHE_PLUGIN,
					enabled: originalCacheEnabled,
					config: savedConfig,
					path: "",
				}).unwrap();
			}
			toast.success("Plugin configuration updated successfully");
			// Update server state to match current state
			const normalizedConfig = toEditorCacheConfig(savedConfig);
			setCacheConfig(normalizedConfig);
			setServerCacheConfig(normalizedConfig);
			setServerCacheEnabled(originalCacheEnabled);
		} catch (error) {
			const errorMessage = getErrorMessage(error);
			toast.error(`Failed to update plugin configuration: ${errorMessage}`);
		}
	};

	if (loading) {
		return (
			<Card>
				<CardContent className="p-6">
					<div className="text-muted-foreground">Loading plugins configuration...</div>
				</CardContent>
			</Card>
		);
	}

	return (
		<div className="space-y-6">
			{/* Semantic Cache Toggle */}
			<div className="rounded-lg border p-4">
				<div className="flex items-center justify-between space-x-2">
					<div className="flex-1 space-y-0.5">
						<label htmlFor="enable-caching" className="text-sm font-medium">
							Enable Semantic Caching
						</label>
						<p className="text-muted-foreground text-sm">
							Enable semantic caching for requests. Send <b>x-bf-cache-key</b> header with requests to use semantic caching.{" "}
							{!isVectorStoreEnabled && (
								<span className="text-destructive font-medium">Requires vector store to be configured and enabled in config.json.</span>
							)}
							{!providersLoading && providers?.length === 0 && (
								<span className="text-destructive font-medium"> Requires at least one provider to be configured.</span>
							)}
							{!providersLoading && providers.length > 0 && embeddingProviders.length === 0 && (
								<span className="text-destructive font-medium">
									{" "}
									Requires at least one provider that supports embedding requests. Configure a built-in embedding provider, or enable the
									<code className="mx-1">embedding</code>request type on a custom provider.
								</span>
							)}
						</p>
					</div>
					<div className="flex items-center gap-2">
						<Switch
							id="enable-caching"
							size="md"
							checked={originalCacheEnabled && isVectorStoreEnabled}
							disabled={!isVectorStoreEnabled || providersLoading || embeddingProviders.length === 0}
							onCheckedChange={(checked) => {
								if (isVectorStoreEnabled) {
									handleSemanticCacheToggle(checked);
								}
							}}
						/>
					</div>
				</div>

				{/* Cache Configuration (only show when enabled) */}
				{originalCacheEnabled &&
					isVectorStoreEnabled &&
					(providersLoading ? (
						<div className="flex items-center justify-center">
							<Loader2 className="h-4 w-4 animate-spin" />
						</div>
					) : (
						<div className="mt-4 space-y-4">
							<Separator />
							{loadedDirectOnlyConfig && (
								<div className="rounded-md border border-amber-200 bg-amber-50 p-3 text-xs text-amber-900">
									This plugin was loaded in direct-only mode via <code>config.json</code>. The Web UI currently edits provider-backed
									semantic cache settings; keep using <code>config.json</code> if you want to stay in direct-only mode.
								</div>
							)}
							{hasInvalidProviderBackedDimension && (
								<div className="rounded-md border border-red-200 bg-red-50 p-3 text-xs text-red-900">
									You selected a provider while keeping <code>dimension: 1</code>. That is only valid for direct-only mode. Set the
									embedding model&apos;s real dimension before saving, or remove the provider to stay in direct-only mode.
								</div>
							)}
							<div className="rounded-md border border-amber-200 bg-amber-50 p-3 text-xs text-amber-900">
								<b>Heads up:</b> a vector store namespace can only hold vectors of <em>one</em> dimension. Whenever you
								change the embedding <b>provider</b>, <b>model</b>, or <b>dimension</b>, make sure the <b>dimension</b> still matches what the model produces - otherwise writes to the existing namespace will
								fail and reads will silently miss. The namespace is <em>not</em> recreated automatically; either use a fresh namespace or drop the existing class/index in your vector store
								before saving.
							</div>
							{/* Provider and Model Settings */}
							<div className="space-y-4">
								<h3 className="text-sm font-medium">Provider and Model Settings</h3>
								<div className="grid grid-cols-2 gap-4">
									<div className="space-y-2">
										<Label htmlFor="provider">Configured Providers</Label>
										<Select
											value={cacheConfig.provider}
											onValueChange={(value: ModelProviderName) =>
												updateCacheConfigLocal({
													provider: value,
													embedding_model: value === cacheConfig.provider ? cacheConfig.embedding_model : "",
												})
											}
										>
											<SelectTrigger className="w-full">
												<SelectValue placeholder="Select provider" />
											</SelectTrigger>
											<SelectContent>
												{embeddingProviders
													.filter((provider) => provider.name)
													.map((provider) => (
														<SelectItem key={provider.name} value={provider.name}>
															<div className="flex items-center gap-2">
																<RenderProviderIcon provider={provider.name as ProviderIconType} size="sm" className="h-4 w-4" />
																<span>{getProviderLabel(provider.name)}</span>
															</div>
														</SelectItem>
													))}
											</SelectContent>
										</Select>
									</div>
									<div className="space-y-2">
										<Label htmlFor="embedding_model">Embedding Model*</Label>
										<ModelMultiselect
											inputId="embedding_model"
											isSingleSelect
											provider={cacheConfig.provider || undefined}
											value={cacheConfig.embedding_model ?? ""}
											onChange={(model) => updateCacheConfigLocal({ embedding_model: model })}
											placeholder={cacheConfig.provider ? "Search or type an embedding model..." : "Select a provider first"}
											disabled={!cacheConfig.provider}
										/>
									</div>
								</div>
								<p className="text-muted-foreground text-xs">
									API keys for the embedding provider will be inherited from the main provider configuration. The semantic cache will use
									the configured provider&apos;s keys automatically.
								</p>
							</div>

							{/* Cache Settings */}
							<div className="space-y-4">
								<h3 className="text-sm font-medium">Cache Settings</h3>
								<div className="grid grid-cols-2 gap-4">
									<div className="space-y-2">
										<Label htmlFor="ttl">TTL (seconds)</Label>
										<Input
											id="ttl"
											type="number"
											min="1"
											value={cacheConfig.ttl === undefined || Number.isNaN(cacheConfig.ttl) ? "" : cacheConfig.ttl}
											onChange={(e) => {
												const value = e.target.value;
												if (value === "") {
													updateCacheConfigLocal({ ttl: undefined });
													return;
												}
												const parsed = parseInt(value);
												if (!Number.isNaN(parsed)) {
													updateCacheConfigLocal({ ttl: parsed });
												}
											}}
										/>
									</div>
									<div className="space-y-2">
										<Label htmlFor="threshold">Similarity Threshold</Label>
										<Input
											id="threshold"
											type="number"
											min="0"
											max="1"
											step="0.01"
											value={cacheConfig.threshold === undefined || Number.isNaN(cacheConfig.threshold) ? "" : cacheConfig.threshold}
											onChange={(e) => {
												const value = e.target.value;
												if (value === "") {
													updateCacheConfigLocal({ threshold: undefined });
													return;
												}
												const parsed = parseFloat(value);
												if (!Number.isNaN(parsed)) {
													updateCacheConfigLocal({ threshold: parsed });
												}
											}}
										/>
									</div>
									<div className="space-y-2">
										<Label htmlFor="dimension">Dimension</Label>
										<Input
											id="dimension"
											type="number"
											min="1"
											value={cacheConfig.dimension === undefined || Number.isNaN(cacheConfig.dimension) ? "" : cacheConfig.dimension}
											onChange={(e) => {
												const value = e.target.value;
												if (value === "") {
													updateCacheConfigLocal({ dimension: undefined });
													return;
												}
												const parsed = parseInt(value);
												if (!Number.isNaN(parsed)) {
													updateCacheConfigLocal({ dimension: parsed });
												}
											}}
										/>
										<p className="text-muted-foreground text-xs">
											Vector size produced by the embedding model - must match the model exactly (e.g. <code>1536</code> for
											OpenAI <code>text-embedding-3-small</code>, <code>3072</code> for <code>text-embedding-3-large</code>,
											<code>768</code> for many Cohere/Voyage models). Use <code>1</code> only in direct-only mode (no provider).
										</p>
									</div>
								</div>
							</div>

							{/* Storage & Cache Key */}
							<div className="space-y-4">
								<h3 className="text-sm font-medium">Storage & Cache Key</h3>
								<div className="grid grid-cols-2 gap-4">
									<div className="space-y-2">
										<Label htmlFor="vector_store_namespace">Vector Store Namespace</Label>
										<Input
											id="vector_store_namespace"
											type="text"
											placeholder="BifrostSemanticCachePlugin"
											value={cacheConfig.vector_store_namespace ?? ""}
											onChange={(e) => updateCacheConfigLocal({ vector_store_namespace: e.target.value })}
										/>
										<p className="text-muted-foreground text-xs">
											Bucket/index name where cache entries are stored in the vector store. Leave blank to use the default
											(<code>BifrostSemanticCachePlugin</code>). Changing the namespace points the plugin at a different (possibly empty) bucket. All previously
											cached entries become inaccessible - every request will miss until the new namespace is repopulated.
										</p>
									</div>
									<div className="space-y-2">
										<Label htmlFor="default_cache_key">Default Cache Key</Label>
										<Input
											id="default_cache_key"
											type="text"
											placeholder="(none)"
											value={cacheConfig.default_cache_key ?? ""}
											onChange={(e) => updateCacheConfigLocal({ default_cache_key: e.target.value })}
										/>
										<p className="text-muted-foreground text-xs">
											Fallback value used as the cache partition when a request doesn&apos;t set the <b>x-bf-cache-key</b> header.
											Cache keys isolate entries: requests that share a key can hit each other&apos;s cached responses, while requests
											with different keys can&apos;t. Leaving this blank means caching is <b>disabled</b> for any request that doesn&apos;t
											send the header.
										</p>
									</div>
								</div>
							</div>

							{/* Conversation Settings */}
							<div className="space-y-4">
								<h3 className="text-sm font-medium">Conversation Settings</h3>
								<div className="grid grid-cols-2 gap-4">
									<div className="space-y-2">
										<Label htmlFor="conversation_history_threshold">Conversation History Threshold</Label>
										<Input
											id="conversation_history_threshold"
											type="number"
											min="1"
											max="50"
											value={cacheConfig.conversation_history_threshold || 3}
											onChange={(e) => updateCacheConfigLocal({ conversation_history_threshold: parseInt(e.target.value) || 3 })}
										/>
										<p className="text-muted-foreground text-xs">
											Skip caching for conversations with more than this number of messages (prevents false positives)
										</p>
									</div>
								</div>
								<div className="space-y-2">
									<div className="flex h-fit items-center justify-between space-x-2 rounded-lg border p-3">
										<div className="space-y-0.5">
											<Label className="text-sm font-medium">Exclude System Prompt</Label>
											<p className="text-muted-foreground text-xs">Exclude system messages from cache key generation</p>
										</div>
										<Switch
											checked={cacheConfig.exclude_system_prompt || false}
											onCheckedChange={(checked) => updateCacheConfigLocal({ exclude_system_prompt: checked })}
											size="md"
										/>
									</div>
								</div>
							</div>

							{/* Cache Behavior */}
							<div className="space-y-4">
								<h3 className="text-sm font-medium">Cache Behavior</h3>
								<div className="space-y-3">
									<div className="flex items-center justify-between space-x-2 rounded-lg border p-3">
										<div className="space-y-0.5">
											<Label className="text-sm font-medium">Cache by Model</Label>
											<p className="text-muted-foreground text-xs">Include model name in cache key</p>
										</div>
										<Switch
											checked={cacheConfig.cache_by_model}
											onCheckedChange={(checked) => updateCacheConfigLocal({ cache_by_model: checked })}
											size="md"
										/>
									</div>
									<div className="flex items-center justify-between space-x-2 rounded-lg border p-3">
										<div className="space-y-0.5">
											<Label className="text-sm font-medium">Cache by Provider</Label>
											<p className="text-muted-foreground text-xs">Include provider name in cache key</p>
										</div>
										<Switch
											checked={cacheConfig.cache_by_provider}
											onCheckedChange={(checked) => updateCacheConfigLocal({ cache_by_provider: checked })}
											size="md"
										/>
									</div>
								</div>
							</div>

							<div className="space-y-2">
								<Label className="text-sm font-medium">Notes</Label>
								<ul className="text-muted-foreground list-inside list-disc text-xs">
									<li>
										You can pass <b>x-bf-cache-ttl</b> header with requests to use request-specific TTL.
									</li>
									<li>
										You can pass <b>x-bf-cache-threshold</b> header with requests to use request-specific similarity threshold.
									</li>
									<li>
										You can pass <b>x-bf-cache-type</b> header with &quot;direct&quot; or &quot;semantic&quot; to control cache behavior.
									</li>
									<li>
										You can pass <b>x-bf-cache-no-store</b> header with &quot;true&quot; to disable response caching.
									</li>
								</ul>
							</div>

							<div className="flex justify-end pt-2">
								<Button
									onClick={handleSave}
									disabled={!hasChanges || isUpdating || isCreating || hasInvalidProviderBackedDimension}
								>
									{isUpdating || isCreating ? "Saving..." : "Save Changes"}
								</Button>
							</div>
						</div>
					))}
			</div>
		</div>
	);
}