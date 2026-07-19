import { Button } from "@/components/ui/button";
import { ArrowUpRight, Webhook } from "lucide-react";

const WEBHOOKS_DOCS_URL = "https://docs.getbifrost.ai/features/webhooks";

interface WebhooksEmptyStateProps {
	onAddClick: () => void;
	canCreate: boolean;
}

export function WebhooksEmptyState({ onAddClick, canCreate }: WebhooksEmptyStateProps) {
	return (
		<div
			className="flex min-h-[80vh] w-full flex-col items-center justify-center gap-4 py-16 text-center"
			data-testid="webhooks-empty-state"
		>
			<div className="text-muted-foreground">
				<Webhook className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />
			</div>
			<div className="flex flex-col gap-1">
				<h1 className="text-muted-foreground text-xl font-medium">Get notified when async jobs finish</h1>
				<div className="text-muted-foreground mx-auto mt-2 max-w-[600px] text-sm font-normal">
					Register webhook endpoints and Bifrost will send a signed notification whenever an async inference job completes or fails, so you
					don't have to poll for results.
				</div>
				<div className="mx-auto mt-6 flex flex-row flex-wrap items-center justify-center gap-2">
					<Button
						variant="outline"
						onClick={() => {
							window.open(`${WEBHOOKS_DOCS_URL}?utm_source=bfd`, "_blank", "noopener,noreferrer");
						}}
					>
						Read more <ArrowUpRight className="text-muted-foreground h-3 w-3" />
					</Button>
					<Button onClick={onAddClick} disabled={!canCreate} data-testid="create-webhook-btn">
						Add Webhook Endpoint
					</Button>
				</div>
			</div>
		</div>
	);
}