import FullPageLoader from "@/components/fullPageLoader";
import PageTitle from "@/components/pageTitle";
import { Button } from "@/components/ui/button";
import { SecretVarInput } from "@/components/ui/secretVarInput";
import { getErrorMessage, useGetCoreConfigQuery, useUpdateCoreConfigMutation } from "@/lib/store";
import { CoreConfig, DefaultCoreConfig } from "@/lib/types/config";
import { SecretVar } from "@/lib/types/schemas";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { toast } from "sonner";

const secretVarEquals = (a?: SecretVar, b?: SecretVar) =>
	(a?.value ?? "") === (b?.value ?? "") && (a?.ref ?? "") === (b?.ref ?? "") && (a?.type ?? "plain_text") === (b?.type ?? "plain_text");

export default function AgentGatewayView() {
	const hasSettingsUpdateAccess = useRbac(RbacResource.Settings, RbacOperation.Update);
	const { data: bifrostConfig, isLoading: isConfigLoading, error: configError, refetch } = useGetCoreConfigQuery({ fromDB: true });
	const config = bifrostConfig?.client_config;
	const [updateCoreConfig, { isLoading }] = useUpdateCoreConfigMutation();
	const [localConfig, setLocalConfig] = useState<CoreConfig>(DefaultCoreConfig);
	const draftRevision = useRef(0);
	const syncedRevision = useRef(0);

	useEffect(() => {
		if (bifrostConfig && config && draftRevision.current === syncedRevision.current) {
			setLocalConfig(config);
		}
	}, [config, bifrostConfig]);

	const hasChanges = useMemo(() => {
		if (!config) return false;
		return !secretVarEquals(localConfig.a2a_external_client_url, config.a2a_external_client_url);
	}, [config, localConfig]);

	const handleExternalURLChange = useCallback((value: SecretVar) => {
		draftRevision.current += 1;
		setLocalConfig((prev) => ({ ...prev, a2a_external_client_url: value }));
	}, []);

	const handleSave = useCallback(async () => {
		const savedRevision = draftRevision.current;
		try {
			if (!bifrostConfig) {
				toast.error("Configuration not loaded. Please refresh and try again.");
				return;
			}
			await updateCoreConfig({
				...bifrostConfig,
				client_config: {
					...bifrostConfig.client_config,
					a2a_external_client_url: localConfig.a2a_external_client_url,
				},
			}).unwrap();
			syncedRevision.current = savedRevision;
			toast.success("Agent Gateway settings updated successfully.");
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	}, [bifrostConfig, localConfig, updateCoreConfig]);

	if (isConfigLoading && !bifrostConfig) {
		return <FullPageLoader />;
	}

	if (configError && !bifrostConfig) {
		return (
			<div className="mx-auto w-full max-w-7xl space-y-4 px-4 py-6" data-testid="agent-gateway-settings-load-error">
				<PageTitle title="Agent Gateway Settings">Configure Agent Gateway (A2A) settings.</PageTitle>
				<div className="border-destructive/30 bg-destructive/5 flex flex-col gap-3 rounded-sm border p-4">
					<p className="text-destructive text-sm font-medium">Failed to load Agent Gateway settings.</p>
					<p className="text-muted-foreground text-sm">{getErrorMessage(configError)}</p>
					<div>
						<Button variant="outline" size="sm" onClick={() => void refetch()}>
							Retry
						</Button>
					</div>
				</div>
			</div>
		);
	}

	return (
		<div className="mx-auto w-full max-w-7xl space-y-4 px-4 py-6" data-testid="agent-gateway-settings-view">
			<PageTitle title="Agent Gateway Settings">Configure Agent Gateway (A2A) settings.</PageTitle>
			<div className="space-y-4">
				<div className="space-y-2 rounded-sm border p-4">
					<label htmlFor="a2a-external-client-url" className="text-sm font-medium">
						External Client URL
					</label>
					<p className="text-muted-foreground text-sm">
						Bifrost&apos;s public base URL, used for served agent cards and push-notification callback URLs. If left blank, agent cards fall
						back to the incoming <code className="text-xs">Host</code> header and <b>push notifications are disabled</b>. Supports env var
						syntax (e.g. <code className="text-xs">env.BIFROST_EXTERNAL_URL</code>).
					</p>
					<SecretVarInput
						id="a2a-external-client-url"
						data-testid="a2a-external-client-url-input"
						placeholder="https://bifrost.example.com or env.BIFROST_EXTERNAL_URL"
						value={localConfig.a2a_external_client_url}
						onChange={handleExternalURLChange}
						disabled={!config || !hasSettingsUpdateAccess}
					/>
				</div>
			</div>
			<div className="flex justify-end pt-2">
				<Button
					onClick={handleSave}
					disabled={!hasChanges || isLoading || !hasSettingsUpdateAccess}
					data-testid="agent-gateway-settings-save-btn"
				>
					{isLoading ? "Saving..." : "Save Changes"}
				</Button>
			</div>
		</div>
	);
}