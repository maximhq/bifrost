import { getErrorMessage, useAppSelector, useUpdatePluginMutation } from "@/lib/store";
import { OtelConfigSchema, OtelFormSchema } from "@/lib/types/schemas";
import { useTranslation } from "react-i18next";
import { useMemo } from "react";
import { toast } from "sonner";
import { OtelFormFragment } from "../../fragments/otelFormFragment";

interface OtelViewProps {
	onDelete?: () => void;
	isDeleting?: boolean;
}

export default function OtelView({ onDelete, isDeleting }: OtelViewProps) {
	const { t } = useTranslation();
	const selectedPlugin = useAppSelector((state) => state.plugin.selectedPlugin);
	const currentConfig = useMemo(
		() => ({ ...((selectedPlugin?.config as OtelConfigSchema) ?? {}), enabled: selectedPlugin?.enabled }),
		[selectedPlugin],
	);
	const [updatePlugin] = useUpdatePluginMutation();
	const baseUrl = `${window.location.protocol}//${window.location.host}`;

	const handleOtelConfigSave = (config: OtelFormSchema): Promise<void> => {
		return new Promise((resolve, reject) => {
			updatePlugin({
				name: "otel",
				data: {
					enabled: config.enabled,
					config: config.otel_config,
				},
			})
				.unwrap()
				.then(() => {
					resolve();
					toast.success(t("workspace.observability.otelForm.configurationUpdated"));
				})
				.catch((err) => {
					toast.error(t("workspace.observability.otelForm.configurationUpdateFailed"), {
						description: getErrorMessage(err),
					});
					reject(err);
				});
		});
	};

	return (
		<div className="flex w-full flex-col gap-4">
			<div className="flex w-full flex-col gap-3">
				<OtelFormFragment onSave={handleOtelConfigSave} currentConfig={currentConfig} onDelete={onDelete} isDeleting={isDeleting} />
			</div>
		</div>
	);
}