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
	useGetCoreConfigQuery,
	useGetLocalCacheConfigQuery,
	useGetProvidersQuery,
	useUpdateCoreConfigMutation,
	useUpdateLocalCacheConfigMutation,
} from "@/lib/store";
import { BifrostConfig, CacheConfig, EditorCacheConfig, ModelProvider, ModelProviderName } from "@/lib/types/config";
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
	cleanup_on_shutdown: false,
};

// Configs we treat as "the user has nothing saved": both API responses
// where every field is the type's zero value and the literal undefined
// look like this.
const isEmptyConfig = (config: Partial<EditorCacheConfig> | undefined): boolean => {
	if (!config) return true;
	const isZero = (v: unknown) => v === undefined || v === null || v === 0 || v === "" || v === false;
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
		cleanup_on_shutdown: config.cleanup_on_shutdown,
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
	return null;
};

export default function CachingView() {
	const { data: bifrostConfig, isLoading: configLoading, error: configError } = useGetCoreConfigQuery({ fromDB: true });
	const isVectorStoreEnabled = bifrostConfig?.is_cache_connected ?? false;
	const enabledOnServer = bifrostConfig?.client_config?.enable_local_cache ?? false;

	const { data: serverConfig, isLoading: localCacheLoading } = useGetLocalCacheConfigQuery();
	const { data: providersData, error: providersError, isLoading: providersLoading } = useGetProvidersQuery();
	const providers = useMemo(() => providersData || [], [providersData]);
	const embeddingProviders = useMemo(() => providers.filter(supportsEmbedding), [providers]);

	const [updateLocalCacheConfig, { isLoading: isSaving }] = useUpdateLocalCacheConfigMutation();
	const [updateCoreConfig, { isLoading: isToggling }] = useUpdateCoreConfigMutation();

	const [cacheConfig, setCacheConfig] = useState<EditorCacheConfig>(defaultDirectConfig);
	const [serverCacheConfig, setServerCacheConfig] = useState<EditorCacheConfig>(defaultDirectConfig);
	const [mode, setMode] = useState<CacheMode>("direct");

	// Hydrate from the server-side config row when it lands.
	useEffect(() => {
		if (serverConfig === undefined) return;
		const editorConfig = toEditorCacheConfig(serverConfig);
		setCacheConfig(editorConfig);
		setServerCacheConfig(editorConfig);
		setMode(inferMode(editorConfig));
	}, [serverConfig]);

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
			"cleanup_on_shutdown",
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

	// Toggle handler. Hits the global config endpoint so the server-side
	// compat-shim can ReloadPlugin/RemovePlugin transparently. We don't gate
	// on hasConfigSaved here because Bifrost rejects the enable-true PUT
	// when no local-cache row exists; we surface that as a toast.
	const handleToggle = async (checked: boolean) => {
		if (!bifrostConfig) return;
		try {
			const next: BifrostConfig = {
				...bifrostConfig,
				client_config: { ...bifrostConfig.client_config, enable_local_cache: checked },
			};
			await updateCoreConfig(next).unwrap();
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
			const updated = await updateLocalCacheConfig(payload).unwrap();
			const editor = toEditorCacheConfig(updated);
			setCacheConfig(editor);
			setServerCacheConfig(editor);
			setMode(inferMode(editor));
			toast.success("Cache configuration updated");
		} catch (error) {
			toast.error(`Failed to update cache configuration: ${getErrorMessage(error)}`);
		}
	};

	const cachingActive = enabledOnServer && isVectorStoreEnabled;
	const isLoading = configLoading || localCacheLoading;

	return (
		<div className="mx-auto w-full max-w-4xl space-y-6">
			<div>
				<h2 className="text-lg font-semibold tracking-tight">Local Cache</h2>
				<p className="text-muted-foreground text-sm">
					Cache responses locally with two complementary lookup paths: <b>direct</b> hash matching for exact replays, and{" "}
					<b>semantic</b> similarity search for related content. Send the <b>x-bf-cache-key</b> header to scope cached
					responses to a tenant or feature.
				</p>
			</div>

			{configError !== undefined && (
				<div className="border-destructive/50 bg-destructive/10 rounded-lg border p-4">
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
					{/* Enable toggle row hits PUT /api/config with
					    client.enable_local_cache. The server-side compat-shim
					    handles ReloadPlugin / RemovePlugin transparently. */}
					<div className="flex items-center justify-between space-x-2">
						<div className="space-y-0.5">
							<label htmlFor="enable-caching" className="text-sm font-medium">
								Enable Local Cache
							</label>
							<p className="text-muted-foreground text-sm">
								Loads (or unloads) the plugin without a server restart. Configuration changes you make below mutate the live
								plugin in place, no redeploy needed.{" "}
								{!isVectorStoreEnabled && (
									<span className="text-destructive font-medium">
										Requires a vector store to be configured and enabled in <code>config.json</code>.
									</span>
								)}
							</p>
						</div>
						<Switch
							id="enable-caching"
							size="md"
							checked={cachingActive}
							disabled={!isVectorStoreEnabled || isToggling}
							onCheckedChange={handleToggle}
						/>
					</div>

					{providersLoading ? (
						<div className="flex items-center justify-center py-4">
							<Loader2 className="text-muted-foreground h-4 w-4 animate-spin" />
						</div>
					) : (
						<>
							<div
								className={cn("space-y-4", !cachingActive && "pointer-events-none opacity-50")}
								aria-disabled={!cachingActive}
							>
								{/* Mode picker. Direct-only is first-class. */}
								<div className="space-y-2">
									<Label className="text-sm font-medium">Cache Mode</Label>
									<Tabs value={mode} onValueChange={(v) => setMode(v as CacheMode)}>
										<TabsList className="grid w-full grid-cols-2">
											<TabsTrigger value="direct">Direct only</TabsTrigger>
											<TabsTrigger
												value="semantic"
												disabled={embeddingProviders.length === 0}
												title={
													embeddingProviders.length === 0
														? "Configure an embedding-capable provider to enable semantic mode."
														: undefined
												}
											>
												Direct + Semantic
											</TabsTrigger>
										</TabsList>
									</Tabs>
									<p className="text-muted-foreground text-xs">
										{mode === "direct" ? (
											<>
												Direct-only mode hashes each request and replays an exact match. No embeddings, no provider needed.
												Cheapest path, perfect for stable prompts.
											</>
										) : (
											<>
												Direct + semantic mode adds vector similarity search on top of direct hash matching. Requires an
												embedding-capable provider and the model&apos;s real dimension. Direct hits are still served first;
												semantic search runs only when the direct lookup misses.
											</>
										)}
									</p>
								</div>

								{validationError && (
									<div className="border-destructive/40 bg-destructive/10 text-destructive rounded-md border p-3 text-xs">
										{validationError}
									</div>
								)}

								{/* Provider/model/dimension only appear in semantic mode. */}
								{mode === "semantic" && (
									<>
										{hasStructuralChange && (
											<div className="rounded-md border border-amber-200 bg-amber-50 p-3 text-xs text-amber-900">
												<b>Heads up:</b> a vector store namespace can only hold vectors of <em>one</em> dimension. Whenever you
												change the embedding <b>provider</b>, <b>model</b>, or <b>dimension</b>, make sure the <b>dimension</b>{" "}
												still matches what the model produces, otherwise writes to the existing namespace will fail and reads
												will silently miss. The namespace is <em>not</em> recreated automatically; either use a fresh namespace
												or drop the existing class/index in your vector store before saving.
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
														<SelectTrigger className="w-full">
															<SelectValue placeholder="Select provider" />
														</SelectTrigger>
														<SelectContent>
															{embeddingProviders
																.filter((provider) => provider.name)
																.map((provider) => (
																	<SelectItem key={provider.name} value={provider.name}>
																		<div className="flex items-center gap-2">
																			<RenderProviderIcon
																				provider={provider.name as ProviderIconType}
																				size="sm"
																				className="h-4 w-4"
																			/>
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
														onChange={(model) => updateLocal({ embedding_model: model })}
														placeholder={cacheConfig.provider ? "Search or type an embedding model..." : "Select a provider first"}
														disabled={!cacheConfig.provider}
													/>
												</div>
											</div>
											<p className="text-muted-foreground text-xs">
												API keys are inherited from the embedding provider&apos;s main configuration, you don&apos;t need to
												add them again here.
											</p>
											<div className="space-y-2">
												<Label htmlFor="dimension">Dimension</Label>
												<Input
													id="dimension"
													type="number"
													min="2"
													value={
														cacheConfig.dimension === undefined || Number.isNaN(cacheConfig.dimension) ? "" : cacheConfig.dimension
													}
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
													Vector size produced by the embedding model. Must match the model exactly (e.g. <code>1536</code>{" "}
													for OpenAI <code>text-embedding-3-small</code>, <code>3072</code> for{" "}
													<code>text-embedding-3-large</code>, <code>768</code> for many Cohere/Voyage models).
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
													type="number"
													min="0"
													max="1"
													step="0.01"
													value={
														cacheConfig.threshold === undefined || Number.isNaN(cacheConfig.threshold) ? "" : cacheConfig.threshold
													}
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
													Minimum cosine similarity for a semantic hit. Override per-request via{" "}
													<b>x-bf-cache-threshold</b>.
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
												type="text"
												placeholder="BifrostLocalCachePlugin"
												value={cacheConfig.vector_store_namespace ?? ""}
												onChange={(e) => updateLocal({ vector_store_namespace: e.target.value })}
											/>
											<p className="text-muted-foreground text-xs">
												Bucket/index name where cache entries live. Leave blank to use the default (
												<code>BifrostLocalCachePlugin</code>). Changing this points the plugin at a different (possibly empty)
												bucket. Old entries are not deleted, they just stop being queried.
											</p>
										</div>
										<div className="space-y-2">
											<Label htmlFor="default_cache_key">Default Cache Key</Label>
											<Input
												id="default_cache_key"
												type="text"
												placeholder="(none)"
												value={cacheConfig.default_cache_key ?? ""}
												onChange={(e) => updateLocal({ default_cache_key: e.target.value })}
											/>
											<p className="text-muted-foreground text-xs">
												Fallback partition key used when a request doesn&apos;t set <b>x-bf-cache-key</b>. Cache keys isolate
												entries: same key ↔ shared cache pool. Leave blank to <b>disable caching</b> for any request that
												doesn&apos;t send the header.
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
												type="number"
												min="1"
												max="50"
												value={cacheConfig.conversation_history_threshold || 3}
												onChange={(e) =>
													updateLocal({ conversation_history_threshold: parseInt(e.target.value) || 3 })
												}
											/>
											<p className="text-muted-foreground text-xs">
												Skip caching for conversations with more than this many messages. Long histories rarely match exactly
												and inflate the cache without paying off.
											</p>
										</div>
									</div>
									<div className="space-y-2">
										<div className="flex h-fit items-center justify-between space-x-2 rounded-lg border p-3">
											<div className="space-y-0.5">
												<Label className="text-sm font-medium">Exclude System Prompt</Label>
												<p className="text-muted-foreground text-xs">Strip system messages from the cache key.</p>
											</div>
											<Switch
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
										<div className="flex items-center justify-between space-x-2 rounded-lg border p-3">
											<div className="space-y-0.5">
												<Label className="text-sm font-medium">Cache by Model</Label>
												<p className="text-muted-foreground text-xs">
													Include model name in the cache key. Different models won&apos;t share cached responses.
												</p>
											</div>
											<Switch
												checked={cacheConfig.cache_by_model}
												onCheckedChange={(checked) => updateLocal({ cache_by_model: checked })}
												size="md"
											/>
										</div>
										<div className="flex items-center justify-between space-x-2 rounded-lg border p-3">
											<div className="space-y-0.5">
												<Label className="text-sm font-medium">Cache by Provider</Label>
												<p className="text-muted-foreground text-xs">
													Include provider name in the cache key. Different providers won&apos;t share cached responses.
												</p>
											</div>
											<Switch
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

								{/* Danger zone. cleanup_on_shutdown is destructive: it deletes
								    every entry tagged from_bifrost_local_cache_plugin and drops
								    the namespace on plugin Cleanup. Surfaced behind explicit
								    visual treatment so it isn't toggled accidentally. */}
								<div className="space-y-4">
									<h3 className="text-destructive text-sm font-medium">Danger Zone</h3>
									<div className="border-destructive/40 bg-destructive/5 flex items-center justify-between space-x-2 rounded-lg border p-3">
										<div className="space-y-0.5">
											<Label className="text-sm font-medium">Cleanup on Shutdown</Label>
											<p className="text-muted-foreground text-xs">
												On every plugin shutdown, delete every cache entry tagged{" "}
												<code>from_bifrost_local_cache_plugin</code> and drop the vector store namespace. Plugin shutdown
												happens on server restart <em>and</em> whenever you toggle <b>Enable Local Cache</b> off.{" "}
												<span className="text-destructive font-medium">
													Destructive: every cached entry is lost the next time the plugin shuts down.
												</span>{" "}
												Default off so cache survives restarts and toggle flips.
											</p>
										</div>
										<Switch
											checked={cacheConfig.cleanup_on_shutdown || false}
											onCheckedChange={(checked) => updateLocal({ cleanup_on_shutdown: checked })}
											size="md"
										/>
									</div>
								</div>
							</div>

							<div className="flex justify-end pt-2">
								<Button onClick={handleSave} disabled={!hasUnsavedConfigChanges || isSaving || Boolean(validationError)}>
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
