import { Button } from "@/components/ui/button";
import { ArrowUpRight, Bot } from "lucide-react";
import { useTranslation } from "react-i18next";

const AGENT_GATEWAY_DOCS_URL = "https://docs.getbifrost.ai/features/agent-gateway/overview";

interface AgentsEmptyStateProps {
	onAddClick: () => void;
	canCreate?: boolean;
}

export function AgentsEmptyState({ onAddClick, canCreate = true }: AgentsEmptyStateProps) {
	const { t } = useTranslation("observability");
	return (
		<div className="flex min-h-[80vh] w-full flex-col items-center justify-center gap-4 py-16 text-center">
			<div className="text-muted-foreground">
				<Bot className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />
			</div>
			<div className="flex flex-col gap-1">
				<h1 className="text-muted-foreground text-xl font-medium">{t("agentGateway.emptyState.title")}</h1>
				<div className="text-muted-foreground mx-auto mt-2 w-full max-w-[600px] text-sm font-normal">
					{t("agentGateway.emptyState.description")}
				</div>
				<div className="mx-auto mt-6 flex flex-row flex-wrap items-center justify-center gap-2">
					<Button
						variant="outline"
						aria-label={t("agentGateway.emptyState.readMoreAria")}
						data-testid="agent-gateway-button-read-more"
						onClick={() => {
							window.open(`${AGENT_GATEWAY_DOCS_URL}?utm_source=bfd`, "_blank", "noopener,noreferrer");
						}}
					>
						{t("agentGateway.emptyState.readMore")} <ArrowUpRight className="text-muted-foreground h-3 w-3" />
					</Button>
					<Button
						aria-label={t("agentGateway.emptyState.registerAria")}
						onClick={onAddClick}
						disabled={!canCreate}
						data-testid="create-agent-btn"
					>
						{t("agentGateway.emptyState.newAgent")}
					</Button>
				</div>
			</div>
		</div>
	);
}