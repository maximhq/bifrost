import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Input } from "@/components/ui/input";
import { ScrollArea } from "@/components/ui/scrollArea";
import { Skeleton } from "@/components/ui/skeleton";
import { useGetMCPLibraryFilterDataQuery } from "@/lib/store";
import { cn } from "@/lib/utils";
import { ChevronDown, PanelLeftClose, PanelLeftOpen, RotateCcw, Search } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";

const COLLAPSE_STORAGE_KEY = "mcp-library-filter-sidebar-collapsed";

// ---------------------------------------------------------------------------
// Filter types
// ---------------------------------------------------------------------------

export interface MCPLibraryFilters {
	categories: string[];
	connection_types: string[];
	auth_types: string[];
	tags: string[];
}

export const EMPTY_FILTERS: MCPLibraryFilters = {
	categories: [],
	connection_types: [],
	auth_types: [],
	tags: [],
};

interface SidebarProps {
	filters: MCPLibraryFilters;
	onFiltersChange: (filters: MCPLibraryFilters) => void;
}

// ---------------------------------------------------------------------------
// MCPLibraryFilterSidebar – orchestrator
// ---------------------------------------------------------------------------

export function MCPLibraryFilterSidebar({ filters, onFiltersChange }: SidebarProps) {
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
		return filters.categories.length + filters.connection_types.length + filters.auth_types.length + filters.tags.length;
	}, [filters]);

	const handleReset = useCallback(() => {
		onFiltersChange(EMPTY_FILTERS);
	}, [onFiltersChange]);

	const { data: filterData, isLoading, isError, refetch } = useGetMCPLibraryFilterDataQuery();

	if (collapsed) {
		return (
			<button
				type="button"
				onClick={toggleCollapsed}
				className="bg-card group flex h-full w-10 shrink-0 cursor-pointer flex-col items-center gap-3 rounded-r-md py-4 text-sm font-medium"
				title="Show filters"
				aria-label="Show filters"
				data-testid="mcpLibraryFilterSidebar-toggle-show"
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
							data-testid="mcpLibraryFilterSidebar-reset-button"
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
						data-testid="mcpLibraryFilterSidebar-toggle-hide"
					>
						<PanelLeftClose className="size-4" />
					</Button>
				</div>
			</div>

			<ScrollArea className="flex flex-1 overflow-y-auto p-2 pb-0" viewportClassName="no-table">
				{isError ? (
					<div className="flex flex-col items-center gap-3 px-3 py-8 text-center" data-testid="mcpLibraryFilterSidebar-error">
						<p className="text-muted-foreground text-sm">Failed to load filters.</p>
						<Button variant="outline" size="sm" onClick={() => refetch()} data-testid="mcpLibraryFilterSidebar-retry-button">
							<RotateCcw className="size-3" />
							Retry
						</Button>
					</div>
				) : (
					<div className="flex grow flex-col gap-1">
						<CheckboxFilterSection
							title="Category"
							items={filterData?.categories || []}
							selected={filters.categories}
							loading={isLoading}
							defaultOpen
							onChange={(categories) => onFiltersChange({ ...filters, categories })}
							testIdPrefix="mcp-library-filter-category"
						/>
						<CheckboxFilterSection
							title="Connection Type"
							items={filterData?.connection_types || []}
							selected={filters.connection_types}
							loading={isLoading}
							onChange={(connection_types) => onFiltersChange({ ...filters, connection_types })}
							testIdPrefix="mcp-library-filter-connection-type"
						/>
						<CheckboxFilterSection
							title="Auth Type"
							items={filterData?.auth_types || []}
							selected={filters.auth_types}
							loading={isLoading}
							onChange={(auth_types) => onFiltersChange({ ...filters, auth_types })}
							testIdPrefix="mcp-library-filter-auth-type"
						/>
						<CheckboxFilterSection
							title="Tags"
							items={filterData?.tags || []}
							selected={filters.tags}
							loading={isLoading}
							onChange={(tags) => onFiltersChange({ ...filters, tags })}
							testIdPrefix="mcp-library-filter-tag"
						/>
					</div>
				)}
			</ScrollArea>
		</div>
	);
}

// ---------------------------------------------------------------------------
// Shared primitives
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
	testId,
}: {
	title: string;
	children: React.ReactNode;
	defaultOpen?: boolean;
	loading?: boolean;
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
				<div className="divide-border divide-y overflow-hidden rounded-sm border">{loading ? <FilterSectionSkeleton /> : children}</div>
			</CollapsibleContent>
		</Collapsible>
	);
}

function CheckboxFilterItem({
	label,
	checked,
	onCheckedChange,
	testId,
}: {
	label: string;
	checked: boolean;
	onCheckedChange: (checked: boolean) => void;
	testId?: string;
}) {
	return (
		<label className="hover:bg-muted/50 flex cursor-pointer items-center gap-2.5 px-3 py-2 text-sm" data-testid={testId}>
			<Checkbox checked={checked} onCheckedChange={onCheckedChange} />
			<span className="truncate">{label}</span>
		</label>
	);
}

// ---------------------------------------------------------------------------
// CheckboxFilterSection – a collapsible, searchable section of checkboxes
// ---------------------------------------------------------------------------

function CheckboxFilterSection({
	title,
	items,
	selected,
	loading,
	defaultOpen = false,
	onChange,
	testIdPrefix,
}: {
	title: string;
	items: string[];
	selected: string[];
	loading?: boolean;
	defaultOpen?: boolean;
	onChange: (selected: string[]) => void;
	testIdPrefix?: string;
}) {
	const [query, setQuery] = useState("");
	const normalized = query.trim().toLowerCase();
	const filtered = normalized ? items.filter((item) => item.toLowerCase().includes(normalized)) : items;

	const hasActive = selected.length > 0;
	const showSearch = items.length > 5;

	const toggle = (value: string) => {
		if (selected.includes(value)) {
			onChange(selected.filter((v) => v !== value));
		} else {
			onChange([...selected, value]);
		}
	};

	return (
		<FilterSection
			title={title}
			defaultOpen={defaultOpen || hasActive}
			loading={loading}
			testId={testIdPrefix ? `${testIdPrefix}-toggle` : undefined}
		>
			{showSearch && (
				<div className="relative border-b">
					<Search className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2" />
					<Input
						value={query}
						onChange={(e) => setQuery(e.target.value)}
						placeholder="Search..."
						className="h-8 border-0 pl-8 text-xs"
						data-testid={testIdPrefix ? `${testIdPrefix}-search` : undefined}
					/>
				</div>
			)}
			{filtered.map((item) => (
				<CheckboxFilterItem
					key={item}
					label={item}
					checked={selected.includes(item)}
					onCheckedChange={() => toggle(item)}
					testId={testIdPrefix ? `${testIdPrefix}-checkbox-${item}` : undefined}
				/>
			))}
			{filtered.length === 0 && <div className="text-muted-foreground flex h-9 items-center px-3 text-xs">No results</div>}
		</FilterSection>
	);
}