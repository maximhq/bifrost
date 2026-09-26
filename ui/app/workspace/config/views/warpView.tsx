import PageTitle from "@/components/pageTitle";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { DateTimePickerWithRange } from "@/components/ui/datePickerWithRange";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ModelSelector } from "@/components/ui/modelSelector";
import { ProviderSelector } from "@/components/ui/providerSelector";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { AutoSizeTextarea } from "@/components/ui/textarea";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { getErrorMessage } from "@/lib/store";
import { cn } from "@/lib/utils";
import { useGetProviderKeysQuery, useGetProvidersQuery } from "@/lib/store/apis/providersApi";
import {
	useCancelWarpBackfillMutation,
	useGetWarpBackfillStatusQuery,
	useGetWarpConfigQuery,
	useStartWarpBackfillMutation,
	useUpdateWarpConfigMutation,
} from "@/lib/store/apis/warpApi";
import {
	WARP_MAX_TEMPERATURE,
	WARP_MIN_TEMPERATURE,
	WARP_REASONING_EFFORTS,
	type WarpBackfillJob,
	type WarpConfigInput,
} from "@/lib/types/warp";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Link } from "@tanstack/react-router";
import { AlertTriangle, ArrowRight, CheckCircle2, Database, Info, Loader2, TriangleAlert } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { getRangeForPeriod, TIME_PERIODS } from "@/lib/utils/timeRange";
import { getExampleBaseUrl } from "@/lib/utils/port";
import {
	embeddingSpaceChanged,
	normalizeWarpNamespace,
	supportsWarpEmbedding,
	validateWarpEmbedding,
	type WarpEmbeddingFields,
} from "./warpConfig.utils";
import { isFiniteNumber, isValidBaseURL, validateWarpRetentionDays } from "./warpView.utils";

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
const defaultBaseUrl = () => {
	// On the Vite dev server the page origin is Vite, not Bifrost, so this
	// resolves to the Go server (localhost:8080) there and to the page origin in
	// production.
	const origin = getExampleBaseUrl();
	return origin ? `${origin}/openai` : "";
};

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
const DEFAULT_BACKFILL_PERIOD = "7d";

