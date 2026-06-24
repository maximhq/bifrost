import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ModelMultiselect } from "@/components/ui/modelMultiselect";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { EmbeddingSupportedProviders, getProviderLabel } from "@/lib/constants/logs";
import {
	getErrorMessage,
	useCreatePluginMutation,
	useGetCoreConfigQuery,
	useGetPluginsQuery,
	useGetProvidersQuery,
	useUpdatePluginMutation,
} from "@/lib/store";
import { CacheConfig, EditorCacheConfig, ModelProvider, ModelProviderName } from "@/lib/types/config";
import { SEMANTIC_CACHE_PLUGIN } from "@/lib/types/plugins";
import { cn } from "@/lib/utils";
import { Loader2 } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";

// The local cache plugin runs in one of two modes. Direct-only is purely
// hash-based, no embedding provider needed; perfect for exact-replay
// caching. Semantic adds vector similarity on top, requiring an
// embedding-capable provider and the model's real dimension.
type CacheMode = "direct" | "semantic";

// Embedding-capable providers gate the semantic mode. Built-in providers
// are listed in EmbeddingSupportedProviders; custom providers expose
// support via custom_provider_config.allowed_requests.embedding.
const supportsEmbedding = (provider: ModelProvider): boolean => {
	if (provider.custom_provider_config) {
		return provider.custom_provider_config.allowed_requests?.embedding === true;
	}
	return (EmbeddingSupportedProviders as readonly string[]).includes(provider.name);
};

const defaultDirectConfig: EditorCacheConfig = {
	ttl: 300,
	threshold: 0.8,
	dimension: 1,
	conversation_history_threshold: 3,
	exclude_system_prompt: false,
	cache_by_model: true,
	cache_by_provider: true,
};

// Configs we treat as "the user has nothing saved": both API responses
// where every field is the type's zero value and the literal undefined
// look like this.
const isEmptyConfig = (config: Partial<EditorCacheConfig> | undefined): boolean => {
	if (!config) return true;
	// Booleans are deliberate user choices (e.g. cache_by_model: false), not
	// empty markers — only treat numeric/string zero values as empty.
	const isZero = (v: unknown) => v === undefined || v === null || v === 0 || v === "";
	return Object.values(config).every(isZero);
};

const toEditorCacheConfig = (config?: Partial<EditorCacheConfig>): EditorCacheConfig => {
	if (!config || isEmptyConfig(config)) {
		return { ...defaultDirectConfig };
	}
	return { ...defaultDirectConfig, ...config };
};

const inferMode = (config: EditorCacheConfig): CacheMode => {
	if (config.dimension && config.dimension > 1 && config.provider) return "semantic";
	return "direct";
};

// Strip semantic-only fields when persisting a direct-only payload so the
// server validator doesn't reject a stale provider choice.
const buildPayload = (config: EditorCacheConfig, mode: CacheMode): CacheConfig => {
	const base = {
		ttl: config.ttl ?? 0,
		threshold: config.threshold ?? 0,
		conversation_history_threshold: config.conversation_history_threshold,
		exclude_system_prompt: config.exclude_system_prompt,
		cache_by_model: config.cache_by_model,
		cache_by_provider: config.cache_by_provider,
		vector_store_namespace: config.vector_store_namespace?.trim() || undefined,
		default_cache_key: config.default_cache_key?.trim() || undefined,
	};
	if (mode === "direct") {
		return { ...base, dimension: 1 } as CacheConfig;
	}
	return {
		...base,
		provider: config.provider as ModelProviderName,
		embedding_model: config.embedding_model ?? "",
		dimension: config.dimension ?? 0,
	} as CacheConfig;
};

const validateForSave = (config: EditorCacheConfig, mode: CacheMode): string | null => {
	if (mode === "semantic") {
		if (!config.provider) return "Pick an embedding provider for semantic mode, or switch to Direct only.";
		if (!config.embedding_model?.trim()) return "Pick an embedding model for semantic mode.";
		if (!config.dimension || config.dimension <= 1) {
			return "Semantic mode requires the embedding model's real dimension (must be > 1).";
		}
	}
	if (config.ttl !== undefined && config.ttl < 0) return "TTL must be non-negative.";
	if (config.threshold !== undefined && (config.threshold < 0 || config.threshold > 1)) {
		return "Similarity threshold must be between 0 and 1.";
	}
	if (
		config.conversation_history_threshold !== undefined &&
		(config.conversation_history_threshold < 1 || config.conversation_history_threshold > 50)
	) {
		return "Conversation history threshold must be between 1 and 50.";
	}
	return null;
};

