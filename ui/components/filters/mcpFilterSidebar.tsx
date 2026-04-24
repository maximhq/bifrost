import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Input } from "@/components/ui/input";
import { ScrollArea } from "@/components/ui/scrollArea";
import { Skeleton } from "@/components/ui/skeleton";
import { Statuses } from "@/lib/constants/logs";
import { useGetMCPLogsFilterDataQuery } from "@/lib/store";
import type { MCPToolLogFilters } from "@/lib/types/logs";
import { cn } from "@/lib/utils";
import { ChevronDown, PanelLeftClose, PanelLeftOpen, RotateCcw } from "lucide-react";
import { Ref, useCallback, useEffect, useMemo, useRef, useState } from "react";

const COLLAPSE_STORAGE_KEY = "mcp-filter-sidebar-collapsed";

// ---------------------------------------------------------------------------
// MCPFilterSidebar – orchestrator
// ---------------------------------------------------------------------------

interface MCPFilterSidebarProps {
	filters: MCPToolLogFilters;
	onFiltersChange: (filters: MCPToolLogFilters) => void;
}

export function MCPFilterSidebar({ filters, onFiltersChange }: MCPFilterSidebarProps) {
	const [collapsed, setCollapsed] = useState(false);

	// Load persisted collapsed state on mount
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
		const excludedKeys = ["start_time", "end_time", "content_search", "period", "polling"];
		let count = Object.entries(filters).reduce((c, [key, value]) => {
			if (excludedKeys.includes(key)) return c;
			if (Array.isArray(value)) return c + value.length;
			return c + (value ? 1 : 0);
		}, 0);
		return count;
	}, [filters]);

	const handleReset = useCallback(() => {
		onFiltersChange({
			start_time: filters.start_time,
			end_time: filters.end_time,
		});
	}, [filters.start_time, filters.end_time, onFiltersChange]);

	// Collapsed: thin rail with vertical "Filters" label — whole rail is clickable to expand
	if (collapsed) {
		return (
			<button
				type="button"
				onClick={toggleCollapsed}
				className="bg-card group flex h-full w-10 shrink-0 cursor-pointer flex-col items-center gap-3 rounded-r-md py-3 text-sm font-medium"
				title="Show filters"
				aria-label="Show filters"
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
			{/* Header */}
			<div className="flex h-11 items-center justify-between border-b pr-2 pl-5">
				<span className="text-sm font-semibold">Filters</span>
				<div className="flex items-center gap-1">
					{activeFilterCount > 0 && (
						<Button variant="outline" size="sm" className="text-muted-foreground h-7 px-2 text-xs" onClick={handleReset}>
							<RotateCcw className="size-3" />
							Reset
						</Button>
					)}
					<Button variant="ghost" size="icon" className="size-7" onClick={toggleCollapsed} title="Hide filters" aria-label="Hide filters">
						<PanelLeftClose className="size-4" />
					</Button>
				</div>
			</div>

			{/* Scrollable filter sections */}
			<ScrollArea className="flex flex-1 overflow-y-auto p-2 pb-0" viewportClassName="no-table">
				<div className="flex grow flex-col gap-1">
					{/* First 2 open by default */}
					<StatusFilter filters={filters} onFiltersChange={onFiltersChange} defaultOpen />
					<ToolNamesFilter filters={filters} onFiltersChange={onFiltersChange} defaultOpen />
					{/* Rest closed unless they have active filters */}
					<ServersFilter filters={filters} onFiltersChange={onFiltersChange} />
					<VirtualKeysFilter filters={filters} onFiltersChange={onFiltersChange} />
				</div>
			</ScrollArea>
		</div>
	);
}

// ---------------------------------------------------------------------------
// Shared helpers & primitives
// ---------------------------------------------------------------------------

interface FilterComponentProps {
	filters: MCPToolLogFilters;
	onFiltersChange: (filters: MCPToolLogFilters) => void;
	defaultOpen?: boolean;
}

// ---------------------------------------------------------------------------
// FilterSection – collapsible wrapper
// ---------------------------------------------------------------------------

function FilterSectionSkeleton({ rows = 3 }: { rows?: number }) {
	return (
		<>
			{Array.from({ length: rows }).map((_, i) => (
				<div key={i} className="flex items-center gap-2.5 px-3 py-2">
					<Skeleton className="size-4 shrink-0 rounded-[4px]" />
					<Skeleton className="h-3.5 w-full rounded" />
				</div>
			))}
		</>
	);
}

