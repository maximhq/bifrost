"use client";

import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Separator } from "@/components/ui/separator";
import { getErrorMessage, useGetPluginsQuery, useUpdatePluginMutation, useCreatePluginMutation, useGetProvidersQuery } from "@/lib/store";
import { CacheConfig, ModelProvider } from "@/lib/types/config";
import { useEffect, useState, useMemo, useCallback, useRef } from "react";
import { toast } from "sonner";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { PROVIDER_LABELS } from "@/lib/constants/logs";
import { Loader2 } from "lucide-react";
import { SEMANTIC_CACHE_PLUGIN } from "@/lib/types/plugins";

const defaultCacheConfig: CacheConfig = {
	provider: "openai" as ModelProvider,
	keys: [],
	embedding_model: "text-embedding-3-small",
	ttl_seconds: 300,
	threshold: 0.8,
	cache_by_model: true,
	cache_by_provider: true,
};

interface PluginsFormProps {
	isVectorStoreEnabled: boolean;
}

export default function PluginsForm({ isVectorStoreEnabled }: PluginsFormProps) {
	const [cacheConfig, setCacheConfig] = useState<CacheConfig>(defaultCacheConfig);

	const { data: providersData, error: providersError, isLoading: providersLoading } = useGetProvidersQuery();

	const providers = useMemo(() => providersData?.providers || [], [providersData]);

	useEffect(() => {
		if (providersError) {
			toast.error(`Failed to load providers: ${getErrorMessage(providersError as any)}`);
		}
	}, [providersError]);

	// RTK Query hooks
	const { data: plugins, isLoading: loading } = useGetPluginsQuery();
	const [updatePlugin] = useUpdatePluginMutation();
	const [createPlugin] = useCreatePluginMutation();

	// Get semantic cache plugin and its config
	const semanticCachePlugin = useMemo(() => plugins?.find((plugin) => plugin.name === SEMANTIC_CACHE_PLUGIN), [plugins]);

	const isSemanticCacheEnabled = Boolean(semanticCachePlugin?.enabled);

	// Initialize cache config from plugin data
	useEffect(() => {
		if (semanticCachePlugin?.config) {
			setCacheConfig({ ...defaultCacheConfig, ...semanticCachePlugin.config });
		}
	}, [semanticCachePlugin]);

	// Update default provider when providers are loaded (only for new configs)
	useEffect(() => {
		if (providers.length > 0 && !semanticCachePlugin?.config) {
			setCacheConfig((prev) => ({
				...prev,
				provider: providers[0].name as ModelProvider,
			}));
		}
	}, [providers, semanticCachePlugin?.config]);

	// Handle semantic cache toggle (create or update)
	const handleSemanticCacheToggle = async (enabled: boolean) => {
		try {
			if (semanticCachePlugin) {
				// Update existing plugin
				await updatePlugin({
					name: SEMANTIC_CACHE_PLUGIN,
					data: { enabled, config: cacheConfig },
				}).unwrap();
			} else {
				// Create new plugin
				await createPlugin({
					name: SEMANTIC_CACHE_PLUGIN,
					enabled,
					config: cacheConfig,
				}).unwrap();
			}
			toast.success(`Semantic cache ${enabled ? "enabled" : "disabled"} successfully`);
		} catch (error) {
			const errorMessage = getErrorMessage(error);
			toast.error(`Failed to ${enabled ? "enable" : "disable"} semantic cache: ${errorMessage}`);
		}
	};

	// Update cache config
	const updateCacheConfig = async (updates: Partial<CacheConfig>) => {
		// Capture snapshot of previous config before updating
		const previousConfig = cacheConfig;
		const newConfig = { ...cacheConfig, ...updates };

		// Set optimistic state
		setCacheConfig(newConfig);

		if (semanticCachePlugin?.enabled) {
			try {
				await updatePlugin({
					name: SEMANTIC_CACHE_PLUGIN,
					data: { enabled: true, config: newConfig },
				}).unwrap();

				// Success toast
				toast.success("Cache configuration updated successfully");
			} catch (error) {
				// Revert to previous config on error
				setCacheConfig(previousConfig);
				toast.error("Failed to update cache configuration");
			}
		}
	};

	// Ref to store the timeout ID for debouncing
	const debounceTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);

	// Debounced version for text/number inputs
	const debouncedUpdateCacheConfig = useCallback(
		(updates: Partial<CacheConfig>) => {
			// Update local state immediately for responsive UI
			const newConfig = { ...cacheConfig, ...updates };
			setCacheConfig(newConfig);

			// Clear previous timeout
			if (debounceTimeoutRef.current) {
				clearTimeout(debounceTimeoutRef.current);
			}

			// Only save to backend if plugin is enabled, with debouncing
			if (semanticCachePlugin?.enabled) {
				debounceTimeoutRef.current = setTimeout(() => {
					updatePlugin({
						name: SEMANTIC_CACHE_PLUGIN,
						data: { enabled: true, config: newConfig },
					})
						.unwrap()
						.then(() => {
							toast.success("Cache configuration updated successfully");
						})
						.catch((error) => {
							toast.error("Failed to update cache configuration");
							// Revert on error
							setCacheConfig(cacheConfig);
						});
				}, 500); // 500ms debounce
			}
		},
		[cacheConfig, semanticCachePlugin?.enabled, updatePlugin],
	);

	// Cleanup timeout on component unmount
	useEffect(() => {
		return () => {
			if (debounceTimeoutRef.current) {
				clearTimeout(debounceTimeoutRef.current);
			}
		};
	}, []);

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
					<div className="space-y-0.5">
						<label htmlFor="enable-caching" className="text-sm font-medium">
							Enable Semantic Caching
						</label>
						<p className="text-muted-foreground text-sm">
							Enable semantic caching for requests. Send <b>x-bf-cache-key</b> header with requests to use semantic caching.
							{!isVectorStoreEnabled && (
								<span className="text-destructive font-medium">Requires vector store to be configured and enabled in config.json.</span>
							)}
							{!providersLoading && providers?.length === 0 && (
								<span className="text-destructive font-medium"> Requires at least one provider to be configured.</span>
							)}
						</p>
					</div>
					<Switch
						id="enable-caching"
						size="md"
						checked={isSemanticCacheEnabled && isVectorStoreEnabled}
						disabled={!isVectorStoreEnabled || providersLoading || providers.length === 0}
						onCheckedChange={(checked) => {
							if (isVectorStoreEnabled) {
								handleSemanticCacheToggle(checked);
							}
						}}
					/>
				</div>

				{/* Cache Configuration (only show when enabled) */}
				{isSemanticCacheEnabled &&
					isVectorStoreEnabled &&
					(providersLoading ? (
						<div className="flex items-center justify-center">
							<Loader2 className="h-4 w-4 animate-spin" />
						</div>
					) : (
						<div className="mt-4 space-y-4">
							<Separator />
							{/* Provider and Model Settings */}
							<div className="space-y-4">
								<h3 className="text-sm font-medium">Provider and Model Settings</h3>
								<div className="grid grid-cols-2 gap-4">
									<div className="space-y-2">
										<Label htmlFor="provider">Configured Providers</Label>
										<Select value={cacheConfig.provider} onValueChange={(value: ModelProvider) => updateCacheConfig({ provider: value })}>
											<SelectTrigger className="w-full">
												<SelectValue placeholder="Select provider" />
											</SelectTrigger>
											<SelectContent>
												{providers.map((provider) => (
													<SelectItem key={provider.name} value={provider.name}>
														{PROVIDER_LABELS[provider.name as ModelProvider]}
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
											value={cacheConfig.embedding_model}
											onChange={(e) => debouncedUpdateCacheConfig({ embedding_model: e.target.value })}
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
											value={cacheConfig.ttl_seconds}
											onChange={(e) => debouncedUpdateCacheConfig({ ttl_seconds: parseInt(e.target.value) || 300 })}
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
											value={cacheConfig.threshold}
											onChange={(e) => debouncedUpdateCacheConfig({ threshold: parseFloat(e.target.value) || 0.8 })}
										/>
									</div>
								</div>
								<div className="space-y-2">
									<Label className="text-sm font-medium">API Keys</Label>
									<p className="text-muted-foreground text-xs">
										API keys for the embedding provider will be inherited from the main provider configuration. The semantic cache will use
										the configured provider&apos;s keys automatically. <b>Updates in keys will be reflected on Bifrost restart.</b>
									</p>
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
											onCheckedChange={(checked) => updateCacheConfig({ cache_by_model: checked })}
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
											onCheckedChange={(checked) => updateCacheConfig({ cache_by_provider: checked })}
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
								</ul>
							</div>
						</div>
					))}
			</div>
		</div>
	);
}
