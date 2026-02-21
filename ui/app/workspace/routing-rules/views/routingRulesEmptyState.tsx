"use client";

import { Button } from "@/components/ui/button";
import { Route } from "lucide-react";
import { ArrowUpRight } from "lucide-react";

const ROUTING_RULES_DOCS_URL = "https://docs.getbifrost.ai/providers/routing-rules";

interface RoutingRulesEmptyStateProps {
	onAddClick: () => void;
	canCreate?: boolean;
}

export function RoutingRulesEmptyState({ onAddClick, canCreate = true }: RoutingRulesEmptyStateProps) {
	return (
		<div className="flex min-h-[65vh] w-full flex-col items-center justify-center gap-4 py-16 text-center">
			<div className="text-muted-foreground">
				<Route className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />
			</div>
			<div className="flex flex-col gap-1">
				<h1 className="text-muted-foreground text-xl font-medium">Routing rules direct requests using CEL conditions</h1>
				<div className="text-muted-foreground mx-auto mt-2 max-w-[600px] text-sm font-normal">
					Create CEL-based rules to route requests by model, provider, budget, or custom attributes. Control which provider or model handles each request.
				</div>
				<div className="mx-auto mt-6 flex flex-row flex-wrap items-center justify-center gap-2">
					<Button
						variant="outline"
						aria-label="Read more about routing rules (opens in new tab)"
						onClick={() => {
							window.open(`${ROUTING_RULES_DOCS_URL}?utm_source=bfd`, "_blank", "noopener,noreferrer");
						}}
					>
						Read more <ArrowUpRight className="text-muted-foreground h-3 w-3" />
					</Button>
					<Button
						aria-label="Create your first routing rule"
						onClick={onAddClick}
						disabled={!canCreate}
					>
						New Rule
					</Button>
				</div>
			</div>
		</div>
	);
}
