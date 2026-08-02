// Inline filter row above the sessions table: search input + three multi-
// selects (kind, status, auth_mode) + a clear-filters affordance shown
// only when something is active.
//
// MCP-client filter is intentionally absent here. Adding it would need a
// separate source for the dropdown options (the existing list response
// only shows clients that have *sessions*, which is filter-dependent and
// would cause the option list to collapse as filters narrow). When we
// want it we'll piggyback on useGetMCPClientsQuery.

import { Button } from "@/components/ui/button";
import { ComboboxSelect } from "@/components/ui/combobox";
import { Input } from "@/components/ui/input";
import { Fingerprint, KeyRound, Search, UserRound, X } from "lucide-react";

// Labels mirror the Type column's TypeBadge ("OAuth" / "Headers") so the
// filter vocabulary matches what the user sees in the table.
const KIND_OPTIONS = [
	{ label: "OAuth", value: "token" },
	{ label: "Headers", value: "header" },
];

const STATUS_OPTIONS = [
	{ label: "Active", value: "active" },
	{ label: "Orphaned", value: "orphaned" },
	{ label: "Needs re-auth", value: "needs_reauth" },
	{ label: "Needs update", value: "needs_update" },
	{ label: "Pending", value: "pending" },
];

// Identity-mode icons match the glyphs used in BindingCell so the dropdown
// reads as the same vocabulary as the rendered table column.
const AUTH_MODE_OPTIONS = [
	{ label: "User", value: "user", icon: <UserRound className="size-3.5" /> },
	{ label: "Virtual key", value: "vk", icon: <KeyRound className="size-3.5" /> },
	{ label: "Session", value: "session", icon: <Fingerprint className="size-3.5" /> },
];

export interface SessionsFilterBarProps {
	search: string;
	onSearchChange: (value: string) => void;
	kindFilter: string[];
	onKindFilterChange: (value: string[]) => void;
	statusFilter: string[];
	onStatusFilterChange: (value: string[]) => void;
	authModeFilter: string[];
	onAuthModeFilterChange: (value: string[]) => void;
	hasActiveFilters: boolean;
	onClearFilters: () => void;
}

export default function SessionsFilterBar(props: SessionsFilterBarProps) {
	return (
		<div className="flex shrink-0 flex-wrap items-center gap-3">
			<div className="relative max-w-sm min-w-[200px] flex-1">
				<Search className="text-muted-foreground absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2" />
				<Input
					aria-label="Search sessions"
					placeholder="Search MCP, user, VK, session..."
					value={props.search}
					onChange={(e) => props.onSearchChange(e.target.value)}
					className="pl-9"
					data-testid="mcp-sessions-search-input"
				/>
			</div>
			<ComboboxSelect
				multiple
				disableSearch
				compactTrigger
				data-testid="mcp-sessions-kind-filter"
				options={KIND_OPTIONS}
				value={props.kindFilter}
				onValueChange={props.onKindFilterChange}
				placeholder="All types"
				className="h-9 w-[180px]"
			/>
			<ComboboxSelect
				multiple
				disableSearch
				compactTrigger
				data-testid="mcp-sessions-status-filter"
				options={STATUS_OPTIONS}
				value={props.statusFilter}
				onValueChange={props.onStatusFilterChange}
				placeholder="All statuses"
				className="h-9 w-[180px]"
			/>
			<ComboboxSelect
				multiple
				disableSearch
				compactTrigger
				data-testid="mcp-sessions-auth-mode-filter"
				options={AUTH_MODE_OPTIONS}
				value={props.authModeFilter}
				onValueChange={props.onAuthModeFilterChange}
				placeholder="All identities"
				className="h-9 w-[180px]"
			/>
			{props.hasActiveFilters && (
				<Button variant="ghost" size="sm" onClick={props.onClearFilters} data-testid="mcp-sessions-clear-filters-btn" className="h-9">
					<X className="h-4 w-4" />
					Clear filters
				</Button>
			)}
		</div>
	);
}