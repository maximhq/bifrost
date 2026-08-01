import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Input } from "@/components/ui/input";
import { ScrollArea } from "@/components/ui/scrollArea";
import { useGetVirtualKeysQuery } from "@/lib/store";
import { cn } from "@/lib/utils";
import { ChevronDown, LoaderCircle, PanelLeftClose, PanelLeftOpen, RotateCcw, Search } from "lucide-react";
import { type Ref, useCallback, useEffect, useMemo, useRef, useState } from "react";

const COLLAPSE_STORAGE_KEY = "mcp-clients-filter-sidebar-collapsed";
const VK_PAGE_SIZE = 25;

// ---------------------------------------------------------------------------
// Filter types
// ---------------------------------------------------------------------------

// Booleans (code_mode, status) are held as string arrays of the underlying
// column values so every checkbox section shares one model. A single selection
// becomes a concrete boolean filter; zero or both selections mean "no filter"
// (translated to an undefined query param by the page).
export interface MCPClientFilters {
	connection_types: string[];
	auth_types: string[];
	states: string[]; // subset of ["connected", "disconnected"]
	code_mode: string[]; // subset of ["true", "false"] → is_code_mode_client
	status: string[]; // subset of ["false", "true"] → disabled column value
	only_all_vks: boolean; // VK access toggle → allow_on_all_virtual_keys
	virtual_keys: string[]; // explicit VK IDs the client must be assigned to
}

export const EMPTY_FILTERS: MCPClientFilters = {
	connection_types: [],
	auth_types: [],
	states: [],
	code_mode: [],
	status: [],
	only_all_vks: false,
	virtual_keys: [],
};

interface FilterOption {
	value: string;
	label: string;
}

// Facet values are fixed enums (schemas.MCPConnectionType / MCPAuthType) so we
// hardcode them here rather than fetching a distinct-values endpoint.
const CONNECTION_TYPE_OPTIONS: FilterOption[] = [
	{ value: "http", label: "HTTP" },
	{ value: "sse", label: "SSE" },
	{ value: "stdio", label: "STDIO" },
];

const AUTH_TYPE_OPTIONS: FilterOption[] = [
	{ value: "none", label: "None" },
	{ value: "headers", label: "Headers" },
	{ value: "oauth", label: "OAuth" },
	{ value: "per_user_oauth", label: "Per-User OAuth" },
	{ value: "per_user_headers", label: "Per-User Headers" },
];

// Connection state is runtime, not a column — the backend resolves these
// against live engine state. "disconnected" covers everything not connected.
const STATE_OPTIONS: FilterOption[] = [
	{ value: "connected", label: "Connected" },
	{ value: "disconnected", label: "Disconnected" },
];

const CODE_MODE_OPTIONS: FilterOption[] = [
	{ value: "true", label: "Enabled" },
	{ value: "false", label: "Disabled" },
];

// Status maps to the `disabled` column: an enabled client has disabled=false.
const STATUS_OPTIONS: FilterOption[] = [
	{ value: "false", label: "Enabled" },
	{ value: "true", label: "Disabled" },
];

interface SidebarProps {
	filters: MCPClientFilters;
	onFiltersChange: (filters: MCPClientFilters) => void;
}

// ---------------------------------------------------------------------------
// MCPClientsFilterSidebar – orchestrator
// ---------------------------------------------------------------------------

