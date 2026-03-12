"use client";

import { Button } from "@/components/ui/button";
import { ArrowUpRight, Server } from "lucide-react";

const PROVIDERS_DOCS_URL = "https://docs.getbifrost.ai/providers/supported-providers/overview";

interface ProvidersEmptyStateProps {
	/** Dropdown (or button) for adding a provider; never greyed out */
	addProviderDropdown: React.ReactNode;
}

export function ProvidersEmptyState({ addProviderDropdown }: ProvidersEmptyStateProps) {
	return (
		<div className="flex min-h-[80vh] w-full flex-col items-center justify-center gap-4 py-16 text-center">
			<div className="text-muted-foreground">
				<Server className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />
			</div>
			<div className="flex flex-col gap-1">
				<h1 className="text-muted-foreground text-xl font-medium">Add a provider to start routing requests</h1>
				<div className="text-muted-foreground mx-auto mt-2 max-w-[600px] text-sm font-normal">
					Configure API keys for OpenAI, Anthropic, Bedrock, and other supported providers. Bifrost unifies them behind a single API.
				</div>
				<div className="mx-auto mt-6 flex flex-row flex-wrap items-center justify-center gap-2">
					<Button
						variant="outline"
						aria-label="Read more about providers (opens in new tab)"
						data-testid="providers-button-read-more"
						onClick={() => {
							window.open(`${PROVIDERS_DOCS_URL}?utm_source=bfd`, "_blank", "noopener,noreferrer");
						}}
					>
						Read more <ArrowUpRight className="text-muted-foreground h-3 w-3" />
					</Button>
					{addProviderDropdown}
				</div>
			</div>
		</div>
	);
}