export default function CachingView() {
	const { data: bifrostConfig, isLoading: configLoading, error: configError } = useGetCoreConfigQuery({ fromDB: true });
	const isVectorStoreEnabled = bifrostConfig?.is_cache_connected ?? false;

	// Local cache state lives on the plugin row keyed by SEMANTIC_CACHE_PLUGIN.
	// No dedicated /local-cache-config endpoint exists — the plugins API is
	// the source of truth for both the enabled flag and the config blob.
	const { data: plugins, isLoading: pluginsLoading } = useGetPluginsQuery();
	const semanticCachePlugin = useMemo(() => plugins?.find((p) => p.name === SEMANTIC_CACHE_PLUGIN), [plugins]);
	const enabledOnServer = Boolean(semanticCachePlugin?.enabled);

	const { data: providersData, error: providersError, isLoading: providersLoading } = useGetProvidersQuery();
	const providers = useMemo(() => providersData || [], [providersData]);
	const embeddingProviders = useMemo(() => providers.filter(supportsEmbedding), [providers]);

	const [updatePlugin, { isLoading: isUpdating }] = useUpdatePluginMutation();
	const [createPlugin, { isLoading: isCreating }] = useCreatePluginMutation();
	const isSaving = isUpdating || isCreating;

	const [cacheConfig, setCacheConfig] = useState<EditorCacheConfig>(defaultDirectConfig);
	const [serverCacheConfig, setServerCacheConfig] = useState<EditorCacheConfig>(defaultDirectConfig);
	const [mode, setMode] = useState<CacheMode>("direct");

	// Hydrate from the plugin row once it lands. If the plugin doesn't exist
	// yet (first-time setup), keep the default direct-only seed so the user
	// can start typing before any save.
	useEffect(() => {
		if (plugins === undefined) return;
		if (!semanticCachePlugin?.config) return;
		const editorConfig = toEditorCacheConfig(semanticCachePlugin.config as Partial<EditorCacheConfig>);
		setCacheConfig(editorConfig);
		setServerCacheConfig(editorConfig);
		setMode(inferMode(editorConfig));
	}, [plugins, semanticCachePlugin]);

	useEffect(() => {
		if (providersError) {
			toast.error(`Failed to load providers: ${getErrorMessage(providersError as any)}`);
		}
	}, [providersError]);

	// Surface validation problems inline rather than only on Save click.
	const validationError = useMemo(() => validateForSave(cacheConfig, mode), [cacheConfig, mode]);

	// Only show the dimension/namespace heads-up when the user has actually
	// touched a structural field. Showing it permanently in semantic mode
	// trains users to ignore it; showing it on diff makes it land.
	const hasStructuralChange = useMemo(() => {
		return (
			cacheConfig.provider !== serverCacheConfig.provider ||
			cacheConfig.embedding_model !== serverCacheConfig.embedding_model ||
			cacheConfig.dimension !== serverCacheConfig.dimension
		);
	}, [cacheConfig, serverCacheConfig]);

	const hasUnsavedConfigChanges = useMemo(() => {
		const fields: (keyof EditorCacheConfig)[] = [
			"provider",
			"embedding_model",
			"dimension",
			"ttl",
			"threshold",
			"conversation_history_threshold",
			"exclude_system_prompt",
			"cache_by_model",
			"cache_by_provider",
			"vector_store_namespace",
			"default_cache_key",
		];
		const changed = fields.some((k) => (cacheConfig[k] ?? "") !== (serverCacheConfig[k] ?? ""));
		const modeChanged = inferMode(serverCacheConfig) !== mode;
		return changed || modeChanged;
	}, [cacheConfig, serverCacheConfig, mode]);

	const updateLocal = (updates: Partial<EditorCacheConfig>) => {
		setCacheConfig((prev) => ({ ...prev, ...updates }));
	};

	// Toggle handler. Updates the semantic_cache plugin's enabled flag while
	// keeping the last-saved config so the backend can ReloadPlugin/RemovePlugin
	// based on the new flag. When toggling on for the first time and no plugin
	// row exists, we seed it with the current editor config (direct-only by
	// default) so the create call has a valid payload — the user can refine
	// the config and Save afterwards.
	const handleToggle = async (checked: boolean) => {
		try {
			if (semanticCachePlugin) {
				await updatePlugin({
					name: SEMANTIC_CACHE_PLUGIN,
					data: { enabled: checked, config: semanticCachePlugin.config },
				}).unwrap();
			} else {
				// No plugin row + user toggling off ⇒ nothing to disable.
				// Bail before the success toast so we don't lie about the state.
				if (!checked) return;
				const err = validateForSave(cacheConfig, mode);
				if (err) {
					toast.error(err);
					return;
				}
				const payload = buildPayload(cacheConfig, mode);
				await createPlugin({
					name: SEMANTIC_CACHE_PLUGIN,
					enabled: true,
					config: payload,
					path: "",
				}).unwrap();
			}
			toast.success(checked ? "Local cache enabled" : "Local cache disabled");
		} catch (error) {
			toast.error(`Failed to ${checked ? "enable" : "disable"} local cache: ${getErrorMessage(error)}`);
		}
	};

	const handleSave = async () => {
		const err = validateForSave(cacheConfig, mode);
		if (err) {
			toast.error(err);
			return;
		}
		const payload = buildPayload(cacheConfig, mode);
		try {
			const updated = semanticCachePlugin
				? await updatePlugin({
						name: SEMANTIC_CACHE_PLUGIN,
						data: { enabled: semanticCachePlugin.enabled, config: payload },
					}).unwrap()
				: await createPlugin({
						name: SEMANTIC_CACHE_PLUGIN,
						enabled: false,
						config: payload,
						path: "",
					}).unwrap();
			const editor = toEditorCacheConfig(updated.config as Partial<EditorCacheConfig>);
			setCacheConfig(editor);
			setServerCacheConfig(editor);
			setMode(inferMode(editor));
			toast.success("Cache configuration updated");
		} catch (error) {
			toast.error(`Failed to update cache configuration: ${getErrorMessage(error)}`);
		}
	};

	const cachingActive = enabledOnServer && isVectorStoreEnabled;
	const isLoading = configLoading || pluginsLoading;

	return (
		<div className="mx-auto w-full max-w-4xl space-y-6">
			<div>
				<h2 className="text-lg font-semibold tracking-tight">Local Cache</h2>
				<p className="text-muted-foreground text-sm">
					Cache responses locally with two complementary lookup paths: <b>direct</b> hash matching for exact replays, and <b>semantic</b>{" "}
					similarity search for related content. Send the <b>x-bf-cache-key</b> header to scope cached responses to a tenant or feature.{" "}
					{!isVectorStoreEnabled && (
						<span className="text-destructive font-medium">
							Requires a vector store to be configured and enabled in <code>config.json</code>.
						</span>
					)}
				</p>
			</div>

			{configError !== undefined && (
				<div className="border-destructive/50 bg-destructive/10 rounded-sm border p-4">
					<p className="text-destructive text-sm font-medium">Failed to load configuration</p>
					<p className="text-muted-foreground mt-1 text-sm">
						{getErrorMessage(configError) || "An unexpected error occurred. Please try again."}
					</p>
				</div>
			)}

			{isLoading && (
				<div className="flex items-center justify-center py-8">
					<Loader2 className="text-muted-foreground h-4 w-4 animate-spin" />
				</div>
			)}

			{!isLoading && !configError && (
				<div className="space-y-4">
					{/* Enable toggle flips plugin.enabled on the semantic_cache
					    plugin row. The plugins API handles ReloadPlugin /
					    RemovePlugin transparently on update. */}
					<div className="flex items-center justify-between space-x-2">
						<div className="space-y-0.5">
							<label htmlFor="enable-caching" className="text-sm font-medium">
								Enable Caching
							</label>
							<p className="text-muted-foreground text-sm">
								Loads (or unloads) the plugin without a server restart. Configuration changes you make below mutate the live plugin in
								place, no redeploy needed.{" "}
							</p>
						</div>
						<Switch
							id="enable-caching"
							data-testid="caching-enable-switch"
							size="md"
							checked={cachingActive}
							disabled={!isVectorStoreEnabled || isSaving}
							onCheckedChange={handleToggle}
						/>
					</div>

					{providersLoading ? (
						<div className="flex items-center justify-center py-4">
							<Loader2 className="text-muted-foreground h-4 w-4 animate-spin" />
						</div>
					) : (
						<>
							<div className={cn("space-y-4", !cachingActive && "pointer-events-none opacity-50")} aria-disabled={!cachingActive}>
								{/* Mode picker. Direct-only is first-class. */}
								<div className="space-y-2">
									<Label className="text-sm font-medium">Cache Mode</Label>
									<Tabs value={mode} onValueChange={(v) => setMode(v as CacheMode)}>
										<TabsList className="grid w-full grid-cols-2">
											<TabsTrigger value="direct" data-testid="caching-mode-direct-tab">
												Direct only
											</TabsTrigger>
											<TabsTrigger
												value="semantic"
												data-testid="caching-mode-semantic-tab"
												disabled={embeddingProviders.length === 0}
												title={
													embeddingProviders.length === 0 ? "Configure an embedding-capable provider to enable semantic mode." : undefined
												}
											>
												Direct + Semantic
											</TabsTrigger>
										</TabsList>
									</Tabs>
									<p className="text-muted-foreground text-xs">
										{mode === "direct" ? (
											<>
												Direct-only mode hashes each request and replays an exact match. No embeddings, no provider needed. Cheapest path,
												perfect for stable prompts.
											</>
										) : (
											<>
												Direct + semantic mode adds vector similarity search on top of direct hash matching. Requires an embedding-capable
												provider and the model&apos;s real dimension. Direct hits are still served first; semantic search runs only when the
												direct lookup misses.
											</>
										)}
									</p>
								</div>

								{validationError && (
									<div className="border-destructive/40 bg-destructive/10 text-destructive rounded-sm border p-3 text-xs">
										{validationError}
									</div>
								)}

								{/* Provider/model/dimension only appear in semantic mode. */}
								{mode === "semantic" && (
									<>
										{hasStructuralChange && (
											<div className="rounded-sm border border-amber-200 bg-amber-50 p-3 text-xs text-amber-900">
												<b>Heads up:</b> a vector store namespace can only hold vectors of <em>one</em> dimension. Whenever you change the
												embedding <b>provider</b>, <b>model</b>, or <b>dimension</b>, make sure the <b>dimension</b> still matches what the
												model produces, otherwise writes to the existing namespace will fail and reads will silently miss. The namespace is{" "}
												<em>not</em> recreated automatically; either use a fresh namespace or drop the existing class/index in your vector
												store before saving.
											</div>
										)}

										<div className="space-y-4">
											<h3 className="text-sm font-medium">Embedding Provider &amp; Model</h3>
											<div className="grid grid-cols-2 gap-4">
												<div className="space-y-2">
													<Label htmlFor="provider">Configured Providers</Label>
													<Select
														value={cacheConfig.provider}
														onValueChange={(value: ModelProviderName) =>
															updateLocal({
																provider: value,
																embedding_model: value === cacheConfig.provider ? cacheConfig.embedding_model : "",
															})
														}
													>
														<SelectTrigger className="w-full" data-testid="caching-provider-select">
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
														data-testid="caching-embedding-model-select"
														isSingleSelect
														provider={cacheConfig.provider || undefined}
														value={cacheConfig.embedding_model ?? ""}
														onChange={(model) => updateLocal({ embedding_model: model })}
														placeholder={cacheConfig.provider ? "Search or type an embedding model..." : "Select a provider first"}
														disabled={!cacheConfig.provider}
													/>
												</div>
											</div>
											<p className="text-muted-foreground text-xs">
												API keys are inherited from the embedding provider&apos;s main configuration, you don&apos;t need to add them again
												here.
											</p>
											<div className="space-y-2">
												<Label htmlFor="dimension">Dimension</Label>
												<Input
													id="dimension"
													data-testid="caching-dimension-input"
													type="number"
													min="2"
													value={cacheConfig.dimension === undefined || Number.isNaN(cacheConfig.dimension) ? "" : cacheConfig.dimension}
													onChange={(e) => {
														const value = e.target.value;
														if (value === "") {
															updateLocal({ dimension: undefined });
															return;
														}
														const parsed = parseInt(value);
														if (!Number.isNaN(parsed)) {
															updateLocal({ dimension: parsed });
														}
													}}
												/>
												<p className="text-muted-foreground text-xs">
													Vector size produced by the embedding model. Must match the model exactly (e.g. <code>1536</code> for OpenAI{" "}
													<code>text-embedding-3-small</code>, <code>3072</code> for <code>text-embedding-3-large</code>, <code>768</code>{" "}
													for many Cohere/Voyage models).
												</p>
											</div>
										</div>
									</>
								)}

								{/* Cache settings shared across modes. */}
								<div className="space-y-4">
									<h3 className="text-sm font-medium">Cache Settings</h3>
									<div className={cn("grid gap-4", mode === "semantic" ? "grid-cols-2" : "grid-cols-1")}>
										<div className="space-y-2">
											<Label htmlFor="ttl">TTL (seconds)</Label>
											<Input
												id="ttl"
												data-testid="caching-ttl-input"
												type="number"
												min="1"
												value={cacheConfig.ttl === undefined || Number.isNaN(cacheConfig.ttl) ? "" : cacheConfig.ttl}
												onChange={(e) => {
													const value = e.target.value;
													if (value === "") {
														updateLocal({ ttl: undefined });
														return;
													}
													const parsed = parseInt(value);
													if (!Number.isNaN(parsed)) {
														updateLocal({ ttl: parsed });
													}
												}}
											/>
											<p className="text-muted-foreground text-xs">
												How long cached entries live before they expire. Override per-request via the <b>x-bf-cache-ttl</b> header.
											</p>
										</div>
										{mode === "semantic" && (
											<div className="space-y-2">
												<Label htmlFor="threshold">Similarity Threshold</Label>
												<Input
													id="threshold"
													data-testid="caching-threshold-input"
													type="number"
													min="0"
													max="1"
													step="0.01"
													value={cacheConfig.threshold === undefined || Number.isNaN(cacheConfig.threshold) ? "" : cacheConfig.threshold}
													onChange={(e) => {
														const value = e.target.value;
														if (value === "") {
															updateLocal({ threshold: undefined });
															return;
														}
														const parsed = parseFloat(value);
														if (!Number.isNaN(parsed)) {
															updateLocal({ threshold: parsed });
														}
													}}
												/>
												<p className="text-muted-foreground text-xs">
													Minimum cosine similarity for a semantic hit. Override per-request via <b>x-bf-cache-threshold</b>.
												</p>
											</div>
										)}
									</div>
								</div>

								{/* Storage & Cache Key. */}
								<div className="space-y-4">
									<h3 className="text-sm font-medium">Storage &amp; Cache Key</h3>
									<div className="grid grid-cols-2 gap-4">
										<div className="space-y-2">
											<Label htmlFor="vector_store_namespace">Vector Store Namespace</Label>
											<Input
												id="vector_store_namespace"
												data-testid="caching-vector-store-namespace-input"
												type="text"
												placeholder="BifrostLocalCachePlugin"
												value={cacheConfig.vector_store_namespace ?? ""}
												onChange={(e) => updateLocal({ vector_store_namespace: e.target.value })}
											/>
											<p className="text-muted-foreground text-xs">
												Bucket/index name where cache entries live. Leave blank to use the default (<code>BifrostLocalCachePlugin</code>).
												Changing this points the plugin at a different (possibly empty) bucket. Old entries are not deleted, they just stop
												being queried.
											</p>
										</div>
										<div className="space-y-2">
											<Label htmlFor="default_cache_key">Default Cache Key</Label>
											<Input
												id="default_cache_key"
												data-testid="caching-default-cache-key-input"
												type="text"
												placeholder="(none)"
												value={cacheConfig.default_cache_key ?? ""}
												onChange={(e) => updateLocal({ default_cache_key: e.target.value })}
											/>
											<p className="text-muted-foreground text-xs">
												Fallback partition key used when a request doesn&apos;t set <b>x-bf-cache-key</b>. Cache keys isolate entries: same
												key ↔ shared cache pool. Leave blank to <b>disable caching</b> for any request that doesn&apos;t send the header.
											</p>
										</div>
									</div>
								</div>

								{/* Conversation Settings. */}
								<div className="space-y-4">
									<h3 className="text-sm font-medium">Conversation Settings</h3>
									<div className="grid grid-cols-2 gap-4">
										<div className="space-y-2">
											<Label htmlFor="conversation_history_threshold">Conversation History Threshold</Label>
											<Input
												id="conversation_history_threshold"
												data-testid="caching-conversation-history-threshold-input"
												type="number"
												min="1"
												max="50"
												value={
													cacheConfig.conversation_history_threshold === undefined ||
													Number.isNaN(cacheConfig.conversation_history_threshold)
														? ""
														: cacheConfig.conversation_history_threshold
												}
												onChange={(e) => {
													const value = e.target.value;
													if (value === "") {
														updateLocal({ conversation_history_threshold: undefined });
														return;
													}
													const parsed = parseInt(value);
													if (!Number.isNaN(parsed)) {
														updateLocal({ conversation_history_threshold: parsed });
													}
												}}
											/>
											<p className="text-muted-foreground text-xs">
												Skip caching for conversations with more than this many messages. Long histories rarely match exactly and inflate
												the cache without paying off.
											</p>
										</div>
									</div>
									<div className="space-y-2">
										<div className="flex h-fit items-center justify-between space-x-2 rounded-sm border p-3">
											<div className="space-y-0.5">
												<Label className="text-sm font-medium">Exclude System Prompt</Label>
												<p className="text-muted-foreground text-xs">Strip system messages from the cache key.</p>
											</div>
											<Switch
												data-testid="caching-exclude-system-prompt-switch"
												checked={cacheConfig.exclude_system_prompt || false}
												onCheckedChange={(checked) => updateLocal({ exclude_system_prompt: checked })}
												size="md"
											/>
										</div>
									</div>
								</div>

								{/* Cache Behavior applies to both modes. */}
								<div className="space-y-4">
									<h3 className="text-sm font-medium">Cache Key Composition</h3>
									<div className="space-y-3">
										<div className="flex items-center justify-between space-x-2 rounded-sm border p-3">
											<div className="space-y-0.5">
												<Label className="text-sm font-medium">Cache by Model</Label>
												<p className="text-muted-foreground text-xs">
													Include model name in the cache key. Different models won&apos;t share cached responses.
												</p>
											</div>
											<Switch
												data-testid="caching-cache-by-model-switch"
												checked={cacheConfig.cache_by_model}
												onCheckedChange={(checked) => updateLocal({ cache_by_model: checked })}
												size="md"
											/>
										</div>
										<div className="flex items-center justify-between space-x-2 rounded-sm border p-3">
											<div className="space-y-0.5">
												<Label className="text-sm font-medium">Cache by Provider</Label>
												<p className="text-muted-foreground text-xs">
													Include provider name in the cache key. Different providers won&apos;t share cached responses.
												</p>
											</div>
											<Switch
												data-testid="caching-cache-by-provider-switch"
												checked={cacheConfig.cache_by_provider}
												onCheckedChange={(checked) => updateLocal({ cache_by_provider: checked })}
												size="md"
											/>
										</div>
									</div>
								</div>

								<div className="space-y-2">
									<Label className="text-sm font-medium">Per-request overrides</Label>
									<ul className="text-muted-foreground list-inside list-disc text-xs">
										<li>
											<b>x-bf-cache-key</b>: scope this request to a specific cache partition.
										</li>
										<li>
											<b>x-bf-cache-ttl</b>: override TTL for just this request.
										</li>
										<li>
											<b>x-bf-cache-threshold</b>: override the semantic similarity threshold.
										</li>
										<li>
											<b>x-bf-cache-type</b>: send <code>direct</code> or <code>semantic</code> to limit lookup to one path.
										</li>
										<li>
											<b>x-bf-cache-no-store</b>: <code>true</code> to skip writing the response (still serves cached hits).
										</li>
									</ul>
								</div>
							</div>

							<div className="flex justify-end pt-2">
								<Button
									data-testid="caching-save-button"
									onClick={handleSave}
									disabled={!hasUnsavedConfigChanges || isSaving || Boolean(validationError)}
								>
									{isSaving ? "Saving..." : "Save Changes"}
								</Button>
							</div>
						</>
					)}
				</div>
			)}
		</div>
	);
}