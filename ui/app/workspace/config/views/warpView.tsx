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
import { useGetWarpConfigQuery, useUpdateWarpConfigMutation } from "@/lib/store/apis/warpApi";
import { useGetProviderKeysQuery, useGetProvidersQuery } from "@/lib/store/apis/providersApi";
import type { WarpConfigInput } from "@/lib/types/warp";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { useEffect, useState } from "react";
import { toast } from "sonner";

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

	const [form, setForm] = useState<WarpFormState>(EMPTY_FORM);

	const providers = providersData ?? [];
	// Keys are provider-scoped, so the query waits for a provider rather than
	// firing a request for "".
	const { data: providerKeysData } = useGetProviderKeysQuery(form.provider, { skip: !form.provider });
	const providerKeys = providerKeysData ?? [];

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
			form.systemPromptSuffix !== (config.system_prompt_suffix ?? ""));

	// The server enforces the same rules; checking here only saves a round trip.
	const missingRequired = form.enabled && (!form.provider || !form.model);
	const baseURLInvalid = form.baseURL !== "" && !/^https?:\/\//.test(form.baseURL);
	const iterationsInvalid = form.maxIterations < 1 || form.maxIterations > 20;
	const timeoutInvalid = form.requestTimeoutSeconds < 1;
	const invalid = missingRequired || baseURLInvalid || iterationsInvalid || timeoutInvalid;

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
									disabled={!hasSettingsUpdateAccess}
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