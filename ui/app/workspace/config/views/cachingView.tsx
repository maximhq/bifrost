import { getErrorMessage, useGetCoreConfigQuery } from "@/lib/store";
import { useTranslation } from "react-i18next";
import PluginsForm from "./pluginsForm";

export default function CachingView() {
	const { t } = useTranslation();
	const { data: bifrostConfig, isLoading, error: configError } = useGetCoreConfigQuery({ fromDB: true });

	return (
		<div className="mx-auto w-full max-w-4xl space-y-4">
			<div>
				<h2 className="text-lg font-semibold tracking-tight">{t("workspace.config.caching.title")}</h2>
				<p className="text-muted-foreground text-sm">{t("workspace.config.caching.description")}</p>
			</div>

			{isLoading && (
				<div className="flex items-center justify-center py-8">
					<p className="text-muted-foreground">{t("workspace.config.caching.loadingConfiguration")}</p>
				</div>
			)}

			{configError !== undefined && (
				<div className="border-destructive/50 bg-destructive/10 rounded-lg border p-4">
					<p className="text-destructive text-sm font-medium">{t("workspace.config.caching.failedToLoadConfiguration")}</p>
					<p className="text-muted-foreground mt-1 text-sm">
						{getErrorMessage(configError) || t("workspace.config.caching.unexpectedError")}
					</p>
				</div>
			)}

			{!isLoading && !configError && <PluginsForm isVectorStoreEnabled={bifrostConfig?.is_cache_connected ?? false} />}
		</div>
	);
}