import PageTitle from "@/components/pageTitle";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ModelMultiselect } from "@/components/ui/modelMultiselect";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { getProviderLabel } from "@/lib/constants/logs";
import { getErrorMessage } from "@/lib/store";
import {
	useCancelWarpBackfillMutation,
	useGetWarpBackfillStatusQuery,
	useGetWarpConfigQuery,
	useStartWarpBackfillMutation,
	useUpdateWarpConfigMutation,
} from "@/lib/store/apis/warpApi";
import { useGetProviderKeysQuery, useGetProvidersQuery } from "@/lib/store/apis/providersApi";
import type { WarpConfigInput } from "@/lib/types/warp";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { AlertTriangle, CheckCircle2, Database, Loader2 } from "lucide-react";
import { useEffect, useState } from "react";
import { toast } from "sonner";
import { embeddingSpaceChanged, supportsWarpEmbedding, validateWarpEmbedding, type WarpEmbeddingFields } from "./warpConfig.utils";

/**
 * Warp talks to Bifrost itself by default.
 *
 * Pointing base_url at this deployment means Warp reaches its model through the
 * gateway, using the provider credentials already configured here. That is why
 * the API key below is optional: for the default setup there is no second
 * credential to supply.
 *
 * The /openai suffix matters. Warp sends OpenAI-shaped requests, and the
 * provider appends its own path - so the base has to be the origin's
 * OpenAI-compatible mount, giving /openai/v1/responses. Pointed at the bare
 * origin it would resolve to /v1/responses, which this server does not serve.
 * Routing through the compatibility layer is also what keeps Warp working
 * against any configured provider rather than only OpenAI.
 */
const defaultBaseUrl = () => (typeof window === "undefined" ? "" : `${window.location.origin}/openai`);

/**
 * Sentinel for "any key". Radix rejects an empty-string SelectItem value, so the
 * unpinned default needs a stand-in that never reaches the form or the API.
 */
const WARP_ANY_KEY = "__any__";

const DEFAULT_MAX_ITERATIONS = 8;
const DEFAULT_TIMEOUT_SECONDS = 120;
const DEFAULT_EMBEDDING_DIMENSION = 1536;
const DEFAULT_VECTOR_NAMESPACE = "BifrostWarpLogs";
const DEFAULT_SEARCH_THRESHOLD = 0.8;
const DEFAULT_SEARCH_LIMIT = 10;

const localDateTimeValue = (date: Date) => {
	const local = new Date(date.getTime() - date.getTimezoneOffset() * 60_000);
	return local.toISOString().slice(0, 16);
};

/** Everything the form edits, in one place. */
interface WarpFormState {
	enabled: boolean;
	provider: string;
	model: string;
	apiKeyID: string;
	baseURL: string;
	maxIterations: number;
	requestTimeoutSeconds: number;
	systemPromptSuffix: string;
	embeddingProvider: string;
	embeddingModel: string;
	embeddingAPIKeyID: string;
	embeddingDimension: number;
	namespace: string;
	threshold: number;
	searchLimit: number;
}

const EMPTY_FORM: WarpFormState = {
	enabled: false,
	provider: "",
	model: "",
	apiKeyID: "",
	baseURL: "",
	maxIterations: DEFAULT_MAX_ITERATIONS,
	requestTimeoutSeconds: DEFAULT_TIMEOUT_SECONDS,
	systemPromptSuffix: "",
	embeddingProvider: "",
	embeddingModel: "",
	embeddingAPIKeyID: "",
	embeddingDimension: DEFAULT_EMBEDDING_DIMENSION,
	namespace: DEFAULT_VECTOR_NAMESPACE,
	threshold: DEFAULT_SEARCH_THRESHOLD,
	searchLimit: DEFAULT_SEARCH_LIMIT,
};

