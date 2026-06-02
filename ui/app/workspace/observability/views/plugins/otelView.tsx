import { getErrorMessage, useAppSelector, useUpdatePluginMutation } from "@/lib/store";
import { OtelFormSchema } from "@/lib/types/schemas";
import { toHeaderStringMap } from "@/lib/utils/envVarForm";
import { useMemo } from "react";
import { toast } from "sonner";
import { OtelFormFragment } from "../../fragments/otelFormFragment";

interface OtelViewProps {
	onDelete?: () => void;
	isDeleting?: boolean;
}

export default function OtelView({ onDelete, isDeleting }: OtelViewProps) {
	const selectedPlugin = useAppSelector((state) => state.plugin.selectedPlugin);
	const currentConfig = useMemo(() => ({ config: selectedPlugin?.config, enabled: selectedPlugin?.enabled }), [selectedPlugin]);
	const [updatePlugin] = useUpdatePluginMutation();

	const handleOtelConfigSave = (config: OtelFormSchema): Promise<void> => {
		// The backend stores headers as a plain "env.VAR"/literal string map, so flatten the
		// EnvVar form values here. The config is sent as the { profiles: [...] } wrapper.
		const profiles = config.profiles.map((profile) => ({
			...profile,
			headers: toHeaderStringMap(profile.headers),
		}));

		return new Promise((resolve, reject) => {
			updatePlugin({
				name: "otel",
				data: {
					enabled: config.enabled,
					config: { profiles },
				},
			})
				.unwrap()
				.then(() => {
					resolve();
					toast.success("OTEL configuration updated successfully");
				})
				.catch((err) => {
					toast.error("Failed to update OTEL configuration", {
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