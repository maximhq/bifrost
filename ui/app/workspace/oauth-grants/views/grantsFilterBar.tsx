// Filter row above the grants table: free-text search (client / identity) and
// a multi-select on the grant's identity mode (user / vk / session), plus a
// clear-filters affordance shown only when something is active.

import { Button } from "@/components/ui/button";
import { ComboboxSelect } from "@/components/ui/combobox";
import { Input } from "@/components/ui/input";
import { Fingerprint, KeyRound, Search, UserRound, X } from "lucide-react";

const MODE_OPTIONS = [
	{ label: "User", value: "user", icon: <UserRound className="size-3.5" /> },
	{ label: "Virtual key", value: "vk", icon: <KeyRound className="size-3.5" /> },
	{ label: "Session", value: "session", icon: <Fingerprint className="size-3.5" /> },
];

interface GrantsFilterBarProps {
	search: string;
	onSearchChange: (value: string) => void;
	modeFilter: string[];
	onModeChange: (value: string[]) => void;
	hasActiveFilters: boolean;
	onClearFilters: () => void;
}

export default function GrantsFilterBar({
	search,
	onSearchChange,
	modeFilter,
	onModeChange,
	hasActiveFilters,
	onClearFilters,
}: GrantsFilterBarProps) {
	return (
		<div className="flex shrink-0 flex-wrap items-center gap-3">
			<div className="relative max-w-sm flex-1 min-w-[200px]">
				<Search className="text-muted-foreground absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2" />
				<Input
					data-testid="oauth-grants-search-input"
					aria-label="Search grants"
					placeholder="Search client, identity..."
					value={search}
					onChange={(e) => onSearchChange(e.target.value)}
					className="pl-9"
				/>
			</div>
			<ComboboxSelect
				data-testid="oauth-grants-mode-filter"
				multiple
				disableSearch
				compactTrigger
				options={MODE_OPTIONS}
				value={modeFilter}
				onValueChange={onModeChange}
				placeholder="All identities"
				className="h-9 w-[180px]"
			/>
			{hasActiveFilters && (
				<Button data-testid="oauth-grants-clear-filters-btn" variant="ghost" size="sm" onClick={onClearFilters} className="h-9">
					<X className="h-4 w-4" />
					Clear filters
				</Button>
			)}
		</div>
	);
}
