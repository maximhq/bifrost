import { Button } from "@/components/ui/button";
import { Route } from "lucide-react";
import { ArrowUpRight } from "lucide-react";
import { useTranslation } from "react-i18next";

const ROUTING_RULES_DOCS_URL = "https://docs.getbifrost.ai/providers/routing-rules";

interface RoutingRulesEmptyStateProps {
	onAddClick: () => void;
	canCreate?: boolean;
}

export function RoutingRulesEmptyState({ onAddClick, canCreate = true }: RoutingRulesEmptyStateProps) {
	const { t } = useTranslation();
	return (
		<div
			className="flex min-h-[80vh] w-full flex-col items-center justify-center gap-4 py-16 text-center"
			data-testid="routing-rules-empty-state"
		>
			<div className="text-muted-foreground">
				<Route className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />
			</div>
			<div className="flex flex-col gap-1">
				<h1 className="text-muted-foreground text-xl font-medium">{t("workspace.routingRules.emptyState.title")}</h1>
				<div className="text-muted-foreground mx-auto mt-2 max-w-[600px] text-sm font-normal">
					{t("workspace.routingRules.emptyState.description")}
				</div>
				<div className="mx-auto mt-6 flex flex-row flex-wrap items-center justify-center gap-2">
					<Button
						variant="outline"
						aria-label="Read more about routing rules (opens in new tab)"
						data-testid="routing-rules-empty-read-more"
						onClick={() => {
							window.open(`${ROUTING_RULES_DOCS_URL}?utm_source=bfd`, "_blank", "noopener,noreferrer");
						}}
					>
						{t("workspace.routingRules.emptyState.readMore")} <ArrowUpRight className="text-muted-foreground h-3 w-3" />
					</Button>
					<Button
						aria-label="Create your first routing rule"
						data-testid="create-routing-rule-btn"
						onClick={onAddClick}
						disabled={!canCreate}
					>
						{t("workspace.routingRules.newRule")}
					</Button>
				</div>
			</div>
		</div>
	);
}