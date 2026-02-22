"use client";

import { Button } from "@/components/ui/button";
import { KeyRound } from "lucide-react";
import { ArrowUpRight } from "lucide-react";

const VIRTUAL_KEYS_DOCS_URL = "https://docs.getbifrost.ai/features/governance/virtual-keys";

interface VirtualKeysEmptyStateProps {
	onAddClick: () => void;
	canCreate?: boolean;
}

export function VirtualKeysEmptyState({ onAddClick, canCreate = true }: VirtualKeysEmptyStateProps) {
	return (
		<div className="flex min-h-[65vh] w-full flex-col items-center justify-center gap-4 py-16 text-center">
			<div className="text-muted-foreground">
				<KeyRound className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />
			</div>
			<div className="flex flex-col gap-1">
				<h1 className="text-muted-foreground text-xl font-medium">Virtual keys control access, budgets, and rate limits</h1>
				<div className="text-muted-foreground mx-auto mt-2 max-w-[600px] text-sm font-normal">
					Create virtual keys to assign permissions, spending limits, and usage quotas to teams, customers, or API clients.
				</div>
				<div className="mx-auto mt-6 flex flex-row flex-wrap items-center justify-center gap-2">
					<Button
						variant="outline"
						aria-label="Read more about virtual keys (opens in new tab)"
						onClick={() => {
							window.open(`${VIRTUAL_KEYS_DOCS_URL}?utm_source=bfd`, "_blank", "noopener,noreferrer");
						}}
					>
						Read more <ArrowUpRight className="text-muted-foreground h-3 w-3" />
					</Button>
					<Button
						aria-label="Add your first virtual key"
						onClick={onAddClick}
						disabled={!canCreate}
						data-testid="create-vk-btn"
					>
						Add Virtual Key
					</Button>
				</div>
			</div>
		</div>
	);
}
