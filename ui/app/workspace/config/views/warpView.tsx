import PageTitle from "@/components/pageTitle";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ModelSelector } from "@/components/ui/modelSelector";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { isFiniteNumber, isValidBaseURL, validateWarpRetentionDays } from "./warpView.utils";
import { Link } from "@tanstack/react-router";
import { ArrowRight, TriangleAlert } from "lucide-react";
import { getProviderLabel } from "@/lib/constants/logs";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { getErrorMessage } from "@/lib/store";
import {
	useCancelWarpBackfillMutation,
	useGetWarpBackfillStatusQuery,
	useGetWarpConfigQuery,
	useStartWarpBackfillMutation,
	useUpdateWarpConfigMutation,
} from "@/lib/store/apis/warpApi";
import { useGetProviderKeysQuery, useGetProvidersQuery } from "@/lib/store/apis/providersApi";
import {
	WARP_MAX_TEMPERATURE,
	WARP_MIN_TEMPERATURE,
	WARP_REASONING_EFFORTS,
	type WarpBackfillJob,
	type WarpConfigInput,
} from "@/lib/types/warp";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { AlertTriangle, CheckCircle2, Database, Loader2 } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import {
	embeddingSpaceChanged,
	normalizeWarpNamespace,
	supportsWarpEmbedding,
	validateWarpEmbedding,
	type WarpEmbeddingFields,
} from "./warpConfig.utils";

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

/** Same idea as WARP_ANY_KEY: Radix rejects an empty-string item value, so
 * "leave reasoning effort unset" needs its own stand-in. */
const WARP_REASONING_UNSET = "__unset__";

const DEFAULT_MAX_ITERATIONS = 8;
const DEFAULT_TIMEOUT_SECONDS = 120;
const DEFAULT_HISTORY_RETENTION_DAYS = 30;
const DEFAULT_TEMPERATURE = 1;
const DEFAULT_EMBEDDING_DIMENSION = 1536;
const DEFAULT_VECTOR_NAMESPACE = "BifrostWarpLogs";
const DEFAULT_SEARCH_THRESHOLD = 0.8;
const DEFAULT_SEARCH_LIMIT = 10;

