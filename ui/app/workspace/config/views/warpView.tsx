import PageTitle from "@/components/pageTitle";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ModelMultiselect } from "@/components/ui/modelMultiselect";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { buildWarpConfigPayload, requireFiniteNumber, validateWarpBaseURL } from "./warpView.utils";
import { Link } from "@tanstack/react-router";
import { ArrowRight, TriangleAlert } from "lucide-react";
import { getProviderLabel } from "@/lib/constants/logs";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { getErrorMessage } from "@/lib/store";
import { useGetProviderKeysQuery, useGetProvidersQuery } from "@/lib/store/apis/providersApi";
import { useGetWarpConfigQuery, useUpdateWarpConfigMutation } from "@/lib/store/apis/warpApi";
import type { WarpConfigInput } from "@/lib/types/warp";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { useEffect, useMemo } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";

interface WarpFormData {
	enabled: boolean;
	provider: string;
	model: string;
	base_url: string;
	api_key_id: string;
	max_iterations: number;
	request_timeout_seconds: number;
	system_prompt_suffix: string;
}

/**
 * Warp talks to Bifrost itself by default.
 *
 * Pointing base_url at the current origin means Warp reaches its model through
 * this deployment's own gateway, using the provider credentials already
 * configured here. That is why the API key below is optional: for the default
 * setup there is no second credential to supply.
 */
const defaultBaseUrl = () => (typeof window === "undefined" ? "" : window.location.origin);

/**
 * Sentinel for "any key". Radix rejects an empty-string SelectItem value, so the
 * unpinned default needs a stand-in that never reaches the form or the API.
 */
const WARP_ANY_KEY = "__any__";

const EMPTY_FORM: WarpFormData = {
	enabled: false,
	provider: "",
	model: "",
	base_url: "",
	api_key_id: "",
	max_iterations: 8,
	request_timeout_seconds: 120,
	system_prompt_suffix: "",
};

