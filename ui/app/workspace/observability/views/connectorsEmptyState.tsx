"use client";

import { Button } from "@/components/ui/button";
import { ArrowUpRight, LineChart } from "lucide-react";

const OBSERVABILITY_CONNECTORS_DOCS_URL =
	"https://docs.getbifrost.ai/features/observability/default";

export type ConnectorOption = {
	id: string;
	name: string;
	icon: React.ReactNode;
};

interface ConnectorsEmptyStateProps {
	/** Called when user clicks Add connector — typically opens the add-connector dialog */
	onOpenAddConnector: () => void;
}

export function ConnectorsEmptyState({ onOpenAddConnector }: ConnectorsEmptyStateProps) {
	return (
		<div className="flex min-h-[80vh] w-full flex-col items-center justify-center gap-4 py-16 text-center">
			<div className="text-muted-foreground">
				<LineChart className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />
			</div>
			<div className="flex flex-col gap-1">
				<h1 className="text-muted-foreground text-xl font-medium">
					Connect Bifrost to your observability stack
				</h1>
				<div className="text-muted-foreground mx-auto mt-2 max-w-[600px] text-sm font-normal">
					Add OpenTelemetry, Prometheus, Maxim, or other connectors to send metrics and traces from Bifrost to your preferred platform.
				</div>
				<div className="mx-auto mt-6 flex flex-row flex-wrap items-center justify-center gap-2">
					<Button
						variant="outline"
						aria-label="Read more about observability connectors (opens in new tab)"
						onClick={() => {
							window.open(
								`${OBSERVABILITY_CONNECTORS_DOCS_URL}?utm_source=bfd`,
								"_blank",
								"noopener,noreferrer",
							);
						}}
					>
						Read more <ArrowUpRight className="text-muted-foreground h-3 w-3" />
					</Button>
					<Button aria-label="Add connector" data-testid="add-connector-btn" onClick={onOpenAddConnector}>
						Add connector
					</Button>
				</div>
			</div>
		</div>
	);
}
