"use client";

import { Button } from "@/components/ui/button";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "@/components/ui/dropdownMenu";
import { ProviderLabels } from "@/lib/constants/logs";
import { RenderProviderIcon } from "@/lib/constants/icons";
import { ProviderIconType } from "@/lib/constants/icons";
import { PlusIcon, Settings2Icon } from "lucide-react";

export type ProviderOption = { name: string };

interface AddProviderDropdownProps {
	/** Provider names that are already in the sidebar (configured or added) */
	existingInSidebar: Set<string>;
	/** All known provider options to show (e.g. from ProviderNames / allProviders) */
	knownProviders: ProviderOption[];
	onSelectKnownProvider: (name: string) => void;
	onAddCustomProvider: () => void;
	/** Optional: use compact trigger for empty state */
	variant?: "default" | "empty";
}

export function AddProviderDropdown({
	existingInSidebar,
	knownProviders,
	onSelectKnownProvider,
	onAddCustomProvider,
	variant = "default",
}: AddProviderDropdownProps) {
	const availableKnown = knownProviders.filter((p) => !existingInSidebar.has(p.name));
	const hasKnown = availableKnown.length > 0;

	return (
		<DropdownMenu>
			<DropdownMenuTrigger asChild>
				<Button
					variant="outline"
					size={variant === "empty" ? "default" : "sm"}
					data-testid="add-provider-btn"
					className={variant === "empty" ? "" : "w-full justify-start"}
					aria-label="Add new provider"
				>
					<PlusIcon className="h-4 w-4" />
					{variant === "empty" ? (
						<span>Add provider</span>
					) : (
						<div className="text-xs">Add New Provider</div>
					)}
				</Button>
			</DropdownMenuTrigger>
			<DropdownMenuContent align="start" className="max-h-[min(70vh,24rem)] overflow-y-auto" data-testid="add-provider-dropdown">
				{availableKnown.map((p) => (
					<DropdownMenuItem
						key={p.name}
						data-testid={`add-provider-option-${p.name}`}
						onSelect={() => onSelectKnownProvider(p.name)}
					>
						<RenderProviderIcon provider={p.name as ProviderIconType} size="sm" className="h-4 w-4" />
						<span>{ProviderLabels[p.name as keyof typeof ProviderLabels] ?? p.name}</span>
					</DropdownMenuItem>
				))}
				{hasKnown && <DropdownMenuSeparator />}
				<DropdownMenuItem
					data-testid="add-provider-option-custom"
					onSelect={onAddCustomProvider}
				>
					<Settings2Icon className="h-4 w-4" />
					<span>Custom provider...</span>
				</DropdownMenuItem>
			</DropdownMenuContent>
		</DropdownMenu>
	);
}
