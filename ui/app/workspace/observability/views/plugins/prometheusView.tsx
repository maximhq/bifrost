"use client";

import { getErrorMessage, useAppSelector, useUpdatePluginMutation } from "@/lib/store";
import { PrometheusFormSchema } from "@/lib/types/schemas";
import { useMemo } from "react";
import { toast } from "sonner";
import { PrometheusFormFragment } from "../../fragments/prometheusFormFragment";

interface PushGatewayConfig {
	push_gateway_url?: string;
	job_name?: string;
	instance_id?: string;
	push_interval?: number;
	basic_auth?: {
		username?: string;
		password?: string;
	};
}

interface TelemetryConfig {
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
		return {
			...pushGateway,
			enabled: selectedPlugin?.enabled,
		};
	}, [selectedPlugin]);

	const [updatePlugin] = useUpdatePluginMutation();
	const baseUrl = `${window.location.protocol}//${window.location.host}`;
	const metricsEndpoint = `${baseUrl}/metrics`;

	const handlePrometheusConfigSave = (config: PrometheusFormSchema): Promise<void> => {
		return new Promise((resolve, reject) => {
			// Transform the form data to the telemetry plugin's push_gateway config format
			const pushGatewayConfig: PushGatewayConfig = {
				push_gateway_url: config.prometheus_config.push_gateway_url,
				job_name: config.prometheus_config.job_name,
				instance_id: config.prometheus_config.instance_id || undefined,
				push_interval: config.prometheus_config.push_interval,
			};

			// Add basic auth if both username and password are provided
			if (config.prometheus_config.basic_auth_username?.trim() && config.prometheus_config.basic_auth_password?.trim()) {
				pushGatewayConfig.basic_auth = {
					username: config.prometheus_config.basic_auth_username,
					password: config.prometheus_config.basic_auth_password,
				};
			}

			updatePlugin({
				name: "telemetry",
				data: {
					enabled: config.enabled,
					config: {
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
				<PrometheusFormFragment onSave={handlePrometheusConfigSave} currentConfig={currentConfig} metricsEndpoint={metricsEndpoint} onDelete={onDelete} isDeleting={isDeleting} />
			</div>
		</div>
	);
}
