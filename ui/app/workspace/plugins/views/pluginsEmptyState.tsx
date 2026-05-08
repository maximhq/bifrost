import { Button } from "@/components/ui/button";
import { useTranslation } from "react-i18next";
import { Puzzle } from "lucide-react";
import { ArrowUpRight } from "lucide-react";

const CUSTOM_PLUGINS_DOCS_URL = "https://docs.getbifrost.ai/plugins";

interface PluginsEmptyStateProps {
	onCreateClick: () => void;
	canCreate?: boolean;
}

export function PluginsEmptyState({ onCreateClick, canCreate = true }: PluginsEmptyStateProps) {
	const { t } = useTranslation();
	return (
		<div
			className="flex min-h-[80vh] w-full flex-col items-center justify-center gap-4 py-16 text-center"
			data-testid="plugins-empty-state"
		>
			<div className="text-muted-foreground">
				<Puzzle className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />
			</div>
			<div className="flex flex-col gap-1">
				<h1 className="text-muted-foreground text-xl font-medium">{t("workspace.plugins.emptyState.title")}</h1>
				<div className="text-muted-foreground mx-auto mt-2 max-w-[600px] text-sm font-normal">
					{t("workspace.plugins.emptyState.description")}
				</div>
				<div className="mx-auto mt-6 flex flex-row flex-wrap items-center justify-center gap-2">
					<Button
						variant="outline"
						aria-label="Read more about custom plugins (opens in new tab)"
						data-testid="plugins-button-read-more"
						onClick={() => {
							window.open(`${CUSTOM_PLUGINS_DOCS_URL}?utm_source=bfd`, "_blank", "noopener,noreferrer");
						}}
					>
						{t("workspace.plugins.emptyState.readMore")} <ArrowUpRight className="text-muted-foreground h-3 w-3" />
					</Button>
					<Button
						aria-label="Create your first plugin"
						data-testid="plugins-button-install-new"
						onClick={onCreateClick}
						disabled={!canCreate}
					>
						{t("workspace.plugins.installNew")}
					</Button>
				</div>
			</div>
		</div>
	);
}