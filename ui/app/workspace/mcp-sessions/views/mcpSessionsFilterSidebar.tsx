// Side filter panel for the MCP sessions table, matching the pattern used by
// the MCP clients and MCP library pages: a collapsible sidebar of checkbox
// sections, collapsing to a narrow rail with an active-filter-count badge.
// State/behavior mirrors mcpClientsFilterSidebar.tsx; kept as its own copy
// since the two pages filter on unrelated fields.

import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { ScrollArea } from "@/components/ui/scrollArea";
import { cn } from "@/lib/utils";
import { ChevronDown, Fingerprint, KeyRound, PanelLeftClose, PanelLeftOpen, RotateCcw, UserRound } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";

const COLLAPSE_STORAGE_KEY = "mcp-sessions-filter-sidebar-collapsed";

// ---------------------------------------------------------------------------
// Filter types
// ---------------------------------------------------------------------------

export interface MCPSessionFilters {
	kind: string[]; // subset of ["token", "header"]
	status: string[]; // subset of ["active", "orphaned", "needs_reauth", "needs_update", "pending"]
	auth_mode: string[]; // subset of ["user", "vk", "session"]
}

export const EMPTY_FILTERS: MCPSessionFilters = {
	kind: [],
	status: [],
	auth_mode: [],
};

interface FilterOption {
	value: string;
	label: string;
	icon?: React.ReactNode;
}

// Labels mirror the Type column's TypeBadge ("OAuth" / "Headers") so the
// filter vocabulary matches what the user sees in the table.
const KIND_OPTIONS: FilterOption[] = [
	{ value: "token", label: "OAuth" },
	{ value: "header", label: "Headers" },
];

const STATUS_OPTIONS: FilterOption[] = [
	{ value: "active", label: "Active" },
	{ value: "orphaned", label: "Orphaned" },
	{ value: "needs_reauth", label: "Needs re-auth" },
	{ value: "needs_update", label: "Needs update" },
	{ value: "pending", label: "Pending" },
];

// Identity-mode icons match the glyphs used in BindingCell so the sidebar
// reads as the same vocabulary as the rendered table column.
const AUTH_MODE_OPTIONS: FilterOption[] = [
	{ value: "user", label: "User", icon: <UserRound className="size-3.5" /> },
	{ value: "vk", label: "Virtual key", icon: <KeyRound className="size-3.5" /> },
	{ value: "session", label: "Session", icon: <Fingerprint className="size-3.5" /> },
];

interface SidebarProps {
	filters: MCPSessionFilters;
	onFiltersChange: (filters: MCPSessionFilters) => void;
}

// ---------------------------------------------------------------------------
// MCPSessionsFilterSidebar – orchestrator
// ---------------------------------------------------------------------------

export function MCPSessionsFilterSidebar({ filters, onFiltersChange }: SidebarProps) {
	const [collapsed, setCollapsed] = useState(false);

	useEffect(() => {
		if (typeof window === "undefined") return;
		const stored = window.localStorage.getItem(COLLAPSE_STORAGE_KEY);
		if (stored === "true") setCollapsed(true);
	}, []);

	const toggleCollapsed = useCallback(() => {
		setCollapsed((prev) => {
			const next = !prev;
			if (typeof window !== "undefined") {
				window.localStorage.setItem(COLLAPSE_STORAGE_KEY, String(next));
			}
			return next;
		});
	}, []);

	const activeFilterCount = useMemo(() => {
		return filters.kind.length + filters.status.length + filters.auth_mode.length;
	}, [filters]);

	const handleReset = useCallback(() => {
		onFiltersChange(EMPTY_FILTERS);
	}, [onFiltersChange]);

	if (collapsed) {
		return (
			<button
				type="button"
				onClick={toggleCollapsed}
				className="bg-card group flex h-full w-10 shrink-0 cursor-pointer flex-col items-center gap-3 rounded-r-md py-4 text-sm font-medium"
				title="Show filters"
				aria-label="Show filters"
				data-testid="mcp-sessions-filter-sidebar-toggle-show"
			>
				<PanelLeftOpen className="text-muted-foreground group-hover:text-foreground size-4 transition-colors" />
				<span className="rotate-180 select-none [writing-mode:vertical-rl]">Filters</span>
				{activeFilterCount > 0 && (
					<span className="bg-primary/10 text-primary flex size-6 items-center justify-center rounded-full text-xs font-medium">
						{activeFilterCount}
					</span>
				)}
			</button>
		);
	}

	return (
		<div className="bg-card flex h-full w-64 shrink-0 flex-col rounded-r-md">
			<div className="flex h-11 items-center justify-between border-b pr-2 pl-5">
				<span className="text-sm font-semibold">Filters</span>
				<div className="flex items-center gap-1">
					{activeFilterCount > 0 && (
						<Button
							variant="outline"
							size="sm"
							className="text-muted-foreground h-7 px-2 text-xs"
							onClick={handleReset}
							data-testid="mcp-sessions-filter-sidebar-reset-button"
						>
							<RotateCcw className="size-3" />
							Reset
						</Button>
					)}
					<Button
						variant="ghost"
						size="icon"
						className="size-7"
						onClick={toggleCollapsed}
						title="Hide filters"
						aria-label="Hide filters"
						data-testid="mcp-sessions-filter-sidebar-toggle-hide"
					>
						<PanelLeftClose className="size-4" />
					</Button>
				</div>
			</div>

			<ScrollArea className="flex flex-1 overflow-y-auto p-2 pb-0" viewportClassName="no-table">
				<div className="flex grow flex-col gap-1">
					<CheckboxFilterSection
						title="Type"
						options={KIND_OPTIONS}
						selected={filters.kind}
						defaultOpen
						onChange={(kind) => onFiltersChange({ ...filters, kind })}
						testIdPrefix="mcp-sessions-filter-kind"
					/>
					<CheckboxFilterSection
						title="Status"
						options={STATUS_OPTIONS}
						selected={filters.status}
						onChange={(status) => onFiltersChange({ ...filters, status })}
						testIdPrefix="mcp-sessions-filter-status"
					/>
					<CheckboxFilterSection
						title="Identity"
						options={AUTH_MODE_OPTIONS}
						selected={filters.auth_mode}
						onChange={(auth_mode) => onFiltersChange({ ...filters, auth_mode })}
						testIdPrefix="mcp-sessions-filter-auth-mode"
					/>
				</div>
			</ScrollArea>
		</div>
	);
}