export default function WarpView() {
	const hasSettingsUpdateAccess = useRbac(RbacResource.Settings, RbacOperation.Update);
	const { data: config, isLoading: isLoadingConfig, isError: isConfigError } = useGetWarpConfigQuery();
	const {
		data: providersData,
		isLoading: isProvidersLoading,
		isError: isProvidersError,
		refetch: refetchProviders,
	} = useGetProvidersQuery();
	const providers = useMemo(() => providersData ?? [], [providersData]);
	const [updateWarpConfig, { isLoading }] = useUpdateWarpConfigMutation();

	const {
		register,
		handleSubmit,
		formState: { errors, isDirty },
		reset,
		watch,
		setValue,
	} = useForm<WarpFormData>({ defaultValues: EMPTY_FORM });

	const formValues = watch();
	const enabled = watch("enabled");

	// Memoized by the id, not rebuilt inline: watch() rerenders this component
	// on every keystroke anywhere in the form, and ModelMultiselect refetches
	// whenever the `keys` reference changes - so an inline array turned each
	// unrelated edit into a models request for the same key.
	const modelKeys = useMemo(() => (formValues.api_key_id ? [formValues.api_key_id] : undefined), [formValues.api_key_id]);

	// The selects cannot carry react-hook-form validators the way the text inputs
	// they replaced did, so completeness is checked here and surfaced on the save
	// button. The server enforces the same rule; this only saves a round trip.
	const missingRequired = enabled && (!formValues.provider || !formValues.model);

	// Keys are provider-scoped, so the query waits for a provider. skipToken-style
	// gating via `skip` keeps an unconfigured form from firing a request for "".
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
	} = useGetProviderKeysQuery(formValues.provider, {
		skip: !formValues.provider,
	});
	const providerKeys = useMemo(() => providerKeysData ?? [], [providerKeysData]);

	// The three select-backed fields are written with setValue rather than spread
	// from register(), so register them explicitly. Without this they sit outside
	// react-hook-form's registry and whether they reach handleSubmit depends on
	// internals - and a dropped provider saves an empty config that still reports
	// success.
	useEffect(() => {
		register("provider");
		register("model");
		register("api_key_id");
	}, [register]);

	useEffect(() => {
		if (!config) return;
		reset({
			enabled: config.enabled,
			provider: config.provider ?? "",
			model: config.model ?? "",
			base_url: config.base_url || defaultBaseUrl(),
			api_key_id: config.api_key_id ?? "",
			max_iterations: config.max_iterations,
			request_timeout_seconds: config.request_timeout_seconds,
			system_prompt_suffix: config.system_prompt_suffix ?? "",
		});
	}, [config, reset]);

	const hasChanges = useMemo(() => {
		if (!config || !isDirty) return false;
		return (
			formValues.enabled !== config.enabled ||
			formValues.provider !== (config.provider ?? "") ||
			formValues.model !== (config.model ?? "") ||
			formValues.base_url !== (config.base_url ?? "") ||
			formValues.api_key_id !== (config.api_key_id ?? "") ||
			formValues.max_iterations !== config.max_iterations ||
			formValues.request_timeout_seconds !== config.request_timeout_seconds ||
			formValues.system_prompt_suffix !== (config.system_prompt_suffix ?? "")
		);
	}, [config, formValues, isDirty]);

	const onSubmit = async (data: WarpFormData) => {
		const payload = buildWarpConfigPayload(data);
		try {
			await updateWarpConfig(payload).unwrap();
			toast.success("Warp configuration saved.");
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	return (
		<div className="mx-auto w-full max-w-7xl space-y-4" data-testid="warp-config-view">
			<form onSubmit={handleSubmit(onSubmit)} className="space-y-4">
				<PageTitle title="Warp">
					Warp answers questions about your Bifrost data in natural language. It runs on its own model, configured here and kept separate
					from the providers Bifrost serves to your traffic.
				</PageTitle>

				{/* Alpha is stated on the settings page as well as in the panel: this is
				    where someone decides whether to turn Warp on for everyone, so it is
				    the moment the maturity signal actually informs a decision. */}
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
									checked={formValues.enabled}
									disabled={!hasSettingsUpdateAccess}
									onCheckedChange={(checked) => setValue("enabled", checked, { shouldDirty: true })}
								/>
							</div>
							{/* A complete but switched-off config saves happily and then leaves the
                  panel saying Warp is unavailable, with nothing on this page admitting
                  why. Say it here, next to the switch that causes it. */}
							{!formValues.enabled && !!formValues.provider && !!formValues.model && (
								<p className="text-muted-foreground text-xs" data-testid="warp-disabled-hint">
									Everything below is filled in, but Warp stays hidden until this is on.
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
								value={formValues.provider}
								onValueChange={(value) => {
									setValue("provider", value, { shouldDirty: true });
									// Model and key are both provider-scoped, so values carried over
									// from the previous provider would be silently invalid.
									setValue("model", "", { shouldDirty: true });
									setValue("api_key_id", "", { shouldDirty: true });
								}}
								disabled={!hasSettingsUpdateAccess}
							>
								<SelectTrigger className="w-full" id="warp-provider" data-testid="warp-provider-select">
									<SelectValue placeholder="Select provider" />
								</SelectTrigger>
								<SelectContent>
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
							<ModelMultiselect
								inputId="warp-model"
								data-testid="warp-model-select"
								isSingleSelect
								provider={formValues.provider || undefined}
								// Scoped to the pinned key, because /api/models filters by each
								// key's model restrictions: without this the picker offered - and
								// the form saved - models the pinned key cannot reach, and the
								// first question failed at the provider. Unset for "Any key",
								// which is Bifrost load-balancing across the whole pool.
								keys={modelKeys}
								value={formValues.model}
								onChange={(model) => setValue("model", model, { shouldDirty: true })}
								placeholder={formValues.provider ? "Search or type a model..." : "Select a provider first"}
								disabled={!formValues.provider || !hasSettingsUpdateAccess}
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
								value={formValues.api_key_id || WARP_ANY_KEY}
								onValueChange={(value) => {
									setValue("api_key_id", value === WARP_ANY_KEY ? "" : value, { shouldDirty: true });
									// Cleared rather than revalidated: the new key's model list is
									// not loaded yet, so there is nothing to check against, and
									// leaving the old value keeps a model the new key may not be
									// allowed to use. An empty model is already a save-blocking
									// validation error, so the operator is told rather than left
									// with a silently wrong pin.
									setValue("model", "", { shouldDirty: true });
								}}
								// Also disabled while this provider's keys are unknown, so a
								// stale or empty list cannot be committed as a choice.
								disabled={!formValues.provider || isKeysLoading || isKeysError || !hasSettingsUpdateAccess}
							>
								<SelectTrigger className="w-full" id="warp-api-key-id" data-testid="warp-api-key-select">
									<SelectValue placeholder={formValues.provider ? "Any key" : "Select a provider first"} />
								</SelectTrigger>
								<SelectContent>
									{/* Radix forbids an empty-string SelectItem value, so the unpinned
									    default needs a sentinel, mapped back to "" before it reaches the
									    form. Listing it first makes it the obvious default. */}
									<SelectItem value={WARP_ANY_KEY}>Any key</SelectItem>
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
							{formValues.provider && isKeysLoading && <p className="text-muted-foreground text-xs">Loading keys...</p>}
							{formValues.provider && isKeysError && (
								<p className="text-destructive flex items-center gap-2 text-xs" role="alert">
									Could not load this provider&apos;s keys.
									<button type="button" onClick={() => refetchKeys()} className="underline" data-testid="warp-keys-retry">
										Retry
									</button>
								</p>
							)}
							{formValues.provider && !isKeysLoading && !isKeysError && providerKeys.length === 0 && (
								<p className="text-muted-foreground text-xs">
									This provider has no keys configured, which is fine if it does not need one.
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
								className={errors.base_url ? "border-destructive" : ""}
								{...register("base_url", { validate: validateWarpBaseURL })}
								disabled={!hasSettingsUpdateAccess}
							/>
							{errors.base_url && <p className="text-destructive text-sm">{errors.base_url.message}</p>}
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
								className={errors.max_iterations ? "border-destructive" : ""}
								{...register("max_iterations", {
									valueAsNumber: true,
									validate: (value) => requireFiniteNumber(value, "A value is required"),
									min: { value: 1, message: "Must be at least 1" },
									max: { value: 20, message: "Cannot exceed 20" },
								})}
								disabled={!hasSettingsUpdateAccess}
							/>
							{errors.max_iterations && <p className="text-destructive text-sm">{errors.max_iterations.message}</p>}
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
								className={errors.request_timeout_seconds ? "border-destructive" : ""}
								{...register("request_timeout_seconds", {
									valueAsNumber: true,
									validate: (value) => requireFiniteNumber(value, "A value is required"),
									min: { value: 1, message: "Must be at least 1 second" },
								})}
								disabled={!hasSettingsUpdateAccess}
							/>
							{errors.request_timeout_seconds && <p className="text-destructive text-sm">{errors.request_timeout_seconds.message}</p>}
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
								{...register("system_prompt_suffix")}
								disabled={!hasSettingsUpdateAccess}
							/>
						</div>
					</div>
				)}

				<div className="flex justify-end gap-3 pt-2">
					{missingRequired && (
						<p className="text-muted-foreground self-center text-xs" data-testid="warp-missing-required">
							Choose a provider and model to enable Warp.
						</p>
					)}
					<Button
						type="submit"
						disabled={!hasChanges || isLoading || missingRequired || !hasSettingsUpdateAccess}
						data-testid="warp-save-btn"
					>
						{isLoading ? "Saving..." : "Save Changes"}
					</Button>
				</div>
			</form>
		</div>
	);
}