import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { getErrorMessage, useGetCoreConfigQuery, useUpdateCoreConfigMutation } from "@/lib/store";
import { CoreConfig, DefaultCoreConfig } from "@/lib/types/config";
import { parseArrayFromText } from "@/lib/utils/array";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { AlertTriangle } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

export default function ObservabilityView() {
	const { t } = useTranslation("config");
	const hasSettingsUpdateAccess = useRbac(RbacResource.Settings, RbacOperation.Update);
	const { data: bifrostConfig } = useGetCoreConfigQuery({ fromDB: true });
	const config = bifrostConfig?.client_config;
	const [updateCoreConfig, { isLoading }] = useUpdateCoreConfigMutation();
	const [localConfig, setLocalConfig] = useState<CoreConfig>(DefaultCoreConfig);
	const [needsRestart, setNeedsRestart] = useState<boolean>(false);

	const [localValues, setLocalValues] = useState<{
		prometheus_labels: string;
	}>({
		prometheus_labels: "",
	});

	useEffect(() => {
		if (bifrostConfig && config) {
			setLocalConfig(config);
			setLocalValues({
				prometheus_labels: config?.prometheus_labels?.join(", ") || "",
			});
		}
	}, [config, bifrostConfig]);

	const hasChanges = useMemo(() => {
		if (!config) return false;
		const localLabels = localConfig.prometheus_labels.slice().sort().join(",");
		const serverLabels = config.prometheus_labels.slice().sort().join(",");
		return localLabels !== serverLabels;
	}, [config, localConfig]);

	const handlePrometheusLabelsChange = useCallback((value: string) => {
		setLocalValues((prev) => ({ ...prev, prometheus_labels: value }));
		setLocalConfig((prev) => ({ ...prev, prometheus_labels: parseArrayFromText(value) }));
		setNeedsRestart(true);
	}, []);

	const handleSave = useCallback(async () => {
		if (!bifrostConfig) {
			toast.error(t("observability.toastNotLoaded"));
			return;
		}
		try {
			await updateCoreConfig({ ...bifrostConfig, client_config: localConfig }).unwrap();
			toast.success(t("observability.toastSaved"));
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	}, [bifrostConfig, localConfig, t, updateCoreConfig]);

	return (
		<div className="mx-auto w-full max-w-4xl space-y-4">
			<div className="flex items-center justify-between"></div>

			<Alert variant="destructive">
				<AlertTriangle className="h-4 w-4" />
				<AlertDescription>{t("restartRequiredAlert")}</AlertDescription>
			</Alert>

			<div className="space-y-4">
				<div>
					<div className="space-y-2 rounded-sm border p-4">
						<div className="space-y-0.5">
							<label htmlFor="prometheus-labels" className="text-sm font-medium">
								{t("observability.prometheusLabels")}
							</label>
							<p className="text-muted-foreground text-sm">{t("observability.prometheusLabelsHelp")}</p>
						</div>
						<Textarea
							id="prometheus-labels"
							className="h-24"
							placeholder={t("observability.prometheusLabelsPlaceholder")}
							value={localValues.prometheus_labels}
							onChange={(e) => handlePrometheusLabelsChange(e.target.value)}
						/>
					</div>
					{needsRestart && <RestartWarning />}
				</div>
			</div>
			<div className="flex justify-end pt-2">
				<Button onClick={handleSave} disabled={!hasChanges || isLoading || !hasSettingsUpdateAccess}>
					{isLoading ? t("saving") : t("saveChanges")}
				</Button>
			</div>
		</div>
	);
}

const RestartWarning = () => {
	const { t } = useTranslation("config");
	return <div className="text-muted-foreground mt-2 pl-4 text-xs font-semibold">{t("restartWarning")}</div>;
};