/** "xhigh" -> "Xhigh". Good enough for a Select item; these are short, plain words. */
const capitalize = (value: string) => value.charAt(0).toUpperCase() + value.slice(1);

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
	const hasWarpUpdateAccess = useRbac(RbacResource.Warp, RbacOperation.Update);
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
	const [backfillPeriod, setBackfillPeriod] = useState<string | undefined>(DEFAULT_BACKFILL_PERIOD);
	const [backfillStart, setBackfillStart] = useState<Date | undefined>(() => getRangeForPeriod(DEFAULT_BACKFILL_PERIOD).from);
	const [backfillEnd, setBackfillEnd] = useState<Date | undefined>(() => getRangeForPeriod(DEFAULT_BACKFILL_PERIOD).to);
	// Memoized so the picker only resyncs on a real edit: it resets its internal
	// state whenever this object's identity changes, and the status poll
	// rerenders this view every couple of seconds.
	const backfillRange = useMemo(() => ({ from: backfillStart, to: backfillEnd }), [backfillStart, backfillEnd]);
	// Full-precision counterparts of backfillStart/backfillEnd, used only for
	// comparing against backfillStatus.start_time/end_time. The range picker
	// works in minutes, so comparing its values directly against the server's
	// timestamps (which can carry seconds/ms) would make a job's own frozen
	// window look like it never matches itself right after hydration. These
	// track the real endpoints: seeded from the server's own timestamps on
	// hydration, or from the picker's edit otherwise.
	const [backfillStartCompare, setBackfillStartCompare] = useState<string | null>(null);
	const [backfillEndCompare, setBackfillEndCompare] = useState<string | null>(null);

	const providers = providersData ?? [];
	const embeddingProviders = providers.filter(supportsWarpEmbedding);

	// Memoized by the id, not rebuilt inline: every form edit rerenders this
	// component, and ModelMultiselect refetches whenever the `keys` reference
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
		skip: !hasWarpUpdateAccess,
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

	// A failed or cancelled run that got partway through the window can be
	// resumed from its checkpoint instead of rescanning from the start - see
	// the `restart` flag on startWarpBackfill. Resuming only makes sense while
	// the selected dates still match the failed job's frozen window: the
	// server only reuses a checkpoint on an exact start/end match
	// (BuildBackfillJobMeta), so once the picker has moved off that window,
	// pressing "resume" would silently start a fresh scan under a label that
	// still says otherwise. Compares against backfillStartCompare/EndCompare
	// (full server precision), not the minute-truncated picker strings - see
	// their declaration above.
	const canResumeBackfill =
		!isBackfillActive &&
		(backfillStatus?.status === "failed" || backfillStatus?.status === "cancelled") &&
		backfillStatus.scanned > 0 &&
		backfillStatus.scanned < backfillStatus.total &&
		!!backfillStatus.start_time &&
		!!backfillStatus.end_time &&
		!!backfillStartCompare &&
		!!backfillEndCompare &&
		new Date(backfillStartCompare).getTime() === new Date(backfillStatus.start_time).getTime() &&
		new Date(backfillEndCompare).getTime() === new Date(backfillStatus.end_time).getTime();
	// Whether the currently selected window is the exact one a completed job
	// already finished - a frozen window that has already fully scanned can't
	// pick up more logs by running again, so offering an active "Start
	// backfill" button there just invites a pointless re-embed of the same
	// rows. Compares against backfillStartCompare/EndCompare (full server
	// precision), not the minute-truncated picker strings - see their
	// declaration above.
	const backfillWindowAlreadyDone =
		!isBackfillActive &&
		backfillStatus?.status === "completed" &&
		!!backfillStatus.start_time &&
		!!backfillStatus.end_time &&
		!!backfillStartCompare &&
		!!backfillEndCompare &&
		new Date(backfillStartCompare).getTime() === new Date(backfillStatus.start_time).getTime() &&
		new Date(backfillEndCompare).getTime() === new Date(backfillStatus.end_time).getTime();

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
			// config.temperature is absent for "unset", not 0, and also for a row
			// saved before this field was always sent - undefined and null both fall
			// back to the default here, since JSON only sends one of them but either
			// could arrive depending on what wrote the row.
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

	// The date pickers default to "last 7 days" at mount time, which no longer
	// matches a finished job's frozen window after a reload (the defaults
	// recompute against the new "now"). Seed them from that job's own window
	// once it's known - for any terminal status, not just a resumable one -
	// so the pickers reflect what was actually last run: a failed/cancelled
	// job resumes from its checkpoint instead of silently mismatching and
	// falling back to a fresh scan, and a completed job is correctly flagged
	// as already covering this window (see backfillWindowAlreadyDone) rather
	// than looking identical to a window nothing has ever touched. Keyed on
	// the job id so a later manual edit sticks.
	useEffect(() => {
		// Narrowed on id, not start_time: the status is a union and the idle arm
		// carries neither timestamp, so id is what tells the two apart.
		if (isBackfillActive || !backfillStatus?.id) return;
		if (!backfillStatus.start_time || !backfillStatus.end_time) return;
		setBackfillPeriod(undefined);
		setBackfillStart(new Date(backfillStatus.start_time));
		setBackfillEnd(new Date(backfillStatus.end_time));
		setBackfillStartCompare(backfillStatus.start_time);
		setBackfillEndCompare(backfillStatus.end_time);
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [backfillStatus?.id, isBackfillActive]);

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
			form.temperature !== (config.temperature ?? DEFAULT_TEMPERATURE) ||
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
	const temperatureInvalid = form.temperature === "" || form.temperature < WARP_MIN_TEMPERATURE || form.temperature > WARP_MAX_TEMPERATURE;
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
			temperature: form.temperature !== "" ? form.temperature : undefined,
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

	const onStartBackfill = async (restart = false) => {
		if (!backfillStart || !backfillEnd || backfillStart >= backfillEnd) {
			toast.error("Choose a valid backfill time range.");
			return;
		}
		// Resuming a checkpoint only works if the request's window matches the
		// server's stored one exactly (BuildBackfillJobMeta) - the picker's
		// minute-truncated values can't be trusted for that, so a genuine resume
		// sends back the frozen full-precision timestamps canResumeBackfill
		// already confirmed match backfillStatus.start_time/end_time. A fresh
		// scan - either explicitly restarted or over a window with nothing to
		// resume - has no such frozen window and uses what the operator picked.
		const resuming = !restart && canResumeBackfill && !!backfillStartCompare && !!backfillEndCompare;
		// A preset is recomputed now, so a page left open still covers logs written since mount.
		const freshRange = backfillPeriod ? getRangeForPeriod(backfillPeriod) : { from: backfillStart, to: backfillEnd };
		try {
			const status = await startWarpBackfill({
				start_time: resuming ? backfillStartCompare : freshRange.from.toISOString(),
				end_time: resuming ? backfillEndCompare : freshRange.to.toISOString(),
				restart,
			}).unwrap();
			// A new run replaces the last one's result, so the panel never shows a
			// finished job beside a running one.
			setFinishedBackfill(null);
			setActiveBackfillID(status.id ?? null);
			toast.success(restart ? "Warp embedding backfill restarted from the beginning." : "Warp embedding backfill started.");
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
								<div className="space-y-1">
									{/* Alpha sits beside the switch: this is where someone decides
									    whether to turn Warp on for everyone, so it is the moment the
									    maturity signal actually informs a decision. */}
									<div className="flex items-center gap-2">
										<Label htmlFor="warp-enabled">Enable Warp</Label>
										<Badge variant="secondary">ALPHA</Badge>
									</div>
									<p className="text-muted-foreground text-sm">
										Adds the Warp panel to the dashboard. Warp reads logs, metrics and usage data on behalf of whoever asks, scoped to what
										that person can already see. It is early, so check its numbers against the dashboard before acting on them.
									</p>
								</div>
								<Switch
									id="warp-enabled"
									size="md"
									data-testid="warp-enabled-switch"
									checked={form.enabled}
									disabled={!hasWarpUpdateAccess || (!config?.vector_store_connected && !form.enabled)}
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

						<WarpSection
							title="Model"
							description="The model Warp reasons with and writes answers from. A capable model pays for itself here."
						>
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

							<div className="grid gap-x-6 gap-y-5 md:grid-cols-3">
								<WarpField label="Provider" htmlFor="warp-provider" hint="Only providers already configured in Bifrost are listed.">
									<ProviderSelector
										inputId="warp-provider"
										data-testid="warp-provider-select"
										value={form.provider}
										onChange={(value: string) =>
											setForm((current) =>
												// Model and key are provider-scoped, so values carried over from
												// the previous provider would be silently invalid.
												// "" is ProviderSelector deselecting the current row, not a choice.
												!value || value === current.provider ? current : { ...current, provider: value, model: "", apiKeyID: "" },
											)
										}
										disabled={!hasWarpUpdateAccess}
									/>
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
								</WarpField>

								<WarpField label="Model" htmlFor="warp-model">
									<ModelSelector
										inputId="warp-model"
										data-testid="warp-model-select"
										multiple={false}
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
										disabled={!form.provider || !hasWarpUpdateAccess}
									/>
								</WarpField>

								<WarpField
									label="API key"
									htmlFor="warp-api-key-id"
									hint="Any key load-balances across the provider's pool. Pin one to isolate Warp's traffic."
								>
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
										disabled={!form.provider || isKeysLoading || isKeysError || !hasWarpUpdateAccess}
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
									    after it failed - both of those also produce an empty list.
									    Kept inline, not in the tooltip: it describes current state. */}
									{form.provider && !isKeysLoading && !isKeysError && providerKeys.length === 0 && (
										<p className="text-muted-foreground text-xs">This provider has no keys configured, which is fine if it needs none.</p>
									)}
									{form.provider && isKeysLoading && <p className="text-muted-foreground text-xs">Loading keys...</p>}
									{form.provider && isKeysError && (
										<p className="text-destructive flex items-center gap-2 text-xs" role="alert">
											Could not load this provider&apos;s keys.
											<button type="button" onClick={() => refetchKeys()} className="underline" data-testid="warp-keys-retry">
												Retry
											</button>
										</p>
									)}
								</WarpField>

								<WarpField
									className="md:col-span-3"
									label="Base URL"
									htmlFor="warp-base-url"
									hint="Defaults to this Bifrost, so Warp reuses the credentials configured here. Point it elsewhere only to call a provider directly."
									error={baseURLInvalid ? "Enter an absolute http:// or https:// URL, with no username or password" : undefined}
								>
									<Input
										id="warp-base-url"
										type="text"
										placeholder="https://llm.internal.example.com/v1"
										data-testid="warp-base-url-input"
										className={baseURLInvalid ? "border-destructive" : ""}
										value={form.baseURL}
										onChange={(event) => update("baseURL", event.target.value)}
										disabled={!hasWarpUpdateAccess}
									/>
								</WarpField>
							</div>
						</WarpSection>

						<WarpSection
							title="Behavior and limits"
							description="How Warp samples, how far it may go per question, and how long chats are kept."
						>
							<div className="grid gap-x-6 gap-y-5 md:grid-cols-2">
								<WarpField
									label="Temperature"
									htmlFor="warp-temperature"
									hint={`${WARP_MIN_TEMPERATURE} to ${WARP_MAX_TEMPERATURE}. Lower gives more consistent answers, which suits a tool that reports numbers.`}
									error={temperatureInvalid ? `Must be between ${WARP_MIN_TEMPERATURE} and ${WARP_MAX_TEMPERATURE}` : undefined}
								>
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
										disabled={!hasWarpUpdateAccess}
									/>
								</WarpField>

								<WarpField
									label="Reasoning effort"
									htmlFor="warp-reasoning-effort"
									hint="Only for reasoning models. Some providers reject an effort sent to a model without reasoning."
								>
									<Select
										value={form.reasoningEffort || WARP_REASONING_UNSET}
										onValueChange={(value) => update("reasoningEffort", value === WARP_REASONING_UNSET ? "" : value)}
										disabled={!hasWarpUpdateAccess}
									>
										<SelectTrigger className="w-full" id="warp-reasoning-effort" data-testid="warp-reasoning-effort-select">
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
								</WarpField>
							</div>

							<div className="grid gap-x-6 gap-y-5 md:grid-cols-3">
								<WarpField
									label="Max iterations"
									htmlFor="warp-max-iterations"
									hint="Query-and-reconsider rounds per answer. Each one is billed."
									error={iterationsInvalid ? "Must be between 1 and 20" : undefined}
								>
									<Input
										id="warp-max-iterations"
										type="number"
										data-testid="warp-max-iterations-input"
										className={iterationsInvalid ? "border-destructive" : ""}
										value={form.maxIterations}
										onChange={(event) => update("maxIterations", Number(event.target.value))}
										disabled={!hasWarpUpdateAccess}
									/>
								</WarpField>

								<WarpField
									label="Request timeout (seconds)"
									htmlFor="warp-request-timeout"
									hint="Bound on one model call. Raise it for slow self-hosted models."
									error={timeoutInvalid ? "Must be at least 1 second" : undefined}
								>
									<Input
										id="warp-request-timeout"
										type="number"
										data-testid="warp-request-timeout-input"
										className={timeoutInvalid ? "border-destructive" : ""}
										value={form.requestTimeoutSeconds}
										onChange={(event) => update("requestTimeoutSeconds", Number(event.target.value))}
										disabled={!hasWarpUpdateAccess}
									/>
								</WarpField>

								<WarpField
									label="Chat history retention (days)"
									htmlFor="warp-history-retention"
									hint={`Days after the last message. 0 uses the default of ${DEFAULT_HISTORY_RETENTION_DAYS}. Separate from log retention.`}
									error={retentionInvalid ? retentionError : undefined}
								>
									<Input
										id="warp-history-retention"
										type="number"
										data-testid="warp-history-retention-input"
										className={retentionInvalid ? "border-destructive" : ""}
										value={form.historyRetentionDays}
										onChange={(event) => update("historyRetentionDays", Number(event.target.value))}
										disabled={!hasWarpUpdateAccess}
									/>
								</WarpField>
							</div>

							<WarpField
								label="Additional instructions"
								htmlFor="warp-system-prompt-suffix"
								hint="Appended to Warp's built-in prompt, never replacing it. Useful for local naming conventions or team structure."
							>
								<AutoSizeTextarea
									id="warp-system-prompt-suffix"
									minRows={3}
									maxRows={12}
									placeholder="Costs are in USD. Team IDs map to squads in Notion."
									data-testid="warp-system-prompt-suffix-input"
									value={form.systemPromptSuffix}
									onChange={(event) => update("systemPromptSuffix", event.target.value)}
									disabled={!hasWarpUpdateAccess}
								/>
							</WarpField>
						</WarpSection>

						<WarpSection
							title="Conversation search"
							description="Warp embeds completed conversations into your vector store and uses them to find logs by meaning. Only vectors and operational metadata are stored there; conversation text stays in the log store."
							action={
								<Badge variant={config?.vector_store_connected ? "secondary" : "destructive"} data-testid="warp-vector-store-status">
									{config?.vector_store_connected ? (
										<CheckCircle2 className="mr-1 h-3.5 w-3.5" />
									) : (
										<Database className="mr-1 h-3.5 w-3.5" />
									)}
									{config?.vector_store_connected ? "Vector store connected" : "Vector store disconnected"}
								</Badge>
							}
						>
							<div className="grid gap-x-6 gap-y-5 md:grid-cols-2">
								<WarpField label="Embedding provider" htmlFor="warp-embedding-provider">
									<ProviderSelector
										inputId="warp-embedding-provider"
										data-testid="warp-embedding-provider-select"
										filter={supportsWarpEmbedding}
										placeholder="Select embedding provider"
										value={form.embeddingProvider}
										onChange={(value: string) =>
											setForm((current) =>
												// Model and key are provider-scoped, so they only reset on a real change.
												!value || value === current.embeddingProvider
													? current
													: { ...current, embeddingProvider: value, embeddingModel: "", embeddingAPIKeyID: "" },
											)
										}
										disabled={!hasWarpUpdateAccess}
									/>
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
								</WarpField>

								<WarpField label="Embedding model" htmlFor="warp-embedding-model">
									<ModelSelector
										inputId="warp-embedding-model"
										data-testid="warp-embedding-model-select"
										multiple={false}
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
										disabled={!form.embeddingProvider || !hasWarpUpdateAccess}
									/>
								</WarpField>

								<WarpField label="Embedding API key" htmlFor="warp-embedding-api-key">
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
										disabled={!form.embeddingProvider || isEmbeddingKeysLoading || isEmbeddingKeysError || !hasWarpUpdateAccess}
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
								</WarpField>

								<WarpField label="Embedding dimension" htmlFor="warp-embedding-dimension">
									<Input
										id="warp-embedding-dimension"
										type="number"
										min={1}
										data-testid="warp-embedding-dimension-input"
										value={form.embeddingDimension}
										onChange={(event) => update("embeddingDimension", Number(event.target.value))}
										disabled={!hasWarpUpdateAccess}
									/>
								</WarpField>
							</div>

							<div className="grid gap-x-6 gap-y-5 md:grid-cols-3">
								<WarpField
									label="Log embedding namespace"
									htmlFor="warp-vector-namespace"
									hint="Change it whenever the provider, model or dimension changes."
								>
									<Input
										id="warp-vector-namespace"
										data-testid="warp-vector-namespace-input"
										value={form.namespace}
										onChange={(event) => update("namespace", event.target.value)}
										disabled={!hasWarpUpdateAccess}
									/>
								</WarpField>

								<WarpField
									label="Similarity threshold"
									htmlFor="warp-search-threshold"
									hint="0.01 to 1. Higher returns fewer, closer matches."
								>
									<Input
										id="warp-search-threshold"
										type="number"
										min={0.01}
										max={1}
										step={0.01}
										data-testid="warp-search-threshold-input"
										value={form.threshold}
										onChange={(event) => update("threshold", Number(event.target.value))}
										disabled={!hasWarpUpdateAccess}
									/>
								</WarpField>

								<WarpField label="Maximum matches" htmlFor="warp-search-limit" hint="1 to 25 conversations per search.">
									<Input
										id="warp-search-limit"
										type="number"
										min={1}
										max={25}
										data-testid="warp-search-limit-input"
										value={form.searchLimit}
										onChange={(event) => update("searchLimit", Number(event.target.value))}
										disabled={!hasWarpUpdateAccess}
									/>
								</WarpField>
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

							<div className="-mx-4 space-y-3 border-t px-4 pt-4" data-testid="warp-backfill-section">
								<div className="space-y-0.5">
									<h4 className="text-sm font-medium">Backfill embeddings</h4>
									<p className="text-muted-foreground text-xs">
										Index completed conversations from an existing log window. Runs in the background, can be cancelled, and resumes safely
										across batches.
									</p>
								</div>

								<div className="flex flex-col gap-2 md:flex-row md:items-center">
									<div className="min-w-0 flex-1">
										<DateTimePickerWithRange
											buttonClassName="w-full"
											popupAlignment="start"
											triggerTestId="warp-backfill-range"
											dateTime={backfillRange}
											disabledAfter={new Date()}
											disabled={isBackfillActive || !hasWarpUpdateAccess}
											predefinedPeriod={backfillPeriod}
											preDefinedPeriods={TIME_PERIODS}
											onDateTimeUpdate={(range) => {
												setBackfillPeriod(undefined);
												setBackfillStart(range.from);
												setBackfillEnd(range.to);
												setBackfillStartCompare(range.from?.toISOString() ?? null);
												setBackfillEndCompare(range.to?.toISOString() ?? null);
											}}
											onPredefinedPeriodChange={(period) => {
												if (!period) return;
												const { from, to } = getRangeForPeriod(period);
												setBackfillPeriod(period);
												setBackfillStart(from);
												setBackfillEnd(to);
												setBackfillStartCompare(from.toISOString());
												setBackfillEndCompare(to.toISOString());
											}}
										/>
									</div>

									<div className="flex shrink-0 items-center justify-end gap-2">
										{isBackfillActive ? (
											<Button
												type="button"
												variant="outline"
												onClick={onCancelBackfill}
												disabled={isCancellingBackfill || !hasWarpUpdateAccess}
												data-testid="warp-backfill-cancel-btn"
											>
												{isCancellingBackfill && <Loader2 className="mr-2 h-4 w-4 animate-spin" />} Cancel backfill
											</Button>
										) : (
											<>
												{canResumeBackfill && (
													<Button
														type="button"
														variant="outline"
														onClick={() => onStartBackfill(true)}
														disabled={
															isStartingBackfill ||
															// Until the status request lands, "no job is running" is a
															// guess. Starting on that guess is a guaranteed 409.
															isBackfillStatusLoading ||
															isBackfillStatusFetching ||
															isBackfillStatusError ||
															!config?.configured ||
															!config.vector_store_connected ||
															!hasWarpUpdateAccess ||
															hasChanges
														}
														data-testid="warp-backfill-restart-btn"
													>
														Start fresh instead
													</Button>
												)}
												<Button
													type="button"
													variant="outline"
													onClick={() => onStartBackfill(false)}
													disabled={
														isStartingBackfill ||
														// Until the status request lands, "no job is running" is a
														// guess. Starting on that guess is a guaranteed 409.
														isBackfillStatusLoading ||
														isBackfillStatusFetching ||
														isBackfillStatusError ||
														!config?.configured ||
														!config.vector_store_connected ||
														!hasWarpUpdateAccess ||
														hasChanges ||
														backfillWindowAlreadyDone
													}
													data-testid="warp-backfill-start-btn"
												>
													{isStartingBackfill && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
													{canResumeBackfill ? `Resume backfill (${backfillStatus.scanned}/${backfillStatus.total})` : "Start backfill"}
												</Button>
											</>
										)}
									</div>
								</div>

								{hasChanges && <p className="text-muted-foreground text-xs">Save configuration changes before starting a backfill.</p>}
								{!hasChanges && backfillWindowAlreadyDone && (
									<p className="text-muted-foreground text-xs">
										This window is already fully indexed. Change the dates to backfill a different range.
									</p>
								)}

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
										{shownBackfill.message && <p className="text-muted-foreground text-xs">{shownBackfill.message}</p>}
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
							</div>
						</WarpSection>
					</div>
				)}

				<div className="bg-card sticky bottom-0 z-10 flex justify-end gap-3 border-t py-3">
					{missingRequired && (
						<p className="text-muted-foreground self-center text-xs" data-testid="warp-missing-required">
							Choose a provider and model to enable Warp.
						</p>
					)}
					<Button type="submit" disabled={!hasChanges || isSaving || invalid || !hasWarpUpdateAccess} data-testid="warp-save-btn">
						{isSaving ? "Saving..." : "Save Changes"}
					</Button>
				</div>
			</form>
		</div>
	);
}

