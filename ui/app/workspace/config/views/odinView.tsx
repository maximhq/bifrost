import PageTitle from "@/components/pageTitle";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { getErrorMessage } from "@/lib/store";
import { useGetOdinConfigQuery, useUpdateOdinConfigMutation } from "@/lib/store/apis/odinApi";
import type { OdinConfigInput } from "@/lib/types/odin";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { useEffect, useMemo, useState } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";

interface OdinFormData {
	enabled: boolean;
	provider: string;
	model: string;
	base_url: string;
	api_key: string;
	max_iterations: number;
	request_timeout_seconds: number;
	system_prompt_suffix: string;
}

const EMPTY_FORM: OdinFormData = {
	enabled: false,
	provider: "",
	model: "",
	base_url: "",
	api_key: "",
	max_iterations: 8,
	request_timeout_seconds: 120,
	system_prompt_suffix: "",
};

export default function OdinView() {
	const hasSettingsUpdateAccess = useRbac(RbacResource.Settings, RbacOperation.Update);
	const { data: config, isLoading: isLoadingConfig } = useGetOdinConfigQuery();
	const [updateOdinConfig, { isLoading }] = useUpdateOdinConfigMutation();

	// The stored key never comes back from the server, so the form cannot round-trip
	// it the way it does every other field. Instead the input stays empty and shows
	// that a key is already configured; only when the operator opts in to replacing
	// it does a value get sent. See the api_key contract in lib/types/odin.ts.
	const [replacingKey, setReplacingKey] = useState(false);

	const {
		register,
		handleSubmit,
		formState: { errors, isDirty },
		reset,
		watch,
		setValue,
	} = useForm<OdinFormData>({ defaultValues: EMPTY_FORM });

	const formValues = watch();
	const enabled = watch("enabled");

	useEffect(() => {
		if (!config) return;
		reset({
			enabled: config.enabled,
			provider: config.provider ?? "",
			model: config.model ?? "",
			base_url: config.base_url ?? "",
			api_key: "",
			max_iterations: config.max_iterations,
			request_timeout_seconds: config.request_timeout_seconds,
			system_prompt_suffix: config.system_prompt_suffix ?? "",
		});
		setReplacingKey(false);
	}, [config, reset]);

	// A typed replacement key is a change even when every other field matches the
	// server, and isDirty alone would miss it on a form whose key input started empty.
	const hasChanges = useMemo(() => {
		if (!config) return false;
		if (replacingKey && formValues.api_key !== "") return true;
		if (!isDirty) return false;
		return (
			formValues.enabled !== config.enabled ||
			formValues.provider !== (config.provider ?? "") ||
			formValues.model !== (config.model ?? "") ||
			formValues.base_url !== (config.base_url ?? "") ||
			formValues.max_iterations !== config.max_iterations ||
			formValues.request_timeout_seconds !== config.request_timeout_seconds ||
			formValues.system_prompt_suffix !== (config.system_prompt_suffix ?? "")
		);
	}, [config, formValues, isDirty, replacingKey]);

	const onSubmit = async (data: OdinFormData) => {
		const payload: OdinConfigInput = {
			enabled: data.enabled,
			provider: data.provider.trim(),
			model: data.model.trim(),
			base_url: data.base_url.trim(),
			max_iterations: data.max_iterations,
			request_timeout_seconds: data.request_timeout_seconds,
			system_prompt_suffix: data.system_prompt_suffix,
		};
		// Omit api_key entirely unless the operator is deliberately replacing it.
		// Sending "" would clear the stored credential, which is emphatically not
		// what editing the model name should do.
		if (replacingKey) {
			payload.api_key = data.api_key;
		}
		try {
			await updateOdinConfig(payload).unwrap();
			toast.success("Odin configuration saved.");
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	return (
		<div className="mx-auto w-full max-w-7xl space-y-4" data-testid="odin-config-view">
			<form onSubmit={handleSubmit(onSubmit)} className="space-y-4">
				<PageTitle title="Odin">
					Odin answers questions about your Bifrost data in natural language. It runs on its own model, configured here and kept separate
					from the providers Bifrost serves to your traffic.
				</PageTitle>

				{/* Alpha is stated on the settings page as well as in the panel: this is
				    where someone decides whether to turn Odin on for everyone, so it is
				    the moment the maturity signal actually informs a decision. */}
				<div className="flex items-center gap-2">
					<Badge variant="secondary">ALPHA</Badge>
					<p className="text-muted-foreground text-xs">Odin is early. Check its numbers against the dashboard before acting on them.</p>
				</div>

				{isLoadingConfig ? (
					<p className="text-muted-foreground text-sm">Loading Odin configuration...</p>
				) : (
					<div className="space-y-4">
						<div className="flex items-center justify-between rounded-sm border p-4">
							<div className="space-y-0.5">
								<Label htmlFor="odin-enabled">Enable Odin</Label>
								<p className="text-muted-foreground text-sm">
									Adds the Odin panel to the dashboard. Odin reads logs, metrics and usage data from Bifrost on behalf of whoever asks,
									scoped to what that person is already allowed to see.
								</p>
							</div>
							<Switch
								id="odin-enabled"
								size="md"
								data-testid="odin-enabled-switch"
								checked={formValues.enabled}
								disabled={!hasSettingsUpdateAccess}
								onCheckedChange={(checked) => setValue("enabled", checked, { shouldDirty: true })}
							/>
						</div>

						<div className="space-y-2 rounded-sm border p-4">
							<div className="space-y-0.5">
								<Label htmlFor="odin-provider">Provider</Label>
								<p className="text-muted-foreground text-sm">
									The model provider that runs Odin, for example <code className="text-xs">openai</code> or{" "}
									<code className="text-xs">anthropic</code>.
								</p>
							</div>
							<Input
								id="odin-provider"
								type="text"
								placeholder="openai"
								data-testid="odin-provider-input"
								className={errors.provider ? "border-destructive" : ""}
								{...register("provider", {
									validate: (value) => !enabled || value.trim() !== "" || "Provider is required when Odin is enabled",
								})}
							/>
							{errors.provider && <p className="text-destructive text-sm">{errors.provider.message}</p>}
						</div>

						<div className="space-y-2 rounded-sm border p-4">
							<div className="space-y-0.5">
								<Label htmlFor="odin-model">Model</Label>
								<p className="text-muted-foreground text-sm">
									Odin reasons over query results and writes the answer, so a capable model pays for itself here.
								</p>
							</div>
							<Input
								id="odin-model"
								type="text"
								placeholder="gpt-4o"
								data-testid="odin-model-input"
								className={errors.model ? "border-destructive" : ""}
								{...register("model", {
									validate: (value) => !enabled || value.trim() !== "" || "Model is required when Odin is enabled",
								})}
							/>
							{errors.model && <p className="text-destructive text-sm">{errors.model.message}</p>}
						</div>

						<div className="space-y-2 rounded-sm border p-4">
							<div className="space-y-0.5">
								<Label htmlFor="odin-api-key">API Key</Label>
								<p className="text-muted-foreground text-sm">
									Accepts a literal key, or a reference like <code className="text-xs">env.OPENAI_API_KEY</code> or{" "}
									<code className="text-xs">vault.path/to/secret</code>. Stored encrypted and never returned by the API.
								</p>
							</div>
							{config?.api_key_set && !replacingKey ? (
								<div className="flex items-center gap-3">
									<span className="text-muted-foreground text-sm" data-testid="odin-api-key-set">
										A key is configured.
									</span>
									<Button
										type="button"
										variant="outline"
										size="sm"
										data-testid="odin-replace-key-btn"
										disabled={!hasSettingsUpdateAccess}
										onClick={() => setReplacingKey(true)}
									>
										Replace
									</Button>
								</div>
							) : (
								<div className="space-y-2">
									<Input
										id="odin-api-key"
										type="password"
										autoComplete="off"
										placeholder="sk-... or env.OPENAI_API_KEY"
										data-testid="odin-api-key-input"
										{...register("api_key")}
										onChange={(event) => {
											setReplacingKey(true);
											register("api_key").onChange(event);
										}}
									/>
									{config?.api_key_set && (
										<Button
											type="button"
											variant="ghost"
											size="sm"
											data-testid="odin-cancel-replace-key-btn"
											onClick={() => {
												setReplacingKey(false);
												setValue("api_key", "");
											}}
										>
											Keep the existing key
										</Button>
									)}
								</div>
							)}
						</div>

						<div className="space-y-2 rounded-sm border p-4">
							<div className="space-y-0.5">
								<Label htmlFor="odin-base-url">Base URL</Label>
								<p className="text-muted-foreground text-sm">
									Overrides the provider&apos;s default endpoint. Needed for self-hosted or proxied models; leave empty otherwise.
								</p>
							</div>
							<Input
								id="odin-base-url"
								type="text"
								placeholder="https://llm.internal.example.com/v1"
								data-testid="odin-base-url-input"
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
								<Label htmlFor="odin-max-iterations">Max Iterations</Label>
								<p className="text-muted-foreground text-sm">
									How many times Odin may query your data and reconsider before it has to answer. Each iteration is a billable round trip,
									so this is a cost ceiling as much as a quality setting.
								</p>
							</div>
							<Input
								id="odin-max-iterations"
								type="number"
								data-testid="odin-max-iterations-input"
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
								<Label htmlFor="odin-request-timeout">Request Timeout (seconds)</Label>
								<p className="text-muted-foreground text-sm">
									Bound on a single call to the model. Raise it for slower self-hosted models.
								</p>
							</div>
							<Input
								id="odin-request-timeout"
								type="number"
								data-testid="odin-request-timeout-input"
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
								<Label htmlFor="odin-system-prompt-suffix">Additional Instructions</Label>
								<p className="text-muted-foreground text-sm">
									Appended to Odin&apos;s built-in instructions. Useful for local naming conventions or how your teams are organised. It
									adds to the built-in prompt and cannot replace it.
								</p>
							</div>
							<Input
								id="odin-system-prompt-suffix"
								type="text"
								placeholder="Costs are in USD. Team IDs map to squads in Notion."
								data-testid="odin-system-prompt-suffix-input"
								{...register("system_prompt_suffix")}
							/>
						</div>
					</div>
				)}

				<div className="flex justify-end pt-2">
					<Button type="submit" disabled={!hasChanges || isLoading || !hasSettingsUpdateAccess} data-testid="odin-save-btn">
						{isLoading ? "Saving..." : "Save Changes"}
					</Button>
				</div>
			</form>
		</div>
	);
}