export function MCPClientsFilterSidebar({ filters, onFiltersChange }: SidebarProps) {
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
		return (
			filters.connection_types.length +
			filters.auth_types.length +
			filters.states.length +
			(filters.code_mode.length === 1 ? 1 : 0) +
			(filters.status.length === 1 ? 1 : 0) +
			filters.virtual_keys.length +
			(filters.only_all_vks ? 1 : 0)
		);
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
				data-testid="mcpClientsFilterSidebar-toggle-show"
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
							data-testid="mcpClientsFilterSidebar-reset-button"
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
						data-testid="mcpClientsFilterSidebar-toggle-hide"
					>
						<PanelLeftClose className="size-4" />
					</Button>
				</div>
			</div>

			<ScrollArea className="flex flex-1 overflow-y-auto p-2 pb-0" viewportClassName="no-table">
				<div className="flex grow flex-col gap-1">
					<CheckboxFilterSection
						title="Connection Type"
						options={CONNECTION_TYPE_OPTIONS}
						selected={filters.connection_types}
						defaultOpen
						onChange={(connection_types) => onFiltersChange({ ...filters, connection_types })}
						testIdPrefix="mcp-clients-filter-connection-type"
					/>
					<CheckboxFilterSection
						title="Auth Type"
						options={AUTH_TYPE_OPTIONS}
						selected={filters.auth_types}
						onChange={(auth_types) => onFiltersChange({ ...filters, auth_types })}
						testIdPrefix="mcp-clients-filter-auth-type"
					/>
					<CheckboxFilterSection
						title="State"
						options={STATE_OPTIONS}
						selected={filters.states}
						onChange={(states) => onFiltersChange({ ...filters, states })}
						testIdPrefix="mcp-clients-filter-state"
					/>
					<CheckboxFilterSection
						title="Code Mode"
						options={CODE_MODE_OPTIONS}
						selected={filters.code_mode}
						onChange={(code_mode) => onFiltersChange({ ...filters, code_mode })}
						testIdPrefix="mcp-clients-filter-code-mode"
					/>
					<CheckboxFilterSection
						title="Status"
						options={STATUS_OPTIONS}
						selected={filters.status}
						onChange={(status) => onFiltersChange({ ...filters, status })}
						testIdPrefix="mcp-clients-filter-status"
					/>
					<VKAccessFilterSection filters={filters} onFiltersChange={onFiltersChange} />
				</div>
			</ScrollArea>
		</div>
	);
}

// ---------------------------------------------------------------------------
// Shared primitives
// ---------------------------------------------------------------------------

function useAutoFocusOnOpen(isOpen: boolean) {
	const ref = useRef<HTMLInputElement>(null);
	// Skip the initial mount so focus isn't stolen when the section starts open
	// from URL state; only focus on an explicit open action by the user.
	const mounted = useRef(false);
	useEffect(() => {
		if (!mounted.current) {
			mounted.current = true;
			return;
		}
		if (isOpen) ref.current?.focus({ preventScroll: true });
	}, [isOpen]);
	return ref;
}

function FilterSection({
	title,
	children,
	defaultOpen = false,
	onOpenChange,
	testId,
}: {
	title: string;
	children: React.ReactNode;
	defaultOpen?: boolean;
	onOpenChange?: (open: boolean) => void;
	testId?: string;
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
					checked={selected.includes(option.value)}
					onCheckedChange={() => toggle(option.value)}
					testId={testIdPrefix ? `${testIdPrefix}-checkbox-${option.value}` : undefined}
				/>
			))}
		</FilterSection>
	);
}

