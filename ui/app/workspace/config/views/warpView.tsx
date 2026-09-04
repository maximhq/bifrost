import PageTitle from "@/components/pageTitle";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ModelMultiselect } from "@/components/ui/modelMultiselect";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
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
	const { data: config, isLoading: isLoadingConfig } = useGetWarpConfigQuery();
	const { data: providersData } = useGetProvidersQuery();
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

	// The selects cannot carry react-hook-form validators the way the text inputs
	// they replaced did, so completeness is checked here and surfaced on the save
	// button. The server enforces the same rule; this only saves a round trip.
	const missingRequired = enabled && (!formValues.provider || !formValues.model);

	// Keys are provider-scoped, so the query waits for a provider. skipToken-style
	// gating via `skip` keeps an unconfigured form from firing a request for "".
	const { data: providerKeysData } = useGetProviderKeysQuery(formValues.provider, {
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
		const payload: WarpConfigInput = {
			enabled: data.enabled,
			provider: data.provider.trim(),
			model: data.model.trim(),
			base_url: data.base_url.trim(),
			api_key_id: data.api_key_id,
			max_iterations: data.max_iterations,
			request_timeout_seconds: data.request_timeout_seconds,
			system_prompt_suffix: data.system_prompt_suffix,
		};
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
								onValueChange={(value) => setValue("api_key_id", value === WARP_ANY_KEY ? "" : value, { shouldDirty: true })}
								disabled={!formValues.provider || !hasSettingsUpdateAccess}
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
							{formValues.provider && providerKeys.length === 0 && (
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
								{...register("base_url", {
									validate: (value) =>
										!value || value.startsWith("http://") || value.startsWith("https://") || "URL must start with http:// or https://",
								})}
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
									min: { value: 1, message: "Must be at least 1" },
									max: { value: 20, message: "Cannot exceed 20" },
								})}
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
									min: { value: 1, message: "Must be at least 1 second" },
								})}
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