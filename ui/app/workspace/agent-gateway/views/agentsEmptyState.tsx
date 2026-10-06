import { Button } from "@/components/ui/button";
import { ArrowUpRight, Bot } from "lucide-react";

const AGENT_GATEWAY_DOCS_URL = "https://docs.getbifrost.ai/features/agent-gateway/overview";

interface AgentsEmptyStateProps {
	onAddClick: () => void;
	canCreate?: boolean;
}

export function AgentsEmptyState({ onAddClick, canCreate = true }: AgentsEmptyStateProps) {
	return (
		<div className="flex min-h-[80vh] w-full flex-col items-center justify-center gap-4 py-16 text-center">
			<div className="text-muted-foreground">
				<Bot className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />
			</div>
			<div className="flex flex-col gap-1">
				<h1 className="text-muted-foreground text-xl font-medium">Agents route A2A traffic through the gateway</h1>
				<div className="text-muted-foreground mx-auto mt-2 w-full max-w-[600px] text-sm font-normal">
					Register your first agent to serve its agent card and proxy A2A requests through Bifrost. Configure discovery, upstream
					authentication, and which virtual keys may reach it.
				</div>
				<div className="mx-auto mt-6 flex flex-row flex-wrap items-center justify-center gap-2">
					<Button
						variant="outline"
						aria-label="Read more about the Agent Gateway (opens in new tab)"
						data-testid="agent-gateway-button-read-more"
						onClick={() => {
							window.open(`${AGENT_GATEWAY_DOCS_URL}?utm_source=bfd`, "_blank", "noopener,noreferrer");
						}}
					>
						Read more <ArrowUpRight className="text-muted-foreground h-3 w-3" />
					</Button>
					<Button aria-label="Register your first agent" onClick={onAddClick} disabled={!canCreate} data-testid="create-agent-btn">
						New Agent
					</Button>
				</div>
			</div>
		</div>
	);
}