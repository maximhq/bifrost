import FullPageLoader from "@/components/fullPageLoader";
import PageTitle from "@/components/pageTitle";
import { Button } from "@/components/ui/button";
import { SecretVarInput } from "@/components/ui/secretVarInput";
import { getErrorMessage, useGetCoreConfigQuery, useUpdateCoreConfigMutation } from "@/lib/store";
import { SecretVar } from "@/lib/types/schemas";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

const secretVarEquals = (a?: SecretVar, b?: SecretVar) =>
	(a?.value ?? "") === (b?.value ?? "") && (a?.ref ?? "") === (b?.ref ?? "") && (a?.type ?? "plain_text") === (b?.type ?? "plain_text");

export default function SkillsSettingsView() {
	const { t } = useTranslation("config");
	const { t: tShell } = useTranslation("shell");
	const hasSettingsUpdateAccess = useRbac(RbacResource.Settings, RbacOperation.Update);
	const { data: bifrostConfig, isLoading: isConfigLoading, error: configError, refetch } = useGetCoreConfigQuery({ fromDB: true });
	const config = bifrostConfig?.client_config;
	const [updateCoreConfig, { isLoading: isSaving }] = useUpdateCoreConfigMutation();
	const [externalURL, setExternalURL] = useState<SecretVar>();
	const draftRevision = useRef(0);
	const syncedRevision = useRef(0);

	useEffect(() => {
		if (config && draftRevision.current === syncedRevision.current) {
			setExternalURL(config.skills_external_client_url);
		}
	}, [config]);

	const hasChanges = Boolean(config) && !secretVarEquals(externalURL, config?.skills_external_client_url);

	const handleSave = async () => {
		if (!bifrostConfig) return;
		const savedRevision = draftRevision.current;
		try {
			await updateCoreConfig({
				...bifrostConfig,
				client_config: {
					...bifrostConfig.client_config,
					skills_external_client_url: externalURL,
				},
			}).unwrap();
			syncedRevision.current = savedRevision;
			toast.success(t("skillsSettings.toastSaved"));
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	if (isConfigLoading && !bifrostConfig) return <FullPageLoader />;

	if (configError && !bifrostConfig) {
		return (
			<div className="mx-auto w-full max-w-7xl space-y-4 px-4 py-6" data-testid="skills-settings-load-error">
				<PageTitle title={tShell("nav.skillsSettings")}>{t("skillsSettings.description")}</PageTitle>
				<div className="border-destructive/30 bg-destructive/5 space-y-3 rounded-sm border p-4">
					<p className="text-destructive text-sm font-medium">{t("skillsSettings.loadFailed")}</p>
					<p className="text-muted-foreground text-sm">{getErrorMessage(configError)}</p>
					<Button data-testid="skills-settings-retry-btn" variant="outline" size="sm" onClick={() => void refetch()}>
						{t("skillsSettings.retry")}
					</Button>
				</div>
			</div>
		);
	}

	return (
		<div className="mx-auto w-full max-w-7xl space-y-4 px-4 py-6" data-testid="skills-settings-view">
			<PageTitle title={tShell("nav.skillsSettings")}>{t("skillsSettings.description")}</PageTitle>
			<div className="space-y-2 rounded-sm border p-4">
				<label htmlFor="skills-external-client-url" className="text-sm font-medium">
					{t("skillsSettings.externalClientUrl")}
				</label>
				<p className="text-muted-foreground text-sm">{t("skillsSettings.externalClientUrlHelp")}</p>
				<SecretVarInput
					id="skills-external-client-url"
					data-testid="skills-external-client-url-input"
					placeholder="https://bifrost.example.com or env.BIFROST_SKILLS_URL"
					value={externalURL}
					onChange={(value) => {
						draftRevision.current += 1;
						if (secretVarEquals(value, config?.skills_external_client_url)) syncedRevision.current = draftRevision.current;
						setExternalURL(value);
					}}
					disabled={!config || !hasSettingsUpdateAccess}
				/>
			</div>
			<div className="flex justify-end pt-2">
				<Button data-testid="skills-settings-save-btn" onClick={handleSave} disabled={!hasChanges || isSaving || !hasSettingsUpdateAccess}>
					{isSaving ? t("saving") : t("saveChanges")}
				</Button>
			</div>
		</div>
	);
}