import { Button } from "@/components/ui/button";
import { getErrorMessage, useGetCoreConfigQuery, useUpdateCoreConfigMutation } from "@/lib/store";
import { cn } from "@/lib/utils";
import { useTranslation } from "react-i18next";
import { ScrollText } from "lucide-react";
import { useCallback } from "react";
import { toast } from "sonner";

export function LoggingDisabledView() {
	const { t } = useTranslation();
	const { data: bifrostConfig } = useGetCoreConfigQuery({ fromDB: true });
	const [updateCoreConfig, { isLoading }] = useUpdateCoreConfigMutation();

	const handleEnable = useCallback(async () => {
		if (!bifrostConfig?.client_config) {
			toast.error(t("loggingDisabled.configNotLoaded"));
			return;
		}
		try {
			await updateCoreConfig({
				...bifrostConfig,
				client_config: { ...bifrostConfig.client_config, enable_logging: true },
			}).unwrap();
			toast.success(t("loggingDisabled.loggingEnabled"));
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	}, [bifrostConfig, updateCoreConfig, t]);

	return (
		<div className={cn("flex flex-col items-center justify-center gap-4 text-center mx-auto w-full max-w-7xl min-h-[80vh]")}>
			<div className="text-muted-foreground">
				<ScrollText className="h-10 w-10" />
			</div>
			<div className="flex flex-col gap-1">
				<h1 className="text-muted-foreground text-xl font-medium">{t("loggingDisabled.title")}</h1>
				<div className="text-muted-foreground mt-2 max-w-[600px] text-sm font-normal">{t("loggingDisabled.description")}</div>
			</div>
			<Button onClick={handleEnable} disabled={isLoading}>
				{isLoading ? t("common.enabling") : t("loggingDisabled.enableButton")}
			</Button>
		</div>
	);
}