function FilterSection({
	title,
	children,
	defaultOpen = false,
	loading = false,
	onOpenChange,
}: {
	title: string;
	children: React.ReactNode;
	defaultOpen?: boolean;
	loading?: boolean;
	onOpenChange?: (open: boolean) => void;
}) {
	const [open, setOpen] = useState(defaultOpen);

	useEffect(() => {
		if (defaultOpen) setOpen(true);
	}, [defaultOpen]);

	const handleOpenChange = (next: boolean) => {
		setOpen(next);
		onOpenChange?.(next);
	};

	return (
		<Collapsible open={open} onOpenChange={handleOpenChange} className="last:pb-2">
			<CollapsibleTrigger className="flex h-8 w-full cursor-pointer items-center gap-1.5 px-2 py-2 text-sm font-medium hover:opacity-80">
				<ChevronDown className={cn("size-3.5 transition-transform", open ? "rotate-0" : "-rotate-90")} />
				<span>{title}</span>
			</CollapsibleTrigger>
			<CollapsibleContent className="pt-1">
				<div className="divide-border divide-y overflow-hidden rounded-sm border">{loading ? <FilterSectionSkeleton /> : children}</div>
			</CollapsibleContent>
		</Collapsible>
	);
}

// ---------------------------------------------------------------------------
// CheckboxFilterItem
// ---------------------------------------------------------------------------

function CheckboxFilterItem({
	label,
	checked,
	onCheckedChange,
	labelClassName,
}: {
	label: string;
	checked: boolean;
	onCheckedChange: (checked: boolean) => void;
	labelClassName?: string;
}) {
	return (
		<label className="hover:bg-muted/50 flex cursor-pointer items-center gap-2.5 px-3 py-2 text-sm">
			<Checkbox checked={checked} onCheckedChange={onCheckedChange} />
			<span className={cn("truncate", labelClassName)}>{label}</span>
		</label>
	);
}

// ---------------------------------------------------------------------------
// SearchableCheckboxList – list of checkbox rows with a search input.
// Caller passes `inputRef` to control focus (see `useAutoFocusOnOpen`).
// ---------------------------------------------------------------------------

function useAutoFocusOnOpen(isOpen: boolean) {
	const ref = useRef<HTMLInputElement>(null);
	useEffect(() => {
		if (isOpen) ref.current?.focus({ preventScroll: true });
	}, [isOpen]);
	return ref;
}

function SearchableCheckboxList({
	items,
	isSelected,
	onToggle,
	placeholder = "Search...",
	inputRef,
}: {
	items: { key: string; label: string }[];
	isSelected: (key: string) => boolean;
	onToggle: (key: string) => void;
	placeholder?: string;
	inputRef?: Ref<HTMLInputElement>;
}) {
	const [query, setQuery] = useState("");
	const normalized = query.trim().toLowerCase();
	const filtered = normalized ? items.filter((item) => item.label.toLowerCase().includes(normalized)) : items;

	return (
		<>
			<div className="border-b">
				<Input
					ref={inputRef}
					value={query}
					onChange={(e) => setQuery(e.target.value)}
					placeholder={placeholder}
					className="h-8 border-0 text-xs"
				/>
			</div>
			{filtered.map((item) => (
				<CheckboxFilterItem key={item.key} label={item.label} checked={isSelected(item.key)} onCheckedChange={() => onToggle(item.key)} />
			))}
			{filtered.length === 0 && <div className="text-muted-foreground flex h-9 items-center px-3 text-xs">No results</div>}
		</>
	);
}

// ---------------------------------------------------------------------------
// StatusFilter
// ---------------------------------------------------------------------------

function StatusFilter({ filters, onFiltersChange, defaultOpen }: FilterComponentProps) {
	const hasActive = (filters.status || []).length > 0;

	return (
		<FilterSection title="Status" defaultOpen={defaultOpen || hasActive}>
			{Statuses.map((status) => (
				<CheckboxFilterItem
					key={status}
					labelClassName="capitalize"
					label={status}
					checked={(filters.status || []).includes(status)}
					onCheckedChange={() => {
						const current = filters.status || [];
						const next = current.includes(status) ? current.filter((s) => s !== status) : [...current, status];
						onFiltersChange({ ...filters, status: next });
					}}
				/>
			))}
		</FilterSection>
	);
}

// ---------------------------------------------------------------------------
// ToolNamesFilter – fetches tool names; skips while closed & inactive
// ---------------------------------------------------------------------------

