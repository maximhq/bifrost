import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { Switch } from "@/components/ui/switch";
import { getProviderLabel } from "@/lib/constants/logs";
import { getErrorMessage, useCreatePluginMutation, useGetPluginsQuery, useGetProvidersQuery, useUpdatePluginMutation } from "@/lib/store";
import { CacheConfig, EditorCacheConfig, ModelProviderName } from "@/lib/types/config";
import { SEMANTIC_CACHE_PLUGIN } from "@/lib/types/plugins";
import { cacheConfigSchema } from "@/lib/types/schemas";
import { Loader2 } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";

const defaultCacheConfig: EditorCacheConfig = {
	ttl_seconds: 300,
	threshold: 0.8,
	conversation_history_threshold: 3,
	exclude_system_prompt: false,
	cache_by_model: true,
	cache_by_provider: true,
};

const toEditorCacheConfig = (config?: Partial<CacheConfig>): EditorCacheConfig => ({
	...defaultCacheConfig,
	...config,
});

const normalizeCacheConfigForSave = (config: EditorCacheConfig) => {
	const normalized: Record<string, unknown> = {
		ttl_seconds: config.ttl_seconds,
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
	if (config.keys !== undefined) {
		normalized.keys = config.keys;
	}

	const provider = config.provider?.trim();
	const embeddingModel = config.embedding_model?.trim();

	if (provider) {
		normalized.provider = provider;
	}
	if (embeddingModel) {
		normalized.embedding_model = embeddingModel;
	}
	if (config.dimension !== undefined) {
		normalized.dimension = config.dimension;
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

	// Update default provider when providers are loaded (only for new configs)
	useEffect(() => {
		if (providers.length > 0 && !semanticCachePlugin?.config) {
			setCacheConfig((prev) => ({
				...prev,
				provider: providers[0].name as ModelProviderName,
				embedding_model: prev.embedding_model ?? "text-embedding-3-small",
				dimension: prev.dimension ?? 1536,
			}));
		}
	}, [providers, semanticCachePlugin?.config]);

	const hasChanges = useMemo(() => {
		if (originalCacheEnabled !== serverCacheEnabled) return true;

		return (
			cacheConfig.provider !== serverCacheConfig.provider ||
			cacheConfig.embedding_model !== serverCacheConfig.embedding_model ||
			cacheConfig.dimension !== serverCacheConfig.dimension ||
			cacheConfig.ttl_seconds !== serverCacheConfig.ttl_seconds ||
			cacheConfig.threshold !== serverCacheConfig.threshold ||
			cacheConfig.conversation_history_threshold !== serverCacheConfig.conversation_history_threshold ||
			cacheConfig.exclude_system_prompt !== serverCacheConfig.exclude_system_prompt ||
			cacheConfig.cache_by_model !== serverCacheConfig.cache_by_model ||
			cacheConfig.cache_by_provider !== serverCacheConfig.cache_by_provider
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
						</p>
					</div>
					<div className="flex items-center gap-2">
						<Switch
							id="enable-caching"
							size="md"
							checked={originalCacheEnabled && isVectorStoreEnabled}
							disabled={!isVectorStoreEnabled || providersLoading || providers.length === 0}
							onCheckedChange={(checked) => {
								if (isVectorStoreEnabled) {
									handleSemanticCacheToggle(checked);
								}
							}}
						/>
						{(isSemanticCacheEnabled || originalCacheEnabled) && (
							<Button
								onClick={handleSave}
								disabled={!hasChanges || isUpdating || isCreating || hasInvalidProviderBackedDimension}
								size="sm"
							>
								{isUpdating || isCreating ? "Saving..." : "Save"}
							</Button>
						)}
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
							{/* Provider and Model Settings */}
							<div className="space-y-4">
								<h3 className="text-sm font-medium">Provider and Model Settings</h3>
								<div className="grid grid-cols-2 gap-4">
									<div className="space-y-2">
										<Label htmlFor="provider">Configured Providers</Label>
										<Select
											value={cacheConfig.provider}
											onValueChange={(value: ModelProviderName) => updateCacheConfigLocal({ provider: value })}
										>
											<SelectTrigger className="w-full">
												<SelectValue placeholder="Select provider" />
											</SelectTrigger>
											<SelectContent>
												{providers
													.filter((provider) => provider.name)
													.map((provider) => (
														<SelectItem key={provider.name} value={provider.name}>
															{getProviderLabel(provider.name)}
														</SelectItem>
													))}
											</SelectContent>
										</Select>
									</div>
									<div className="space-y-2">
										<Label htmlFor="embedding_model">Embedding Model*</Label>
										<Input
											id="embedding_model"
											placeholder="text-embedding-3-small"
											value={cacheConfig.embedding_model ?? ""}
											onChange={(e) => updateCacheConfigLocal({ embedding_model: e.target.value })}
										/>
									</div>
								</div>
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
											value={cacheConfig.ttl_seconds === undefined || Number.isNaN(cacheConfig.ttl_seconds) ? "" : cacheConfig.ttl_seconds}
											onChange={(e) => {
												const value = e.target.value;
												if (value === "") {
													updateCacheConfigLocal({ ttl_seconds: undefined });
													return;
												}
												const parsed = parseInt(value);
												if (!Number.isNaN(parsed)) {
													updateCacheConfigLocal({ ttl_seconds: parsed });
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
									</div>
								</div>
								<p className="text-muted-foreground text-xs">
									API keys for the embedding provider will be inherited from the main provider configuration. The semantic cache will use
									the configured provider&apos;s keys automatically. <b>Updates in keys will be reflected on Bifrost restart.</b>
								</p>
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
						</div>
					))}
			</div>
		</div>
	);
}