import { getErrorMessage, useAppSelector, useUpdatePluginMutation } from "@/lib/store";
import { type SecretVar, PrometheusFormSchema } from "@/lib/types/schemas";
import { toOptionalSecretVarPayload } from "@/lib/utils/secretVarForm";
import { useMemo } from "react";
import { toast } from "sonner";
import { PrometheusFormFragment } from "../../fragments/prometheusFormFragment";

interface PushGatewayConfig {
	enabled?: boolean;
	push_gateway_url?: string | SecretVar;
	job_name?: string;
	instance_id?: string;
	push_interval?: number;
	basic_auth?: {
		username?: string | SecretVar;
		password?: string | SecretVar;
	};
}

interface TelemetryConfig {
	metrics_enabled?: boolean;
	push_gateway?: PushGatewayConfig;
}

interface PrometheusViewProps {
	onDelete?: () => void;
	isDeleting?: boolean;
}

export default function PrometheusView({ onDelete, isDeleting }: PrometheusViewProps) {
	const selectedPlugin = useAppSelector((state) => state.plugin.selectedPlugin);
	const currentConfig = useMemo(() => {
		const telemetryConfig = (selectedPlugin?.config as TelemetryConfig) ?? {};
		const pushGateway = telemetryConfig.push_gateway ?? {};
		const metricsEnabled = telemetryConfig.metrics_enabled ?? true;
		return {
			...pushGateway,
			metrics_enabled: metricsEnabled,
			push_gateway_enabled: pushGateway.enabled ?? false,
		};
	}, [selectedPlugin]);

	const [updatePlugin] = useUpdatePluginMutation();
	const baseUrl = `${window.location.protocol}//${window.location.host}`;
	const metricsEndpoint = `${baseUrl}/metrics`;

	const handlePrometheusConfigSave = (config: PrometheusFormSchema): Promise<void> => {
		return new Promise((resolve, reject) => {
			const pushGatewayConfig: PushGatewayConfig = {
				enabled: config.push_gateway_enabled,
				push_gateway_url: config.prometheus_config.push_gateway_url,
				job_name: config.prometheus_config.job_name,
				instance_id: config.prometheus_config.instance_id || undefined,
				push_interval: config.prometheus_config.push_interval,
			};

			const username = toOptionalSecretVarPayload(config.prometheus_config.basic_auth_username);
			const password = toOptionalSecretVarPayload(config.prometheus_config.basic_auth_password);
			if (username && password) {
				pushGatewayConfig.basic_auth = { username, password };
			}

			// Plugin stays loaded as long as the connector exists; the two inner
			// toggles independently control the /metrics endpoint and push gateway.
			updatePlugin({
				name: "telemetry",
				data: {
					enabled: true,
					config: {
						metrics_enabled: config.metrics_enabled,
						push_gateway: pushGatewayConfig,
					},
				},
			})
				.unwrap()
				.then(() => {
					resolve();
					toast.success("Prometheus configuration updated successfully");
				})
				.catch((err) => {
					toast.error("Failed to update Prometheus configuration", {
						description: getErrorMessage(err),
					});
					reject(err);
				});
		});
	};

	return (
		<div className="flex w-full flex-col gap-4">
			<div className="flex w-full flex-col gap-3">
				<PrometheusFormFragment
					onSave={handlePrometheusConfigSave}
					currentConfig={currentConfig}
					metricsEndpoint={metricsEndpoint}
					onDelete={onDelete}
					isDeleting={isDeleting}
				/>
			</div>
		</div>
	);
}