// ---------------------------------------------------------------------------
// Shared primitives
// ---------------------------------------------------------------------------

function FilterSection({
	title,
	children,
	defaultOpen = false,
	testId,
}: {
	title: string;
	children: React.ReactNode;
	defaultOpen?: boolean;
	testId?: string;
}) {
	const [open, setOpen] = useState(defaultOpen);

	useEffect(() => {
		if (defaultOpen) setOpen(true);
	}, [defaultOpen]);

	return (
		<Collapsible open={open} onOpenChange={setOpen} className="last:pb-2">
			<CollapsibleTrigger
				className="flex h-8 w-full cursor-pointer items-center gap-1.5 px-2 py-2 text-sm font-medium hover:opacity-80"
				data-testid={testId}
			>
				<ChevronDown className={cn("size-3.5 transition-transform", open ? "rotate-0" : "-rotate-90")} />
				<span>{title}</span>
			</CollapsibleTrigger>
			<CollapsibleContent className="pt-1">
				<div className="divide-border divide-y overflow-hidden rounded-sm border">{children}</div>
			</CollapsibleContent>
		</Collapsible>
	);
}

function CheckboxFilterItem({
	label,
	icon,
	checked,
	onCheckedChange,
	testId,
}: {
	label: string;
	icon?: React.ReactNode;
	checked: boolean;
	onCheckedChange: (checked: boolean) => void;
	testId?: string;
}) {
	return (
		<label className="hover:bg-muted/50 flex cursor-pointer items-center gap-2.5 px-3 py-2 text-sm" data-testid={testId}>
			<Checkbox checked={checked} onCheckedChange={onCheckedChange} />
			{icon && <span className="text-muted-foreground">{icon}</span>}
			<span className="truncate">{label}</span>
		</label>
	);
}

function CheckboxFilterSection({
	title,
	options,
	selected,
	defaultOpen = false,
	onChange,
	testIdPrefix,
}: {
	title: string;
	options: FilterOption[];
	selected: string[];
	defaultOpen?: boolean;
	onChange: (selected: string[]) => void;
	testIdPrefix?: string;
}) {
	const hasActive = selected.length > 0;

	const toggle = (value: string) => {
		if (selected.includes(value)) {
			onChange(selected.filter((v) => v !== value));
		} else {
			onChange([...selected, value]);
		}
	};

	return (
		<FilterSection title={title} defaultOpen={defaultOpen || hasActive} testId={testIdPrefix ? `${testIdPrefix}-toggle` : undefined}>
			{options.map((option) => (
				<CheckboxFilterItem
					key={option.value}
					label={option.label}
					icon={option.icon}
					checked={selected.includes(option.value)}
					onCheckedChange={() => toggle(option.value)}
					testId={testIdPrefix ? `${testIdPrefix}-checkbox-${option.value}` : undefined}
				/>
			))}
		</FilterSection>
	);
}
