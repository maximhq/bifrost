import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { getErrorMessage, useGetCoreConfigQuery, useUpdateCoreConfigMutation } from "@/lib/store";
import { CompatConfig, DefaultCoreConfig } from "@/lib/types/config";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { useCallback, useEffect, useMemo, useState } from "react";
import { toast } from "sonner";

export default function CompatibilityView() {
	const hasSettingsUpdateAccess = useRbac(RbacResource.Settings, RbacOperation.Update);
	const { data: bifrostConfig } = useGetCoreConfigQuery({ fromDB: true });
	const config = bifrostConfig?.client_config?.compat;
	const [updateCoreConfig, { isLoading }] = useUpdateCoreConfigMutation();
	const [localCompatConfig, setLocalCompatConfig] = useState<CompatConfig>(DefaultCoreConfig.compat);

	useEffect(() => {
		if (config) {
			setLocalCompatConfig(config);
			return;
		}
		setLocalCompatConfig(DefaultCoreConfig.compat);
	}, [config]);

	const hasChanges = useMemo(() => {
		const baseline = config ?? DefaultCoreConfig.compat;
		return (
			localCompatConfig.convert_text_to_chat !== baseline.convert_text_to_chat ||
			localCompatConfig.convert_chat_to_responses !== baseline.convert_chat_to_responses ||
			localCompatConfig.should_drop_params !== baseline.should_drop_params ||
			localCompatConfig.should_convert_params !== baseline.should_convert_params
		);
	}, [config, localCompatConfig]);

	const handleCompatChange = useCallback((field: keyof CompatConfig, value: boolean) => {
		setLocalCompatConfig((prev) => ({ ...prev, [field]: value }));
	}, []);

	const handleSave = useCallback(async () => {
		if (!bifrostConfig) {
			toast.error("Configuration not loaded");
			return;
		}

		try {
			await updateCoreConfig({
				...bifrostConfig,
				client_config: {
					...(bifrostConfig.client_config ?? DefaultCoreConfig),
					compat: localCompatConfig,
				},
			}).unwrap();
			toast.success("Compatibility settings updated successfully.");
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	}, [bifrostConfig, localCompatConfig, updateCoreConfig]);

	return (
		<div className="mx-auto w-full max-w-4xl space-y-6">
			<div>
				<h2 className="text-lg font-semibold tracking-tight">Compatibility</h2>
				<p className="text-muted-foreground text-sm">
					Configure request conversions and compatibility fallbacks.{" "}
					<a
						className="text-primary underline"
						href="https://docs.getbifrost.ai/features/litellm-compat"
						target="_blank"
						rel="noopener noreferrer"
						data-testid="litellm-docs-link"
					>
						Learn more
					</a>
				</p>
			</div>

			<div className="space-y-4">
				<div className="flex items-center justify-between space-x-2">
					<div className="space-y-0.5">
						<label htmlFor="compat-convert-text-to-chat" className="text-sm font-medium">
							Convert Text to Chat
						</label>
						<p className="text-muted-foreground text-sm">Convert text completion requests to chat for models that only support chat.</p>
					</div>
					<Switch
						id="compat-convert-text-to-chat"
						data-testid="compat-convert-text-to-chat"
						size="md"
						checked={localCompatConfig.convert_text_to_chat}
						onCheckedChange={(checked) => handleCompatChange("convert_text_to_chat", checked)}
						disabled={!hasSettingsUpdateAccess}
					/>
				</div>

				<div className="flex items-center justify-between space-x-2">
					<div className="space-y-0.5">
						<label htmlFor="compat-convert-chat-to-responses" className="text-sm font-medium">
							Convert Chat to Responses
						</label>
						<p className="text-muted-foreground text-sm">
							Convert chat completion requests to responses for models that only support responses.
						</p>
					</div>
					<Switch
						id="compat-convert-chat-to-responses"
						data-testid="compat-convert-chat-to-responses"
						size="md"
						checked={localCompatConfig.convert_chat_to_responses}
						onCheckedChange={(checked) => handleCompatChange("convert_chat_to_responses", checked)}
						disabled={!hasSettingsUpdateAccess}
					/>
				</div>

				<div className="flex items-center justify-between space-x-2">
					<div className="space-y-0.5">
						<label htmlFor="compat-should-drop-params" className="text-sm font-medium">
							Drop Unsupported Params
						</label>
						<p className="text-muted-foreground text-sm">Drop unsupported parameters based on model catalog allowlist.</p>
					</div>
					<Switch
						id="compat-should-drop-params"
						data-testid="compat-should-drop-params"
						size="md"
						checked={localCompatConfig.should_drop_params}
						onCheckedChange={(checked) => handleCompatChange("should_drop_params", checked)}
						disabled={!hasSettingsUpdateAccess}
					/>
				</div>

				<div className="flex items-center justify-between space-x-2">
					<div className="space-y-0.5">
						<label htmlFor="compat-should-convert-params" className="text-sm font-medium">
							Convert Unsupported Parameter Values
						</label>
						<p className="text-muted-foreground text-sm">Converts model parameter values that are not supported by the model.</p>
					</div>
					<Switch
						id="compat-should-convert-params"
						data-testid="compat-should-convert-params"
						size="md"
						checked={localCompatConfig.should_convert_params}
						onCheckedChange={(checked) => handleCompatChange("should_convert_params", checked)}
						disabled={!hasSettingsUpdateAccess}
					/>
				</div>
			</div>

			<div className="flex justify-end pt-2">
				<Button onClick={handleSave} disabled={!hasChanges || isLoading || !hasSettingsUpdateAccess} data-testid="compat-save-button">
					{isLoading ? "Saving..." : "Save Changes"}
				</Button>
			</div>
		</div>
	);
}