/**
 * Warp's settings.
 *
 * Deliberately plain React state rather than react-hook-form.
 *
 * The provider, model and key controls are custom components with no <input> of
 * their own, and every RHF binding tried for them - setValue plus watch,
 * explicit register, Controller - left those three selects painting an empty
 * value after a saved config loaded, while every field backed by a real input
 * hydrated correctly. cachingView drives the same provider/model pair from plain
 * state and works. One source of truth, one hydration point, nothing sitting
 * between the value and the control.
 */
export default function WarpView() {
	const hasSettingsUpdateAccess = useRbac(RbacResource.Settings, RbacOperation.Update);
	const { data: config, isLoading: isLoadingConfig } = useGetWarpConfigQuery();
	const { data: providersData } = useGetProvidersQuery();
	const [updateWarpConfig, { isLoading: isSaving }] = useUpdateWarpConfigMutation();
	const [startWarpBackfill, { isLoading: isStartingBackfill }] = useStartWarpBackfillMutation();
	const [cancelWarpBackfill, { isLoading: isCancellingBackfill }] = useCancelWarpBackfillMutation();

	const [form, setForm] = useState<WarpFormState>(EMPTY_FORM);
	const [activeBackfillID, setActiveBackfillID] = useState<string | null>(null);
	const [backfillStart, setBackfillStart] = useState(() => localDateTimeValue(new Date(Date.now() - 7 * 24 * 60 * 60 * 1000)));
	const [backfillEnd, setBackfillEnd] = useState(() => localDateTimeValue(new Date()));

	const providers = providersData ?? [];
	const embeddingProviders = providers.filter(supportsWarpEmbedding);
	// Keys are provider-scoped, so the query waits for a provider rather than
	// firing a request for "".
	const { data: providerKeysData } = useGetProviderKeysQuery(form.provider, { skip: !form.provider });
	const providerKeys = providerKeysData ?? [];
	const { data: embeddingProviderKeysData } = useGetProviderKeysQuery(form.embeddingProvider, { skip: !form.embeddingProvider });
	const embeddingProviderKeys = embeddingProviderKeysData ?? [];
	const { data: backfillStatus } = useGetWarpBackfillStatusQuery(activeBackfillID ? { id: activeBackfillID } : undefined, {
		skip: !hasSettingsUpdateAccess || !config?.configured,
		pollingInterval: activeBackfillID ? 2000 : 10000,
	});
	const isBackfillActive =
		backfillStatus?.status === "pending" || backfillStatus?.status === "running" || backfillStatus?.status === "cancelling";

	// One hydration point. Everything the form shows comes from here.
	useEffect(() => {
		if (!config) return;
		setForm({
			enabled: config.enabled,
			provider: config.provider ?? "",
			model: config.model ?? "",
			apiKeyID: config.api_key_id ?? "",
			baseURL: config.base_url || defaultBaseUrl(),
			maxIterations: config.max_iterations || DEFAULT_MAX_ITERATIONS,
			requestTimeoutSeconds: config.request_timeout_seconds || DEFAULT_TIMEOUT_SECONDS,
			systemPromptSuffix: config.system_prompt_suffix ?? "",
			embeddingProvider: config.embedding_provider ?? "",
			embeddingModel: config.embedding_model ?? "",
			embeddingAPIKeyID: config.embedding_api_key_id ?? "",
			embeddingDimension: config.embedding_dimension || DEFAULT_EMBEDDING_DIMENSION,
			namespace: config.log_vector_store_namespace || DEFAULT_VECTOR_NAMESPACE,
			threshold: config.semantic_search_threshold || DEFAULT_SEARCH_THRESHOLD,
			searchLimit: config.semantic_search_limit || DEFAULT_SEARCH_LIMIT,
		});
	}, [config]);

	const update = <K extends keyof WarpFormState>(key: K, value: WarpFormState[K]) => setForm((current) => ({ ...current, [key]: value }));

	const hasChanges =
		!!config &&
		(form.enabled !== config.enabled ||
			form.provider !== (config.provider ?? "") ||
			form.model !== (config.model ?? "") ||
			form.apiKeyID !== (config.api_key_id ?? "") ||
			form.baseURL !== (config.base_url || defaultBaseUrl()) ||
			form.maxIterations !== config.max_iterations ||
			form.requestTimeoutSeconds !== config.request_timeout_seconds ||
			form.systemPromptSuffix !== (config.system_prompt_suffix ?? "") ||
			form.embeddingProvider !== (config.embedding_provider ?? "") ||
			form.embeddingModel !== (config.embedding_model ?? "") ||
			form.embeddingAPIKeyID !== (config.embedding_api_key_id ?? "") ||
			form.embeddingDimension !== config.embedding_dimension ||
			form.namespace !== config.log_vector_store_namespace ||
			form.threshold !== config.semantic_search_threshold ||
			form.searchLimit !== config.semantic_search_limit);

	// The server enforces the same rules; checking here only saves a round trip.
	const missingRequired = form.enabled && (!form.provider || !form.model);
	const baseURLInvalid = form.baseURL !== "" && !/^https?:\/\//.test(form.baseURL);
	const iterationsInvalid = form.maxIterations < 1 || form.maxIterations > 20;
	const timeoutInvalid = form.requestTimeoutSeconds < 1;
	const embeddingFields: WarpEmbeddingFields = form;
	const embeddingValidation = validateWarpEmbedding(embeddingFields, form.enabled, config?.vector_store_connected ?? false);
	const savedEmbeddingFields: WarpEmbeddingFields = {
		embeddingProvider: config?.embedding_provider ?? "",
		embeddingModel: config?.embedding_model ?? "",
		embeddingDimension: config?.embedding_dimension ?? 0,
		namespace: config?.log_vector_store_namespace ?? DEFAULT_VECTOR_NAMESPACE,
		threshold: config?.semantic_search_threshold ?? DEFAULT_SEARCH_THRESHOLD,
		searchLimit: config?.semantic_search_limit ?? DEFAULT_SEARCH_LIMIT,
	};
	const needsNewNamespace =
		!!config?.configured &&
		embeddingSpaceChanged(embeddingFields, savedEmbeddingFields) &&
		form.namespace === savedEmbeddingFields.namespace;
	const invalid = missingRequired || baseURLInvalid || iterationsInvalid || timeoutInvalid || !!embeddingValidation || needsNewNamespace;

	const onSubmit = async (event: React.FormEvent) => {
		event.preventDefault();
		if (invalid || !hasChanges) return;

		const payload: WarpConfigInput = {
			enabled: form.enabled,
			provider: form.provider.trim(),
			model: form.model.trim(),
			api_key_id: form.apiKeyID,
			base_url: form.baseURL.trim(),
			max_iterations: form.maxIterations,
			request_timeout_seconds: form.requestTimeoutSeconds,
			system_prompt_suffix: form.systemPromptSuffix,
			embedding_provider: form.embeddingProvider.trim(),
			embedding_model: form.embeddingModel.trim(),
			embedding_api_key_id: form.embeddingAPIKeyID,
			embedding_dimension: form.embeddingDimension,
			log_vector_store_namespace: form.namespace.trim(),
			semantic_search_threshold: form.threshold,
			semantic_search_limit: form.searchLimit,
		};
		try {
			await updateWarpConfig(payload).unwrap();
			toast.success("Warp configuration saved.");
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const onStartBackfill = async () => {
		if (!backfillStart || !backfillEnd || new Date(backfillStart) >= new Date(backfillEnd)) {
			toast.error("Choose a valid backfill time range.");
			return;
		}
		try {
			const status = await startWarpBackfill({
				start_time: new Date(backfillStart).toISOString(),
				end_time: new Date(backfillEnd).toISOString(),
			}).unwrap();
			setActiveBackfillID(status.id ?? null);
			toast.success("Warp embedding backfill started.");
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const onCancelBackfill = async () => {
		try {
			await cancelWarpBackfill(activeBackfillID ? { id: activeBackfillID } : undefined).unwrap();
			toast.success("Backfill cancellation requested.");
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	return (
		<div className="mx-auto w-full max-w-7xl space-y-4" data-testid="warp-config-view">
			<form onSubmit={onSubmit} className="space-y-4">
				<PageTitle title="Warp">
					Warp answers questions about your Bifrost data in natural language. It runs on its own model, configured here and kept separate
					from the providers Bifrost serves to your traffic.
				</PageTitle>

				{/* Alpha is stated here as well as in the panel: this is where someone
				    decides whether to turn Warp on for everyone, so it is the moment the
				    maturity signal actually informs a decision. */}
				<div className="flex items-center gap-2">
					<Badge variant="secondary">ALPHA</Badge>
					<p className="text-muted-foreground text-xs">Warp is early. Check its numbers against the dashboard before acting on them.</p>
				</div>

				{isLoadingConfig ? (
					<p className="text-muted-foreground text-sm">Loading Warp configuration...</p>
				) : (
					<div className="space-y-4">
						<div className="space-y-2 rounded-sm border p-4">
							<div className="flex items-center justify-between gap-4">
								<div className="space-y-0.5">
									<Label htmlFor="warp-enabled">Enable Warp</Label>
									<p className="text-muted-foreground text-sm">
										Adds the Warp panel to the dashboard. Warp reads logs, metrics and usage data from Bifrost on behalf of whoever asks,
										scoped to what that person is already allowed to see.
									</p>
								</div>
								<Switch
									id="warp-enabled"
									size="md"
									data-testid="warp-enabled-switch"
									checked={form.enabled}
									disabled={!hasSettingsUpdateAccess || (!config?.vector_store_connected && !form.enabled)}
									onCheckedChange={(checked) => update("enabled", checked)}
								/>
							</div>
							{/* A complete but switched-off config saves happily and then leaves
							    the panel saying Warp is unavailable, with nothing on this page
							    admitting why. Say it here, next to the switch that causes it. */}
							{!form.enabled && !!form.provider && !!form.model && (
								<p className="text-muted-foreground text-xs" data-testid="warp-disabled-hint">
									Everything below is filled in, but Warp stays hidden until this is on.
								</p>
							)}
							{!config?.vector_store_connected && (
								<p className="text-destructive flex items-center gap-1.5 text-xs" data-testid="warp-vector-store-required">
									<AlertTriangle className="h-3.5 w-3.5" /> Connect a vector store in Settings before enabling Warp.
								</p>
							)}
						</div>

						<div className="space-y-2 rounded-sm border p-4">
							<div className="space-y-0.5">
								<Label htmlFor="warp-provider">Provider</Label>
								<p className="text-muted-foreground text-sm">
									Which of your configured providers runs Warp. Only providers already set up in Bifrost are listed, so Warp cannot be
									pointed at one that does not exist.
								</p>
							</div>
							<Select
								value={form.provider}
								onValueChange={(value) => {
									// Ignore an empty value. No item here has one, so Radix only
									// emits "" when it decides the current value matches nothing -
									// which happens for one render as the fetched provider list
									// arrives and the fallback item below is swapped for the real
									// one. Acting on it wipes the saved provider a moment after it
									// hydrated, which is exactly what it looked like: the value
									// loaded, then vanished.
									if (!value) return;
									// Model and key are provider-scoped, so values carried over from
									// the previous provider would be silently invalid.
									setForm((current) => ({ ...current, provider: value, model: "", apiKeyID: "" }));
								}}
								disabled={!hasSettingsUpdateAccess}
							>
								<SelectTrigger className="w-full" id="warp-provider" data-testid="warp-provider-select">
									<SelectValue placeholder="Select provider" />
								</SelectTrigger>
								<SelectContent>
									{/* The saved provider is listed even when it is missing from the
									    fetched list - the list may still be loading, or the provider
									    may have been removed since. Radix renders a Select with no
									    matching item as its placeholder, which is indistinguishable
									    from "nothing was ever saved". */}
									{form.provider && !providers.some((provider) => provider.name === form.provider) && (
										<SelectItem value={form.provider}>
											<div className="flex items-center gap-2">
												<RenderProviderIcon provider={form.provider as ProviderIconType} size="sm" className="h-4 w-4" />
												<span>{getProviderLabel(form.provider)}</span>
											</div>
										</SelectItem>
									)}
									{providers
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

						<div className="space-y-2 rounded-sm border p-4">
							<div className="space-y-0.5">
								<Label htmlFor="warp-model">Model</Label>
								<p className="text-muted-foreground text-sm">
									Warp reasons over query results and writes the answer, so a capable model pays for itself here.
								</p>
							</div>
							<ModelMultiselect
								inputId="warp-model"
								data-testid="warp-model-select"
								isSingleSelect
								provider={form.provider || undefined}
								value={form.model}
								onChange={(model) => update("model", model)}
								placeholder={form.provider ? "Search or type a model..." : "Select a provider first"}
								disabled={!form.provider || !hasSettingsUpdateAccess}
							/>
						</div>

						<div className="space-y-2 rounded-sm border p-4">
							<div className="space-y-0.5">
								<Label htmlFor="warp-api-key-id">API Key</Label>
								<p className="text-muted-foreground text-sm">
									Warp holds no credential of its own - it reaches its model through Bifrost, which supplies the key. Leave this on Any key
									to let Bifrost load-balance across the provider&apos;s pool, or pin one to isolate Warp&apos;s traffic to a single key.
								</p>
							</div>
							<Select
								value={form.apiKeyID || WARP_ANY_KEY}
								onValueChange={(value) => {
									// Same reasoning as the provider select: "" is Radix losing track
									// of the value, not a choice. The real "no key" answer is the
									// sentinel.
									if (!value) return;
									update("apiKeyID", value === WARP_ANY_KEY ? "" : value);
								}}
								disabled={!form.provider || !hasSettingsUpdateAccess}
							>
								<SelectTrigger className="w-full" id="warp-api-key-id" data-testid="warp-api-key-select">
									<SelectValue placeholder={form.provider ? "Any key" : "Select a provider first"} />
								</SelectTrigger>
								<SelectContent>
									{/* Radix forbids an empty-string SelectItem value, so the unpinned
									    default needs a sentinel, mapped back to "" before it leaves.
									    Listing it first makes it the obvious default. */}
									<SelectItem value={WARP_ANY_KEY}>Any key</SelectItem>
									{/* Same reasoning as the provider list: a pinned key missing from
									    the fetched set still has to show, or it silently reads as Any
									    key here while staying pinned on the server. */}
									{form.apiKeyID && !providerKeys.some((providerKey) => providerKey.id === form.apiKeyID) && (
										<SelectItem value={form.apiKeyID}>{form.apiKeyID}</SelectItem>
									)}
									{providerKeys.map((providerKey) => (
										<SelectItem key={providerKey.id} value={providerKey.id}>
											{providerKey.name || providerKey.id}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
							{form.provider && providerKeys.length === 0 && (
								<p className="text-muted-foreground text-xs">This provider has no keys configured, which is fine if it needs none.</p>
							)}
						</div>

						<div className="space-y-2 rounded-sm border p-4">
							<div className="flex items-start justify-between gap-4">
								<div className="space-y-0.5">
									<Label>Conversation search</Label>
									<p className="text-muted-foreground text-sm">
										Warp embeds completed conversations and stores those vectors in your connected vector store, then uses them to find logs
										by meaning.
									</p>
								</div>
								<Badge variant={config?.vector_store_connected ? "secondary" : "destructive"} data-testid="warp-vector-store-status">
									{config?.vector_store_connected ? (
										<CheckCircle2 className="mr-1 h-3.5 w-3.5" />
									) : (
										<Database className="mr-1 h-3.5 w-3.5" />
									)}
									{config?.vector_store_connected ? "Vector store connected" : "Vector store disconnected"}
								</Badge>
							</div>

							<div className="grid gap-4 pt-2 md:grid-cols-2">
								<div className="space-y-2">
									<Label htmlFor="warp-embedding-provider">Embedding Provider</Label>
									<Select
										value={form.embeddingProvider}
										onValueChange={(value) => {
											if (!value) return;
											setForm((current) => ({ ...current, embeddingProvider: value, embeddingModel: "", embeddingAPIKeyID: "" }));
										}}
										disabled={!hasSettingsUpdateAccess}
									>
										<SelectTrigger id="warp-embedding-provider" data-testid="warp-embedding-provider-select">
											<SelectValue placeholder="Select embedding provider" />
										</SelectTrigger>
										<SelectContent>
											{form.embeddingProvider && !embeddingProviders.some((provider) => provider.name === form.embeddingProvider) && (
												<SelectItem value={form.embeddingProvider}>{getProviderLabel(form.embeddingProvider)}</SelectItem>
											)}
											{embeddingProviders.map((provider) => (
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
									<Label htmlFor="warp-embedding-model">Embedding Model</Label>
									<ModelMultiselect
										inputId="warp-embedding-model"
										data-testid="warp-embedding-model-select"
										isSingleSelect
										provider={form.embeddingProvider || undefined}
										value={form.embeddingModel}
										onChange={(model) => update("embeddingModel", model)}
										placeholder={form.embeddingProvider ? "Search or type an embedding model..." : "Select a provider first"}
										disabled={!form.embeddingProvider || !hasSettingsUpdateAccess}
									/>
								</div>

								<div className="space-y-2">
									<Label htmlFor="warp-embedding-api-key">Embedding API Key</Label>
									<Select
										value={form.embeddingAPIKeyID || WARP_ANY_KEY}
										onValueChange={(value) => value && update("embeddingAPIKeyID", value === WARP_ANY_KEY ? "" : value)}
										disabled={!form.embeddingProvider || !hasSettingsUpdateAccess}
									>
										<SelectTrigger id="warp-embedding-api-key" data-testid="warp-embedding-api-key-select">
											<SelectValue placeholder="Any key" />
										</SelectTrigger>
										<SelectContent>
											<SelectItem value={WARP_ANY_KEY}>Any key</SelectItem>
											{form.embeddingAPIKeyID && !embeddingProviderKeys.some((key) => key.id === form.embeddingAPIKeyID) && (
												<SelectItem value={form.embeddingAPIKeyID}>{form.embeddingAPIKeyID}</SelectItem>
											)}
											{embeddingProviderKeys.map((key) => (
												<SelectItem key={key.id} value={key.id}>
													{key.name || key.id}
												</SelectItem>
											))}
										</SelectContent>
									</Select>
								</div>

								<div className="space-y-2">
									<Label htmlFor="warp-embedding-dimension">Embedding Dimension</Label>
									<Input
										id="warp-embedding-dimension"
										type="number"
										min={1}
										data-testid="warp-embedding-dimension-input"
										value={form.embeddingDimension}
										onChange={(event) => update("embeddingDimension", Number(event.target.value))}
										disabled={!hasSettingsUpdateAccess}
									/>
								</div>

								<div className="space-y-2 md:col-span-2">
									<Label htmlFor="warp-vector-namespace">Log embedding namespace</Label>
									<Input
										id="warp-vector-namespace"
										data-testid="warp-vector-namespace-input"
										value={form.namespace}
										onChange={(event) => update("namespace", event.target.value)}
										disabled={!hasSettingsUpdateAccess}
									/>
									<p className="text-muted-foreground text-xs">
										Only vector embeddings and operational metadata are stored here; plaintext conversation content stays in the log store.
									</p>
								</div>

								<div className="space-y-2">
									<Label htmlFor="warp-search-threshold">Similarity Threshold</Label>
									<Input
										id="warp-search-threshold"
										type="number"
										min={0.01}
										max={1}
										step={0.01}
										data-testid="warp-search-threshold-input"
										value={form.threshold}
										onChange={(event) => update("threshold", Number(event.target.value))}
										disabled={!hasSettingsUpdateAccess}
									/>
								</div>
								<div className="space-y-2">
									<Label htmlFor="warp-search-limit">Maximum Matches</Label>
									<Input
										id="warp-search-limit"
										type="number"
										min={1}
										max={25}
										data-testid="warp-search-limit-input"
										value={form.searchLimit}
										onChange={(event) => update("searchLimit", Number(event.target.value))}
										disabled={!hasSettingsUpdateAccess}
									/>
								</div>
							</div>

							{needsNewNamespace && (
								<p className="text-destructive flex items-center gap-1.5 text-sm" data-testid="warp-namespace-change-warning">
									<AlertTriangle className="h-4 w-4" /> Provider, model, or dimension changed. Choose a new namespace so incompatible
									vectors cannot mix.
								</p>
							)}
							{embeddingValidation && (
								<p className="text-destructive text-sm" data-testid="warp-embedding-validation">
									{embeddingValidation}
								</p>
							)}
						</div>

						<div className="space-y-2 rounded-sm border p-4">
							<div className="space-y-0.5">
								<Label htmlFor="warp-base-url">Base URL</Label>
								<p className="text-muted-foreground text-sm">
									Defaults to this Bifrost, so Warp reaches its model through your own gateway and reuses the credentials already configured
									here. Point it elsewhere only to call a provider directly.
								</p>
							</div>
							<Input
								id="warp-base-url"
								type="text"
								placeholder="https://llm.internal.example.com/v1"
								data-testid="warp-base-url-input"
								className={baseURLInvalid ? "border-destructive" : ""}
								value={form.baseURL}
								onChange={(event) => update("baseURL", event.target.value)}
								disabled={!hasSettingsUpdateAccess}
							/>
							{baseURLInvalid && <p className="text-destructive text-sm">URL must start with http:// or https://</p>}
						</div>

						<div className="space-y-2 rounded-sm border p-4">
							<div className="space-y-0.5">
								<Label htmlFor="warp-max-iterations">Max Iterations</Label>
								<p className="text-muted-foreground text-sm">
									How many times Warp may query your data and reconsider before it has to answer. Each iteration is a billable round trip,
									so this is a cost ceiling as much as a quality setting.
								</p>
							</div>
							<Input
								id="warp-max-iterations"
								type="number"
								data-testid="warp-max-iterations-input"
								className={iterationsInvalid ? "border-destructive" : ""}
								value={form.maxIterations}
								onChange={(event) => update("maxIterations", Number(event.target.value))}
								disabled={!hasSettingsUpdateAccess}
							/>
							{iterationsInvalid && <p className="text-destructive text-sm">Must be between 1 and 20</p>}
						</div>

						<div className="space-y-2 rounded-sm border p-4">
							<div className="space-y-0.5">
								<Label htmlFor="warp-request-timeout">Request Timeout (seconds)</Label>
								<p className="text-muted-foreground text-sm">
									Bound on a single call to the model. Raise it for slower self-hosted models.
								</p>
							</div>
							<Input
								id="warp-request-timeout"
								type="number"
								data-testid="warp-request-timeout-input"
								className={timeoutInvalid ? "border-destructive" : ""}
								value={form.requestTimeoutSeconds}
								onChange={(event) => update("requestTimeoutSeconds", Number(event.target.value))}
								disabled={!hasSettingsUpdateAccess}
							/>
							{timeoutInvalid && <p className="text-destructive text-sm">Must be at least 1 second</p>}
						</div>

						<div className="space-y-2 rounded-sm border p-4">
							<div className="space-y-0.5">
								<Label htmlFor="warp-system-prompt-suffix">Additional Instructions</Label>
								<p className="text-muted-foreground text-sm">
									Appended to Warp&apos;s built-in instructions. Useful for local naming conventions or how your teams are organised. It
									adds to the built-in prompt and cannot replace it.
								</p>
							</div>
							<Input
								id="warp-system-prompt-suffix"
								type="text"
								placeholder="Costs are in USD. Team IDs map to squads in Notion."
								data-testid="warp-system-prompt-suffix-input"
								value={form.systemPromptSuffix}
								onChange={(event) => update("systemPromptSuffix", event.target.value)}
								disabled={!hasSettingsUpdateAccess}
							/>
						</div>

						<div className="space-y-4 rounded-sm border p-4" data-testid="warp-backfill-section">
							<div className="space-y-0.5">
								<Label>Backfill conversation embeddings</Label>
								<p className="text-muted-foreground text-sm">
									Index completed conversations from an existing log window. This runs in Sidekiq, can be cancelled, and safely resumes
									through batches.
								</p>
							</div>
							<div className="grid gap-4 md:grid-cols-2">
								<div className="space-y-2">
									<Label htmlFor="warp-backfill-start">Start</Label>
									<Input
										id="warp-backfill-start"
										type="datetime-local"
										data-testid="warp-backfill-start-input"
										value={backfillStart}
										onChange={(event) => setBackfillStart(event.target.value)}
										disabled={isBackfillActive || !hasSettingsUpdateAccess}
									/>
								</div>
								<div className="space-y-2">
									<Label htmlFor="warp-backfill-end">End</Label>
									<Input
										id="warp-backfill-end"
										type="datetime-local"
										data-testid="warp-backfill-end-input"
										value={backfillEnd}
										onChange={(event) => setBackfillEnd(event.target.value)}
										disabled={isBackfillActive || !hasSettingsUpdateAccess}
									/>
								</div>
							</div>

							{backfillStatus?.id && (
								<div className="bg-muted/40 space-y-2 rounded-sm p-3 text-sm" data-testid="warp-backfill-status">
									<div className="flex items-center justify-between gap-3">
										<span className="font-medium capitalize">{backfillStatus.status}</span>
										<span className="text-muted-foreground">
											{backfillStatus.scanned} / {backfillStatus.total} scanned
										</span>
									</div>
									<div className="bg-border h-2 overflow-hidden rounded-full">
										<div
											className="bg-primary h-full transition-all"
											style={{
												width: `${backfillStatus.total > 0 ? Math.min(100, (backfillStatus.scanned / backfillStatus.total) * 100) : 0}%`,
											}}
										/>
									</div>
									<p className="text-muted-foreground text-xs">
										{backfillStatus.indexed} indexed · {backfillStatus.skipped} skipped · {backfillStatus.failed} failed
									</p>
									{backfillStatus.last_error && <p className="text-destructive text-xs">Latest error: {backfillStatus.last_error}</p>}
								</div>
							)}

							<div className="flex justify-end">
								{isBackfillActive ? (
									<Button
										type="button"
										variant="outline"
										onClick={onCancelBackfill}
										disabled={isCancellingBackfill || !hasSettingsUpdateAccess}
										data-testid="warp-backfill-cancel-btn"
									>
										{isCancellingBackfill && <Loader2 className="mr-2 h-4 w-4 animate-spin" />} Cancel backfill
									</Button>
								) : (
									<Button
										type="button"
										onClick={onStartBackfill}
										disabled={
											isStartingBackfill || !config?.configured || !config.vector_store_connected || !hasSettingsUpdateAccess || hasChanges
										}
										data-testid="warp-backfill-start-btn"
									>
										{isStartingBackfill && <Loader2 className="mr-2 h-4 w-4 animate-spin" />} Start backfill
									</Button>
								)}
							</div>
							{hasChanges && (
								<p className="text-muted-foreground text-right text-xs">Save configuration changes before starting a backfill.</p>
							)}
						</div>
					</div>
				)}

				<div className="flex justify-end gap-3 pt-2">
					{missingRequired && (
						<p className="text-muted-foreground self-center text-xs" data-testid="warp-missing-required">
							Choose a provider and model to enable Warp.
						</p>
					)}
					<Button type="submit" disabled={!hasChanges || isSaving || invalid || !hasSettingsUpdateAccess} data-testid="warp-save-btn">
						{isSaving ? "Saving..." : "Save Changes"}
					</Button>
				</div>
			</form>
		</div>
	);
}