// SearchableCheckboxList – checkbox rows with a search input. Client-side label
// filtering is applied on top of the (debounced) onSearch callback so the caller
// can fetch server-side results. Mirrors the logs filter sidebar pattern.
function SearchableCheckboxList({
	items,
	pinnedItems = [],
	isSelected,
	onToggle,
	placeholder = "Search...",
	inputRef,
	testIdPrefix,
	onSearch,
	fetching,
}: {
	items: { key: string; label: string }[];
	// Always-visible rows rendered before the searchable list and immune to the
	// text filter (e.g. a static "All" option). Share isSelected/onToggle.
	pinnedItems?: { key: string; label: string }[];
	isSelected: (key: string) => boolean;
	onToggle: (key: string) => void;
	placeholder?: string;
	inputRef?: Ref<HTMLInputElement>;
	testIdPrefix?: string;
	onSearch?: (query: string) => void;
	fetching?: boolean;
}) {
	const [query, setQuery] = useState("");
	const normalized = query.trim().toLowerCase();
	const filtered = normalized ? items.filter((item) => item.label.toLowerCase().includes(normalized)) : items;

	useEffect(() => {
		if (!onSearch) return;
		const timer = setTimeout(() => onSearch(query.trim()), 300);
		return () => clearTimeout(timer);
	}, [query, onSearch]);

	return (
		<>
			{pinnedItems.map((item) => (
				<CheckboxFilterItem
					key={item.key}
					label={item.label}
					checked={isSelected(item.key)}
					onCheckedChange={() => onToggle(item.key)}
					testId={testIdPrefix ? `${testIdPrefix}-checkbox-${item.key}` : undefined}
				/>
			))}
			<div className="relative border-b">
				{fetching ? (
					<LoaderCircle className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 animate-spin" />
				) : (
					<Search className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2" />
				)}
				<Input
					ref={inputRef}
					value={query}
					onChange={(e) => setQuery(e.target.value)}
					placeholder={placeholder}
					className="h-8 border-0 pl-8 text-xs"
					data-testid={testIdPrefix ? `${testIdPrefix}-search` : undefined}
				/>
			</div>
			{filtered.map((item) => (
				<CheckboxFilterItem
					key={item.key}
					label={item.label}
					checked={isSelected(item.key)}
					onCheckedChange={() => onToggle(item.key)}
					testId={testIdPrefix ? `${testIdPrefix}-checkbox-${item.key}` : undefined}
				/>
			))}
			{filtered.length === 0 && <div className="text-muted-foreground flex h-9 items-center px-3 text-xs">No results</div>}
		</>
	);
}

// ---------------------------------------------------------------------------
// VKAccessFilterSection – a single checkbox list whose pinned first option is
// "All virtual keys" (allow_on_all_virtual_keys); the rest are individual VKs
// resolved via server-side search. They OR together server-side: a client
// matches if it is open to all VKs OR assigned to one of the selected VKs.
// ---------------------------------------------------------------------------

// Reserved key for the pinned "All virtual keys" row — namespaced so it can
// never collide with a real virtual key id.
const ALL_VKS_KEY = "__all_virtual_keys__";

function VKAccessFilterSection({ filters, onFiltersChange }: SidebarProps) {
	const hasActive = filters.only_all_vks || filters.virtual_keys.length > 0;
	const [opened, setOpened] = useState(hasActive);
	const [searchQuery, setSearchQuery] = useState("");
	const searchInputRef = useAutoFocusOnOpen(opened);

	const { data, isFetching } = useGetVirtualKeysQuery(
		{ limit: VK_PAGE_SIZE, offset: 0, search: searchQuery || undefined },
		{ skip: !opened && !hasActive },
	);
	const virtualKeys = data?.virtual_keys || [];

	const isSelected = (key: string) => (key === ALL_VKS_KEY ? filters.only_all_vks : filters.virtual_keys.includes(key));

	const toggle = (key: string) => {
		if (key === ALL_VKS_KEY) {
			onFiltersChange({ ...filters, only_all_vks: !filters.only_all_vks });
			return;
		}
		const current = filters.virtual_keys;
		const next = current.includes(key) ? current.filter((v) => v !== key) : [...current, key];
		onFiltersChange({ ...filters, virtual_keys: next });
	};

	return (
		<FilterSection title="VK Access" defaultOpen={hasActive} onOpenChange={setOpened} testId="mcp-clients-filter-vk-access-toggle">
			<SearchableCheckboxList
				inputRef={searchInputRef}
				placeholder="Search virtual keys"
				pinnedItems={[{ key: ALL_VKS_KEY, label: "All virtual keys" }]}
				items={virtualKeys.map((vk) => ({ key: vk.id, label: vk.name }))}
				isSelected={isSelected}
				onToggle={toggle}
				onSearch={setSearchQuery}
				fetching={isFetching}
				testIdPrefix="mcp-clients-filter-vk"
			/>
		</FilterSection>
	);
}
