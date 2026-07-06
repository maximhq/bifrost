import { EmptyStateView } from "@/components/emptyStateView";
import { Button } from "@/components/ui/button";
import { getErrorMessage, useGetCoreConfigQuery, useUpdateCoreConfigMutation } from "@/lib/store";
import { ScrollText } from "lucide-react";
import { useCallback } from "react";
import { toast } from "sonner";

export function LoggingDisabledView() {
	const { data: bifrostConfig } = useGetCoreConfigQuery({ fromDB: true });
	const [updateCoreConfig, { isLoading }] = useUpdateCoreConfigMutation();

	const handleEnable = useCallback(async () => {
		if (!bifrostConfig?.client_config) {
			toast.error("Configuration not loaded");
			return;
		}
		try {
			await updateCoreConfig({
				...bifrostConfig,
				client_config: { ...bifrostConfig.client_config, enable_logging: true },
			}).unwrap();
			toast.success("Logging enabled.");
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	}, [bifrostConfig, updateCoreConfig]);

	return (
		<EmptyStateView
			icon={ScrollText}
			title="Logging is disabled"
			description="Enable logging to view LLM and MCP request logs, traces, and observability data."
			actions={
				<Button onClick={handleEnable} disabled={isLoading}>
					{isLoading ? "Enabling…" : "Enable logging"}
				</Button>
			}
		/>
	);
}