/** "xhigh" -> "Xhigh". Good enough for a Select item; these are short, plain words. */
const capitalize = (value: string) => value.charAt(0).toUpperCase() + value.slice(1);

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
	historyRetentionDays: number;
	// temperatureEnabled decides whether temperature is sent at all - the value
	// itself is meaningless while disabled, but is kept around rather than
	// reset, so toggling back on restores what was there instead of resetting
	// to the default.
	temperatureEnabled: boolean;
	// "" is a cleared input, distinct from 0 - WARP_MIN_TEMPERATURE is 0, so
	// coercing a blank field straight to a number would silently read as a
	// valid, deliberately-chosen 0 instead of "still typing".
	temperature: number | "";
	// "" leaves reasoning_effort unset, same meaning as WARP_REASONING_UNSET on
	// the select item - this is the value actually sent, that one is UI-only.
	reasoningEffort: string;
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
	historyRetentionDays: DEFAULT_HISTORY_RETENTION_DAYS,
	temperatureEnabled: false,
	temperature: DEFAULT_TEMPERATURE,
	reasoningEffort: "",
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
	const { data: config, isLoading: isLoadingConfig, isError: isConfigError } = useGetWarpConfigQuery();
	const {
		data: providersData,
		isLoading: isProvidersLoading,
		isError: isProvidersError,
		refetch: refetchProviders,
	} = useGetProvidersQuery();
	const [updateWarpConfig, { isLoading: isSaving }] = useUpdateWarpConfigMutation();
	const [startWarpBackfill, { isLoading: isStartingBackfill }] = useStartWarpBackfillMutation();
	const [cancelWarpBackfill, { isLoading: isCancellingBackfill }] = useCancelWarpBackfillMutation();

	const [form, setForm] = useState<WarpFormState>(EMPTY_FORM);
	const [activeBackfillID, setActiveBackfillID] = useState<string | null>(null);
	// The last terminal result, held so it survives the switch to the id-less
	// status query. Cleared when a new backfill starts.
	const [finishedBackfill, setFinishedBackfill] = useState<WarpBackfillJob | null>(null);
	const [backfillStart, setBackfillStart] = useState(() => localDateTimeValue(new Date(Date.now() - 7 * 24 * 60 * 60 * 1000)));
	const [backfillEnd, setBackfillEnd] = useState(() => localDateTimeValue(new Date()));

	const providers = providersData ?? [];
	const embeddingProviders = providers.filter(supportsWarpEmbedding);

	// Memoized by the id, not rebuilt inline: every form edit rerenders this
	// component, and ModelSelector refetches whenever the `keys` reference
	// changes - so an inline array turned each unrelated edit into a models
	// request for the same key.
	const modelKeys = useMemo(() => (form.apiKeyID ? [form.apiKeyID] : undefined), [form.apiKeyID]);
	// Same reasoning for the embedding picker's pinned key.
	const embeddingModelKeys = useMemo(() => (form.embeddingAPIKeyID ? [form.embeddingAPIKeyID] : undefined), [form.embeddingAPIKeyID]);

	// Keys are provider-scoped, so the query waits for a provider rather than
	// firing a request for "".
	const {
		// currentData, not data: RTK Query keeps the previous argument's result
		// while it fetches the new one, so after switching provider the selector
		// briefly offered the old provider's keys - and saving one stored an
		// api_key_id that belongs to a different provider, which only fails later
		// at key selection.
		currentData: providerKeysData,
		// isFetching, not isLoading: isLoading is only true when there is nothing
		// cached at all, so it is false for exactly the refetch that matters here.
		isFetching: isKeysLoading,
		isError: isKeysError,
		refetch: refetchKeys,
	} = useGetProviderKeysQuery(form.provider, { skip: !form.provider });
	const providerKeys = providerKeysData ?? [];
	const {
		// currentData, for the same reason as the chat provider above: data holds
		// the previous provider's keys while the new request is in flight, so the
		// selector could offer - and store - a key belonging to a provider the
		// form no longer points at. Nothing downstream checks that membership.
		currentData: embeddingProviderKeysData,
		isFetching: isEmbeddingKeysLoading,
		isError: isEmbeddingKeysError,
		refetch: refetchEmbeddingKeys,
	} = useGetProviderKeysQuery(form.embeddingProvider, { skip: !form.embeddingProvider });
	const embeddingProviderKeys = embeddingProviderKeysData ?? [];
	const {
		data: backfillStatus,
		// A status request that has not landed yet is not "no job running": the
		// Start button was enabled during discovery, so a reload mid-backfill
		// offered a start that the server then rejected as a conflict.
		isLoading: isBackfillStatusLoading,
		// isFetching as well as isLoading: with a cached status the 10s poll
		// refreshes without isLoading ever going true, so a refresh that is about
		// to discover a running job left Start enabled - and the click landed on
		// the 409 the server returns for an in-flight backfill.
		isFetching: isBackfillStatusFetching,
		isError: isBackfillStatusError,
		refetch: refetchBackfillStatus,
	} = useGetWarpBackfillStatusQuery(activeBackfillID ? { id: activeBackfillID } : undefined, {
		// Not gated on config.configured: disabling Warp mid-backfill must not
		// hide the running job or its cancel action after a reload - the status
		// and cancel endpoints need admin access and the backfill stores, not a
		// currently-configured Warp.
		skip: !hasSettingsUpdateAccess,
		pollingInterval: activeBackfillID ? 2000 : 10000,
	});
	const isBackfillActive =
		backfillStatus?.status === "pending" || backfillStatus?.status === "running" || backfillStatus?.status === "cancelling";
	// A live job always wins; otherwise fall back to the run that just ended.
	const shownBackfill = backfillStatus?.id ? backfillStatus : finishedBackfill;

	// Adopt a job discovered by the id-less request. Without this a reload during
	// a running backfill kept polling id-less, and the moment the job finished
	// the endpoint answered "idle" instead of the terminal job - so the counters
	// and last_error vanished at exactly the point someone wants to read them.
	useEffect(() => {
		if (activeBackfillID || !backfillStatus?.id) return;
		if (backfillStatus.status === "pending" || backfillStatus.status === "running" || backfillStatus.status === "cancelling") {
			setActiveBackfillID(backfillStatus.id);
		}
	}, [activeBackfillID, backfillStatus?.id, backfillStatus?.status]);

	// Release the pinned id once the job has finished, or the view keeps asking
	// about a completed backfill every two seconds for as long as it stays open.
	// Keyed on the status string rather than isBackfillActive so this does not
	// depend on a value declared below it.
	useEffect(() => {
		const status = backfillStatus?.status;
		if (!status) return;
		if (status === "completed" || status === "failed" || status === "cancelled") {
			// Kept before the id is released. Releasing it switches the query to the
			// id-less request, which answers {status:"idle"} with no id for any
			// deployment with no active job - so the counters and last_error of the
			// run that just finished disappeared the moment it finished, which is
			// exactly when someone wants to read them.
			setFinishedBackfill(backfillStatus);
			setActiveBackfillID(null);
		}
	}, [backfillStatus?.status]);

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
			historyRetentionDays: config.history_retention_days || DEFAULT_HISTORY_RETENTION_DAYS,
			// config.temperature is absent for "unset", not 0 - undefined and null
			// both read as "not configured" here, since JSON only sends one of them
			// but either could arrive depending on what wrote the row.
			temperatureEnabled: config.temperature !== undefined && config.temperature !== null,
			temperature: config.temperature ?? DEFAULT_TEMPERATURE,
			reasoningEffort: config.reasoning_effort ?? "",
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
			// The same fallback hydration applied, or a config stored without these
			// fields reads as dirty the moment it loads and Save lights up before
			// anyone has touched anything.
			form.maxIterations !== (config.max_iterations || DEFAULT_MAX_ITERATIONS) ||
			form.requestTimeoutSeconds !== (config.request_timeout_seconds || DEFAULT_TIMEOUT_SECONDS) ||
			form.historyRetentionDays !== (config.history_retention_days || DEFAULT_HISTORY_RETENTION_DAYS) ||
			(form.temperatureEnabled ? form.temperature : undefined) !== (config.temperature ?? undefined) ||
			form.reasoningEffort !== (config.reasoning_effort ?? "") ||
			form.systemPromptSuffix !== (config.system_prompt_suffix ?? "") ||
			form.embeddingProvider !== (config.embedding_provider ?? "") ||
			form.embeddingModel !== (config.embedding_model ?? "") ||
			form.embeddingAPIKeyID !== (config.embedding_api_key_id ?? "") ||
			// The same fallbacks hydration applied above, or a config saved before
			// these fields existed reads as dirty the moment it loads.
			form.embeddingDimension !== (config.embedding_dimension || DEFAULT_EMBEDDING_DIMENSION) ||
			form.namespace !== (config.log_vector_store_namespace || DEFAULT_VECTOR_NAMESPACE) ||
			form.threshold !== (config.semantic_search_threshold || DEFAULT_SEARCH_THRESHOLD) ||
			form.searchLimit !== (config.semantic_search_limit || DEFAULT_SEARCH_LIMIT));

	// The server enforces the same rules; checking here only saves a round trip.
	const missingRequired = form.enabled && (!form.provider || !form.model);
	const baseURLInvalid = form.baseURL !== "" && !isValidBaseURL(form.baseURL);
	const iterationsInvalid = !isFiniteNumber(form.maxIterations) || form.maxIterations < 1 || form.maxIterations > 20;
	const timeoutInvalid = !isFiniteNumber(form.requestTimeoutSeconds) || form.requestTimeoutSeconds < 1;
	// No upper bound: the per-owner conversation cap already limits the table, so
	// how long a transcript stays readable is a policy choice with no ceiling.
	// Zero is the documented way to ask for the default, so it must not be
	// rejected: a `< 1` rule made that value unreachable once an operator had
	// typed a number, with no way back to default retention from the form.
	const retentionError = validateWarpRetentionDays(form.historyRetentionDays);
	const retentionInvalid = retentionError !== true;
	const temperatureInvalid =
		form.temperatureEnabled &&
		(form.temperature === "" || form.temperature < WARP_MIN_TEMPERATURE || form.temperature > WARP_MAX_TEMPERATURE);
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
		normalizeWarpNamespace(form.namespace) === normalizeWarpNamespace(savedEmbeddingFields.namespace);
	const invalid =
		missingRequired ||
		baseURLInvalid ||
		iterationsInvalid ||
		timeoutInvalid ||
		retentionInvalid ||
		temperatureInvalid ||
		!!embeddingValidation ||
		needsNewNamespace;

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
			history_retention_days: form.historyRetentionDays,
			// invalid (checked above) already blocks a cleared field from reaching
			// here, but the type still allows "" - narrow it explicitly rather than
			// send a value the API layer has to reject on its own.
			temperature: form.temperatureEnabled && form.temperature !== "" ? form.temperature : undefined,
			reasoning_effort: form.reasoningEffort || undefined,
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
			// A new run replaces the last one's result, so the panel never shows a
			// finished job beside a running one.
			setFinishedBackfill(null);
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
				) : isConfigError ? (
					// Without this branch a failed fetch falls through to the empty
					// form, which reads as "Warp is switched off" rather than "we
					// could not load it" - and Save stays disabled with no reason given.
					<p className="text-destructive text-sm" role="alert">
						Unable to load Warp configuration. Reload the page to try again.
					</p>
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
							{/* An empty dropdown reads as "this deployment has no providers",
							    which is a different and much more alarming statement than
							    "the list has not arrived yet". */}
							{isProvidersLoading && <p className="text-muted-foreground text-xs">Loading providers...</p>}
							{isProvidersError && (
								<p className="text-destructive flex items-center gap-2 text-xs" role="alert">
									Could not load providers.
									<button type="button" onClick={() => refetchProviders()} className="underline" data-testid="warp-providers-retry">
										Retry
									</button>
								</p>
							)}
							{/* A successful empty list is its own situation, distinct from
							    loading and from a failed query. Without this the selector is
							    simply blank, missingRequired blocks the save, and nothing on
							    the page says the deployment has no providers yet or where to
							    add one. Same treatment the complexity router uses. */}
							{!isProvidersLoading && !isProvidersError && providers.length === 0 && (
								<Alert variant="warning" data-testid="warp-no-providers">
									<TriangleAlert className="h-4 w-4" />
									<AlertDescription className="gap-2">
										<span>No provider is configured yet. Warp needs one to run its model on.</span>
										<Button asChild variant="outline" size="sm" data-testid="warp-add-provider-link">
											<Link to="/workspace/providers">
												Add a provider
												<ArrowRight className="size-3.5" />
											</Link>
										</Button>
									</AlertDescription>
								</Alert>
							)}
						</div>

						<div className="space-y-2 rounded-sm border p-4">
							<div className="space-y-0.5">
								<Label htmlFor="warp-model">Model</Label>
								<p className="text-muted-foreground text-sm">
									Warp reasons over query results and writes the answer, so a capable model pays for itself here.
								</p>
							</div>
							<ModelSelector
								inputId="warp-model"
								data-testid="warp-model-select"
								allowCustomModel
								provider={form.provider || undefined}
								// Scoped to the pinned key, because /api/models filters by each
								// key's model restrictions: without this the picker offered - and
								// the form saved - models the pinned key cannot reach, and the
								// first question failed at the provider. Unset for "Any key",
								// which is Bifrost load-balancing across the whole pool.
								keys={modelKeys}
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
									setForm((current) => ({
										...current,
										apiKeyID: value === WARP_ANY_KEY ? "" : value,
										// Cleared rather than revalidated: the new key's model list is
										// not loaded yet, so there is nothing to check against, and
										// leaving the old value keeps a model the new key may not be
										// allowed to use. An empty model already blocks the save, so the
										// operator is told rather than left with a silently wrong pin.
										model: "",
									}));
								}}
								// Also disabled while this provider's keys are unknown, so a stale
								// or empty list cannot be committed as a choice.
								disabled={!form.provider || isKeysLoading || isKeysError || !hasSettingsUpdateAccess}
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
							{/* "No keys configured" is a statement of fact about the provider,
							    so it must not be made while the query is still in flight or
							    after it failed - both of those also produce an empty list. */}
							{form.provider && isKeysLoading && <p className="text-muted-foreground text-xs">Loading keys...</p>}
							{form.provider && isKeysError && (
								<p className="text-destructive flex items-center gap-2 text-xs" role="alert">
									Could not load this provider&apos;s keys.
									<button type="button" onClick={() => refetchKeys()} className="underline" data-testid="warp-keys-retry">
										Retry
									</button>
								</p>
							)}
							{form.provider && !isKeysLoading && !isKeysError && providerKeys.length === 0 && (
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
										<SelectTrigger className="w-full" id="warp-embedding-provider" data-testid="warp-embedding-provider-select">
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
									{/* An empty selector with a "Choose an embedding provider" save
									    error underneath is an instruction nobody can follow. Kept
									    distinct from the loading and error states: "none support
									    embeddings" is a different thing to tell someone than "the
									    list has not arrived". */}
									{!isProvidersLoading && !isProvidersError && embeddingProviders.length === 0 && (
										<p className="text-muted-foreground text-xs" data-testid="warp-no-embedding-providers">
											None of the configured providers support embeddings. Add one that does - OpenAI, Azure OpenAI, Bedrock, Vertex or
											Cohere - before enabling Warp.
										</p>
									)}
								</div>

								<div className="space-y-2">
									<Label htmlFor="warp-embedding-model">Embedding Model</Label>
									<ModelSelector
										inputId="warp-embedding-model"
										data-testid="warp-embedding-model-select"
										allowCustomModel
										provider={form.embeddingProvider || undefined}
										// Scoped to the pinned key, same as the chat model above:
										// /api/models filters by each key's models and
										// blacklisted_models, so without this the selector offered -
										// and the form saved - an embedding model the pinned key
										// cannot use, and core key selection rejected it at indexing,
										// search and backfill alike. Unset for "Any key".
										keys={embeddingModelKeys}
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
										onValueChange={(value) => {
											if (!value) return;
											setForm((current) => ({
												...current,
												embeddingAPIKeyID: value === WARP_ANY_KEY ? "" : value,
												// Cleared, not revalidated: the new key's model list is
												// not loaded yet, so there is nothing to check against,
												// and keeping the old value pins a model the new key may
												// not be allowed to use. An empty embedding model already
												// blocks the save, so the operator is told.
												embeddingModel: "",
											}));
										}}
										disabled={!form.embeddingProvider || isEmbeddingKeysLoading || isEmbeddingKeysError || !hasSettingsUpdateAccess}
									>
										<SelectTrigger className="w-full" id="warp-embedding-api-key" data-testid="warp-embedding-api-key-select">
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
									{form.embeddingProvider && isEmbeddingKeysLoading && <p className="text-muted-foreground text-xs">Loading keys...</p>}
									{form.embeddingProvider && isEmbeddingKeysError && (
										<p className="text-destructive flex items-center gap-2 text-xs" role="alert">
											Could not load this provider&apos;s keys.
											<button
												type="button"
												onClick={() => refetchEmbeddingKeys()}
												className="underline"
												data-testid="warp-embedding-keys-retry"
											>
												Retry
											</button>
										</p>
									)}
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
							{baseURLInvalid && (
								<p className="text-destructive text-sm">Enter an absolute http:// or https:// URL, with no username or password</p>
							)}
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
							<div className="flex items-start justify-between gap-4">
								<div className="space-y-0.5">
									<Label htmlFor="warp-temperature">Temperature</Label>
									<p className="text-muted-foreground text-sm">
										Overrides the model&apos;s sampling temperature. Off leaves it unset, so the provider applies its own default - most
										providers default near 1. Lower is more consistent answer to answer; a tool that reports numbers often wants that.
									</p>
								</div>
								<Switch
									id="warp-temperature-enabled"
									data-testid="warp-temperature-enabled-switch"
									checked={form.temperatureEnabled}
									onCheckedChange={(checked) => update("temperatureEnabled", checked)}
									disabled={!hasSettingsUpdateAccess}
								/>
							</div>
							{form.temperatureEnabled && (
								<>
									<Input
										id="warp-temperature"
										type="number"
										min={WARP_MIN_TEMPERATURE}
										max={WARP_MAX_TEMPERATURE}
										step={0.1}
										data-testid="warp-temperature-input"
										className={temperatureInvalid ? "border-destructive" : ""}
										value={form.temperature}
										onChange={(event) => {
											const raw = event.target.value;
											update("temperature", raw === "" ? "" : Number(raw));
										}}
										disabled={!hasSettingsUpdateAccess}
									/>
									{temperatureInvalid && (
										<p className="text-destructive text-sm">
											Must be between {WARP_MIN_TEMPERATURE} and {WARP_MAX_TEMPERATURE}
										</p>
									)}
								</>
							)}
						</div>

						<div className="space-y-2 rounded-sm border p-4">
							<div className="space-y-0.5">
								<Label htmlFor="warp-reasoning-effort">Reasoning Effort</Label>
								<p className="text-muted-foreground text-sm">
									Only meaningful for a reasoning-capable model. Provider default leaves it unset - sending an effort to a model without
									reasoning is a parameter some providers simply reject.
								</p>
							</div>
							<Select
								value={form.reasoningEffort || WARP_REASONING_UNSET}
								onValueChange={(value) => update("reasoningEffort", value === WARP_REASONING_UNSET ? "" : value)}
								disabled={!hasSettingsUpdateAccess}
							>
								<SelectTrigger id="warp-reasoning-effort" data-testid="warp-reasoning-effort-select">
									<SelectValue />
								</SelectTrigger>
								<SelectContent>
									<SelectItem value={WARP_REASONING_UNSET}>Provider default</SelectItem>
									{WARP_REASONING_EFFORTS.map((effort) => (
										<SelectItem key={effort} value={effort}>
											{capitalize(effort)}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
						</div>

						<div className="space-y-2 rounded-sm border p-4">
							<div className="space-y-0.5">
								<Label htmlFor="warp-history-retention">Chat History Retention (days)</Label>
								<p className="text-muted-foreground text-sm">
									How long a saved chat is kept after its last message. Set 0 to use the default of 30 days. This is separate from log
									retention: chats hold what people typed, so how long to keep them is a different decision from how long to keep request
									telemetry.
								</p>
							</div>
							<Input
								id="warp-history-retention"
								type="number"
								data-testid="warp-history-retention-input"
								className={retentionInvalid ? "border-destructive" : ""}
								value={form.historyRetentionDays}
								onChange={(event) => update("historyRetentionDays", Number(event.target.value))}
								disabled={!hasSettingsUpdateAccess}
							/>
							{retentionInvalid && <p className="text-destructive text-sm">{retentionError}</p>}
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

							{shownBackfill?.id && (
								<div className="bg-muted/40 space-y-2 rounded-sm p-3 text-sm" data-testid="warp-backfill-status">
									<div className="flex items-center justify-between gap-3">
										<span className="font-medium capitalize">{shownBackfill.status}</span>
										<span className="text-muted-foreground">
											{shownBackfill.scanned} / {shownBackfill.total} scanned
										</span>
									</div>
									<div className="bg-border h-2 overflow-hidden rounded-full">
										<div
											className="bg-primary h-full transition-all"
											style={{
												width: `${shownBackfill.total > 0 ? Math.min(100, (shownBackfill.scanned / shownBackfill.total) * 100) : 0}%`,
											}}
										/>
									</div>
									<p className="text-muted-foreground text-xs">
										{shownBackfill.indexed} indexed · {shownBackfill.skipped} skipped · {shownBackfill.failed} failed
									</p>
									{shownBackfill.last_error && <p className="text-destructive text-xs">Latest error: {shownBackfill.last_error}</p>}
								</div>
							)}

							{isBackfillStatusError && (
								<p className="text-destructive flex items-center gap-2 text-xs" role="alert" data-testid="warp-backfill-status-error">
									Could not read backfill status, so a running job may not be shown.
									<button
										type="button"
										onClick={() => refetchBackfillStatus()}
										className="underline"
										data-testid="warp-backfill-status-retry"
									>
										Retry
									</button>
								</p>
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
											isStartingBackfill ||
											// Until the status request lands, "no job is running" is a
											// guess. Starting on that guess is a guaranteed 409.
											isBackfillStatusLoading ||
											isBackfillStatusFetching ||
											isBackfillStatusError ||
											!config?.configured ||
											!config.vector_store_connected ||
											!hasSettingsUpdateAccess ||
											hasChanges
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