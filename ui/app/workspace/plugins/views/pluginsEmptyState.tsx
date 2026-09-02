import { Button } from "@/components/ui/button";
import { ArrowUpRight, Puzzle } from "lucide-react";
import { useTranslation } from "react-i18next";

const CUSTOM_PLUGINS_DOCS_URL = "https://docs.getbifrost.ai/plugins";

interface PluginsEmptyStateProps {
	onCreateClick: () => void;
	canCreate?: boolean;
}

export function PluginsEmptyState({ onCreateClick, canCreate = true }: PluginsEmptyStateProps) {
	const { t } = useTranslation("governance");

	return (
		<div
			className="flex min-h-[80vh] w-full flex-col items-center justify-center gap-4 py-16 text-center"
			data-testid="plugins-empty-state"
		>
			<div className="text-muted-foreground">
				<Puzzle className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />
			</div>
			<div className="flex flex-col gap-1">
				<h1 className="text-muted-foreground text-xl font-medium">{t("plugins.emptyTitle")}</h1>
				<div className="text-muted-foreground mx-auto mt-2 w-full max-w-[600px] text-sm font-normal">{t("plugins.emptyDescription")}</div>
				<div className="mx-auto mt-6 flex flex-row flex-wrap items-center justify-center gap-2">
					<Button
						variant="outline"
						aria-label={t("plugins.readMoreAria")}
						data-testid="plugins-button-read-more"
						onClick={() => {
							window.open(`${CUSTOM_PLUGINS_DOCS_URL}?utm_source=bfd`, "_blank", "noopener,noreferrer");
						}}
					>
						{t("contactUs.readMore")} <ArrowUpRight className="text-muted-foreground h-3 w-3" />
					</Button>
					<Button
						aria-label={t("plugins.installNewAria")}
						data-testid="plugins-button-install-new"
						onClick={onCreateClick}
						disabled={!canCreate}
					>
						{t("plugins.installNew")}
					</Button>
				</div>
			</div>
		</div>
	);
}
