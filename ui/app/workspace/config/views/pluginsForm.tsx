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
import { useTranslation } from "react-i18next";
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
	const { t } = useTranslation();
	const [cacheConfig, setCacheConfig] = useState<EditorCacheConfig>(defaultCacheConfig);
	const [originalCacheEnabled, setOriginalCacheEnabled] = useState<boolean>(false);
	const [serverCacheConfig, setServerCacheConfig] = useState<EditorCacheConfig>(defaultCacheConfig);
	const [serverCacheEnabled, setServerCacheEnabled] = useState<boolean>(false);

	const { data: providersData, error: providersError, isLoading: providersLoading } = useGetProvidersQuery();

	const providers = useMemo(() => providersData || [], [providersData]);

	useEffect(() => {
		if (providersError) {
			toast.error(t("workspace.config.caching.failedToLoadProviders", { error: getErrorMessage(providersError as any) }));
		}
	}, [providersError, t]);

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
			toast.error(t("workspace.config.caching.providerBackedDimensionError"));
			return;
		}

		const parseResult = cacheConfigSchema.safeParse(normalizeCacheConfigForSave(cacheConfig));
		if (!parseResult.success) {
			const firstIssue = parseResult.error.issues[0]?.message ?? t("workspace.config.caching.invalidConfig");
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
			toast.success(t("workspace.config.caching.pluginConfigUpdated"));
			// Update server state to match current state
			const normalizedConfig = toEditorCacheConfig(savedConfig);
			setCacheConfig(normalizedConfig);
			setServerCacheConfig(normalizedConfig);
			setServerCacheEnabled(originalCacheEnabled);
		} catch (error) {
			const errorMessage = getErrorMessage(error);
			toast.error(t("workspace.config.caching.pluginConfigUpdateFailed", { error: errorMessage }));
		}
	};

	if (loading) {
		return (
			<Card>
				<CardContent className="p-6">
					<div className="text-muted-foreground">{t("workspace.config.caching.loadingPluginsConfiguration")}</div>
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
							{t("workspace.config.caching.enableSemanticCaching")}
						</label>
						<p className="text-muted-foreground text-sm">
							{t("workspace.config.caching.enableSemanticCachingDescPrefix")} <b>x-bf-cache-key</b>{" "}
							{t("workspace.config.caching.enableSemanticCachingDescSuffix")}{" "}
							{!isVectorStoreEnabled && (
								<span className="text-destructive font-medium">{t("workspace.config.caching.vectorStoreRequired")}</span>
							)}
							{!providersLoading && providers?.length === 0 && (
								<span className="text-destructive font-medium"> {t("workspace.config.caching.providerRequired")}</span>
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
								{isUpdating || isCreating ? t("common.saving") : t("common.save")}
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
									{t("workspace.config.caching.directOnlyModePrefix")} <code>config.json</code>.{" "}
									{t("workspace.config.caching.directOnlyModeSuffix")} <code>config.json</code>{" "}
									{t("workspace.config.caching.directOnlyModeTail")}
								</div>
							)}
							{hasInvalidProviderBackedDimension && (
								<div className="rounded-md border border-red-200 bg-red-50 p-3 text-xs text-red-900">
									{t("workspace.config.caching.invalidDimensionPrefix")} <code>dimension: 1</code>.{" "}
									{t("workspace.config.caching.invalidDimensionSuffix")}
								</div>
							)}
							{/* Provider and Model Settings */}
							<div className="space-y-4">
								<h3 className="text-sm font-medium">{t("workspace.config.caching.providerAndModelSettings")}</h3>
								<div className="grid grid-cols-2 gap-4">
									<div className="space-y-2">
										<Label htmlFor="provider">{t("workspace.config.caching.configuredProviders")}</Label>
										<Select
											value={cacheConfig.provider}
											onValueChange={(value: ModelProviderName) => updateCacheConfigLocal({ provider: value })}
										>
											<SelectTrigger className="w-full">
												<SelectValue placeholder={t("workspace.config.caching.selectProvider")} />
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
										<Label htmlFor="embedding_model">{t("workspace.config.caching.embeddingModel")}</Label>
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
								<h3 className="text-sm font-medium">{t("workspace.config.caching.cacheSettings")}</h3>
								<div className="grid grid-cols-2 gap-4">
									<div className="space-y-2">
										<Label htmlFor="ttl">{t("workspace.config.caching.ttlSeconds")}</Label>
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
										<Label htmlFor="threshold">{t("workspace.config.caching.similarityThreshold")}</Label>
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
										<Label htmlFor="dimension">{t("workspace.config.caching.dimension")}</Label>
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
								<p className="text-muted-foreground text-xs">{t("workspace.config.caching.embeddingProviderKeysDesc")}</p>
							</div>

							{/* Conversation Settings */}
							<div className="space-y-4">
								<h3 className="text-sm font-medium">{t("workspace.config.caching.conversationSettings")}</h3>
								<div className="grid grid-cols-2 gap-4">
									<div className="space-y-2">
										<Label htmlFor="conversation_history_threshold">{t("workspace.config.caching.conversationHistoryThreshold")}</Label>
										<Input
											id="conversation_history_threshold"
											type="number"
											min="1"
											max="50"
											value={cacheConfig.conversation_history_threshold || 3}
											onChange={(e) => updateCacheConfigLocal({ conversation_history_threshold: parseInt(e.target.value) || 3 })}
										/>
										<p className="text-muted-foreground text-xs">{t("workspace.config.caching.conversationHistoryThresholdDesc")}</p>
									</div>
								</div>
								<div className="space-y-2">
									<div className="flex h-fit items-center justify-between space-x-2 rounded-lg border p-3">
										<div className="space-y-0.5">
											<Label className="text-sm font-medium">{t("workspace.config.caching.excludeSystemPrompt")}</Label>
											<p className="text-muted-foreground text-xs">{t("workspace.config.caching.excludeSystemPromptDesc")}</p>
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
								<h3 className="text-sm font-medium">{t("workspace.config.caching.cacheBehavior")}</h3>
								<div className="space-y-3">
									<div className="flex items-center justify-between space-x-2 rounded-lg border p-3">
										<div className="space-y-0.5">
											<Label className="text-sm font-medium">{t("workspace.config.caching.cacheByModel")}</Label>
											<p className="text-muted-foreground text-xs">{t("workspace.config.caching.cacheByModelDesc")}</p>
										</div>
										<Switch
											checked={cacheConfig.cache_by_model}
											onCheckedChange={(checked) => updateCacheConfigLocal({ cache_by_model: checked })}
											size="md"
										/>
									</div>
									<div className="flex items-center justify-between space-x-2 rounded-lg border p-3">
										<div className="space-y-0.5">
											<Label className="text-sm font-medium">{t("workspace.config.caching.cacheByProvider")}</Label>
											<p className="text-muted-foreground text-xs">{t("workspace.config.caching.cacheByProviderDesc")}</p>
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
								<Label className="text-sm font-medium">{t("workspace.config.caching.notes")}</Label>
								<ul className="text-muted-foreground list-inside list-disc text-xs">
									<li>
										{t("workspace.config.caching.noteTtlPrefix")} <b>x-bf-cache-ttl</b> {t("workspace.config.caching.noteTtlSuffix")}
									</li>
									<li>
										{t("workspace.config.caching.noteThresholdPrefix")} <b>x-bf-cache-threshold</b>{" "}
										{t("workspace.config.caching.noteThresholdSuffix")}
									</li>
									<li>
										{t("workspace.config.caching.noteTypePrefix")} <b>x-bf-cache-type</b> {t("workspace.config.caching.noteTypeSuffix")}
									</li>
									<li>
										{t("workspace.config.caching.noteNoStorePrefix")} <b>x-bf-cache-no-store</b>{" "}
										{t("workspace.config.caching.noteNoStoreSuffix")}
									</li>
								</ul>
							</div>
						</div>
					))}
			</div>
		</div>
	);
}