function ToolNamesFilter({ filters, onFiltersChange, defaultOpen }: FilterComponentProps) {
	const hasActive = (filters.tool_names || []).length > 0;
	const [opened, setOpened] = useState(defaultOpen || hasActive);
	const searchInputRef = useAutoFocusOnOpen(opened);
	const { data: filterData, isUninitialized, isLoading } = useGetMCPLogsFilterDataQuery(undefined, { skip: !opened && !hasActive });
	const availableToolNames = filterData?.tool_names || [];

	if (!isUninitialized && !isLoading && availableToolNames.length === 0 && !hasActive) return null;

	return (
		<FilterSection title="Tool Names" defaultOpen={defaultOpen || hasActive} loading={isLoading} onOpenChange={setOpened}>
			<SearchableCheckboxList
				inputRef={searchInputRef}
				placeholder="Search tools"
				items={availableToolNames.map((name) => ({ key: name, label: name }))}
				isSelected={(name) => (filters.tool_names || []).includes(name)}
				onToggle={(name) => {
					const current = filters.tool_names || [];
					const next = current.includes(name) ? current.filter((n) => n !== name) : [...current, name];
					onFiltersChange({ ...filters, tool_names: next });
				}}
			/>
		</FilterSection>
	);
}

// ---------------------------------------------------------------------------
// ServersFilter – fetches server labels; skips while closed & inactive
// ---------------------------------------------------------------------------

function ServersFilter({ filters, onFiltersChange, defaultOpen }: FilterComponentProps) {
	const hasActive = (filters.server_labels || []).length > 0;
	const [opened, setOpened] = useState(defaultOpen || hasActive);
	const searchInputRef = useAutoFocusOnOpen(opened);
	const { data: filterData, isUninitialized, isLoading } = useGetMCPLogsFilterDataQuery(undefined, { skip: !opened && !hasActive });
	const availableServerLabels = filterData?.server_labels || [];

	if (!isUninitialized && !isLoading && availableServerLabels.length === 0 && !hasActive) return null;

	return (
		<FilterSection title="Servers" defaultOpen={defaultOpen || hasActive} loading={isLoading} onOpenChange={setOpened}>
			<SearchableCheckboxList
				inputRef={searchInputRef}
				placeholder="Search servers"
				items={availableServerLabels.map((label) => ({ key: label, label }))}
				isSelected={(label) => (filters.server_labels || []).includes(label)}
				onToggle={(label) => {
					const current = filters.server_labels || [];
					const next = current.includes(label) ? current.filter((l) => l !== label) : [...current, label];
					onFiltersChange({ ...filters, server_labels: next });
				}}
			/>
		</FilterSection>
	);
}

// ---------------------------------------------------------------------------
// VirtualKeysFilter – fetches virtual keys; maps name→ID
// ---------------------------------------------------------------------------

function VirtualKeysFilter({ filters, onFiltersChange, defaultOpen }: FilterComponentProps) {
	const hasActive = (filters.virtual_key_ids || []).length > 0;
	const [opened, setOpened] = useState(defaultOpen || hasActive);
	const searchInputRef = useAutoFocusOnOpen(opened);
	const { data: filterData, isUninitialized, isLoading } = useGetMCPLogsFilterDataQuery(undefined, { skip: !opened && !hasActive });
	const availableVirtualKeys = filterData?.virtual_keys || [];
	const nameToId = useMemo(() => new Map(availableVirtualKeys.map((key) => [key.name, key.id])), [availableVirtualKeys]);

	if (!isUninitialized && !isLoading && availableVirtualKeys.length === 0 && !hasActive) return null;

	const isSelected = (name: string) => {
		const id = nameToId.get(name) || name;
		return (filters.virtual_key_ids || []).includes(id);
	};

	const toggle = (name: string) => {
		const id = nameToId.get(name) || name;
		const current = filters.virtual_key_ids || [];
		const next = current.includes(id) ? current.filter((v) => v !== id) : [...current, id];
		onFiltersChange({ ...filters, virtual_key_ids: next });
	};

	return (
		<FilterSection title="Virtual Keys" defaultOpen={defaultOpen || hasActive} loading={isLoading} onOpenChange={setOpened}>
			<SearchableCheckboxList
				inputRef={searchInputRef}
				placeholder="Search virtual keys"
				items={availableVirtualKeys.map((key) => ({ key: key.name, label: key.name }))}
				isSelected={isSelected}
				onToggle={toggle}
			/>
		</FilterSection>
	);
}