interface WarpSectionProps {
	title: string;
	description?: React.ReactNode;
	action?: React.ReactNode;
	children: React.ReactNode;
}

/** One bordered group per concern, so related fields share a card instead of each getting its own box. */
function WarpSection({ title, description, action, children }: WarpSectionProps) {
	return (
		<section className="rounded-sm border">
			<div className="flex items-start justify-between gap-4 border-b px-4 py-3">
				<div className="space-y-0.5">
					<h3 className="text-sm font-semibold">{title}</h3>
					{description && <p className="text-muted-foreground text-sm">{description}</p>}
				</div>
				{action && <div className="shrink-0">{action}</div>}
			</div>
			<div className="space-y-5 p-4">{children}</div>
		</section>
	);
}

interface WarpFieldProps {
	label: string;
	htmlFor: string;
	// Shown behind an info icon beside the label, so the field itself stays compact.
	hint?: React.ReactNode;
	// Inline under the control: a validation message has to be visible without hovering.
	error?: React.ReactNode;
	className?: string;
	children: React.ReactNode;
}

function WarpField({ label, htmlFor, hint, error, className, children }: WarpFieldProps) {
	return (
		<div className={cn("flex min-w-0 flex-col gap-1.5", className)}>
			<div className="flex items-center gap-1.5">
				<Label htmlFor={htmlFor}>{label}</Label>
				{hint && (
					<Tooltip>
						<TooltipTrigger asChild>
							<button
								type="button"
								className="text-muted-foreground hover:text-foreground inline-flex"
								aria-label={`About ${label}`}
								data-testid={`${htmlFor}-info`}
							>
								<Info className="size-3.5" />
							</button>
						</TooltipTrigger>
						<TooltipContent className="max-w-xs">{hint}</TooltipContent>
					</Tooltip>
				)}
			</div>
			{children}
			{error && <p className="text-destructive text-xs">{error}</p>}
		</div>
	);
}