// Side filter panel for the MCP sessions table, matching the pattern used by
// the MCP clients and MCP library pages: a collapsible sidebar of checkbox
// sections, collapsing to a narrow rail with an active-filter-count badge.
// State/behavior mirrors mcpClientsFilterSidebar.tsx; kept as its own copy
// since the two pages filter on unrelated fields.

import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Input } from "@/components/ui/input";
import { ScrollArea } from "@/components/ui/scrollArea";
import { getUserSearchQuery } from "@/lib/registries/userPicker";
import { useGetMCPClientsQuery, useGetVirtualKeysQuery } from "@/lib/store";
import { cn } from "@/lib/utils";
import { ChevronDown, Fingerprint, KeyRound, LoaderCircle, PanelLeftClose, PanelLeftOpen, RotateCcw, Search, UserRound } from "lucide-react";
import { type Ref, useCallback, useEffect, useMemo, useRef, useState } from "react";
// Side-effect import: registers the enterprise user search hook (if this is
// an enterprise build) before this module's first render. OSS has no user
// directory, so nothing registers and getUserSearchQuery() stays undefined —
// the Users filter section renders nothing. See ui/lib/registries/userPicker.tsx.
import "@enterprise/lib/registrations/userPicker";

const COLLAPSE_STORAGE_KEY = "mcp-sessions-filter-sidebar-collapsed";
const MCP_CLIENT_PAGE_SIZE = 25;
const VK_PAGE_SIZE = 25;
const USER_PAGE_SIZE = 25;

// ---------------------------------------------------------------------------
// Filter types
// ---------------------------------------------------------------------------

export interface MCPSessionFilters {
	kind: string[]; // subset of ["token", "header"]
	status: string[]; // subset of ["active", "orphaned", "needs_reauth", "needs_update", "pending"]
	auth_mode: string[]; // subset of ["user", "vk", "session"]
	mcp_client_id: string[]; // explicit MCP client IDs
	virtual_key_id: string[]; // explicit VK IDs (vk-mode rows only)
	user_id: string[]; // explicit user IDs (user-mode rows only; enterprise only, see UsersFilterSection)
}

export const EMPTY_FILTERS: MCPSessionFilters = {
	kind: [],
	status: [],
	auth_mode: [],
	mcp_client_id: [],
	virtual_key_id: [],
	user_id: [],
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
		return (
			filters.kind.length +
			filters.status.length +
			filters.auth_mode.length +
			filters.mcp_client_id.length +
			filters.virtual_key_id.length +
			filters.user_id.length
		);
	}, [filters]);

	const handleReset = useCallback(() => {
		onFiltersChange(EMPTY_FILTERS);
	}, [onFiltersChange]);

	// Mirrors MCPClientFilterSection's auth-type scoping, but for section
	// visibility rather than query narrowing: when the Identity facet pins the
	// row set to exactly one mode, the other mode's picker can't match
	// anything, so hide it outright rather than leave a checkbox list that
	// would only ever come back empty. Selecting "Session" hides both, since
	// session-bound rows have no VK or user to pick. Neither/both selected
	// leaves both pickers visible.
	const authModeOnly = filters.auth_mode.length === 1 ? filters.auth_mode[0] : null;
	const showVirtualKeyFilter = authModeOnly !== "user" && authModeOnly !== "session";
	const showUsersFilter = authModeOnly !== "vk" && authModeOnly !== "session";

	// A hidden picker's stale selection would otherwise keep silently
	// filtering (a VK pick surviving a switch to "User" identity, say) with
	// no visible control left to clear it — so drop it the moment its section
	// disappears.
	useEffect(() => {
		if (!showVirtualKeyFilter && filters.virtual_key_id.length > 0) {
			onFiltersChange({ ...filters, virtual_key_id: [] });
		}
	}, [showVirtualKeyFilter, filters, onFiltersChange]);

	useEffect(() => {
		if (!showUsersFilter && filters.user_id.length > 0) {
			onFiltersChange({ ...filters, user_id: [] });
		}
	}, [showUsersFilter, filters, onFiltersChange]);

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
					<MCPClientFilterSection filters={filters} onFiltersChange={onFiltersChange} />
					{showVirtualKeyFilter && <VirtualKeyFilterSection filters={filters} onFiltersChange={onFiltersChange} />}
					{showUsersFilter && <UsersFilterSection filters={filters} onFiltersChange={onFiltersChange} />}
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

// SearchableCheckboxList – checkbox rows with a search input. Client-side label
// filtering is applied on top of the (debounced) onSearch callback so the caller
// can fetch server-side results. Mirrors the MCP clients filter sidebar pattern.
function SearchableCheckboxList({
	items,
	isSelected,
	onToggle,
	placeholder = "Search...",
	inputRef,
	testIdPrefix,
	onSearch,
	fetching,
}: {
	items: { key: string; label: string }[];
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

// MCPClientFilterSection – restricts sessions to a set of MCP clients, resolved
// via server-side search (mirrors VKAccessFilterSection in the MCP clients
// sidebar). Scoped to per_user_oauth/per_user_headers clients only — those are
// the only auth types that ever produce a session row here. Further narrowed
// by the Type filter when it picks a single kind: OAuth (token) → only
// per_user_oauth clients, Headers (header) → only per_user_headers clients.
// Neither/both selected falls back to the full per-user set. The client list
// only fetches once the section is opened or already has a selection, since
// most visits won't touch this filter.
function MCPClientFilterSection({ filters, onFiltersChange }: SidebarProps) {
	const hasActive = filters.mcp_client_id.length > 0;
	const [opened, setOpened] = useState(hasActive);
	const [searchQuery, setSearchQuery] = useState("");
	const searchInputRef = useAutoFocusOnOpen(opened);

	const authType = useMemo(() => {
		const onlyOAuth = filters.kind.includes("token") && !filters.kind.includes("header");
		const onlyHeaders = filters.kind.includes("header") && !filters.kind.includes("token");
		if (onlyOAuth) return "per_user_oauth";
		if (onlyHeaders) return "per_user_headers";
		return "per_user_oauth,per_user_headers";
	}, [filters.kind]);

	const { data, isFetching } = useGetMCPClientsQuery(
		{ limit: MCP_CLIENT_PAGE_SIZE, offset: 0, search: searchQuery || undefined, auth_type: authType },
		{ skip: !opened && !hasActive },
	);
	const mcpClients = data?.clients || [];

	const toggle = (clientId: string) => {
		const current = filters.mcp_client_id;
		const next = current.includes(clientId) ? current.filter((v) => v !== clientId) : [...current, clientId];
		onFiltersChange({ ...filters, mcp_client_id: next });
	};

	return (
		<FilterSection title="MCP Server" defaultOpen={hasActive} onOpenChange={setOpened} testId="mcp-sessions-filter-mcp-client-toggle">
			<SearchableCheckboxList
				inputRef={searchInputRef}
				placeholder="Search MCP servers"
				items={mcpClients.map((client) => ({ key: client.config.client_id, label: client.config.name || client.config.client_id }))}
				isSelected={(key) => filters.mcp_client_id.includes(key)}
				onToggle={toggle}
				onSearch={setSearchQuery}
				fetching={isFetching}
				testIdPrefix="mcp-sessions-filter-mcp-client"
			/>
		</FilterSection>
	);
}

// VirtualKeyFilterSection – restricts sessions to a set of VKs, resolved via
// the same server-side search+limit VK picker the MCP clients sidebar uses
// (governance/virtual-keys), rather than loading the full VK list — VK counts
// can be huge, so this only ever fetches a page at a time, scoped by the
// in-flight search text. The client list only fetches once the section is
// opened or already has a selection, since most visits won't touch this
// filter.
function VirtualKeyFilterSection({ filters, onFiltersChange }: SidebarProps) {
	const hasActive = filters.virtual_key_id.length > 0;
	const [opened, setOpened] = useState(hasActive);
	const [searchQuery, setSearchQuery] = useState("");
	const searchInputRef = useAutoFocusOnOpen(opened);

	const { data, isFetching } = useGetVirtualKeysQuery(
		{ limit: VK_PAGE_SIZE, offset: 0, search: searchQuery || undefined },
		{ skip: !opened && !hasActive },
	);
	const virtualKeys = data?.virtual_keys || [];

	const toggle = (vkId: string) => {
		const current = filters.virtual_key_id;
		const next = current.includes(vkId) ? current.filter((v) => v !== vkId) : [...current, vkId];
		onFiltersChange({ ...filters, virtual_key_id: next });
	};

	return (
		<FilterSection title="Virtual Key" defaultOpen={hasActive} onOpenChange={setOpened} testId="mcp-sessions-filter-vk-toggle">
			<SearchableCheckboxList
				inputRef={searchInputRef}
				placeholder="Search virtual keys"
				items={virtualKeys.map((vk) => ({ key: vk.id, label: vk.name || vk.id }))}
				isSelected={(key) => filters.virtual_key_id.includes(key)}
				onToggle={toggle}
				onSearch={setSearchQuery}
				fetching={isFetching}
				testIdPrefix="mcp-sessions-filter-vk"
			/>
		</FilterSection>
	);
}

// UsersFilterSection – restricts sessions to a set of users, mirroring
// VirtualKeyFilterSection's inline search+checkbox UI exactly (same
// server-side search+limit shape, same lazy-fetch-on-open behavior for huge
// user counts). Renders nothing in OSS: there's no user directory to search
// against, so unlike the VK/MCP-client sections this can't fall back to
// anything — an unresolvable search box would be worse than no filter at
// all. Enterprise builds register a search hook via getUserSearchQuery()
// (see ui/lib/registries/userPicker.tsx), wrapping the same users API the
// single-select UserPicker (routing rules' "User" scope, the VK table's
// user filter) already uses.
function UsersFilterSection({ filters, onFiltersChange }: SidebarProps) {
	const useUserSearchQuery = getUserSearchQuery();
	const hasActive = filters.user_id.length > 0;
	const [opened, setOpened] = useState(hasActive);
	const [searchQuery, setSearchQuery] = useState("");
	const searchInputRef = useAutoFocusOnOpen(opened);

	// Hooks must run unconditionally, so the registry lookup above can't gate
	// this call — pass a no-op fallback and gate the render below instead.
	const { data, isFetching } = (useUserSearchQuery ?? useNoopUserSearchQuery)(
		{ limit: USER_PAGE_SIZE, search: searchQuery || undefined },
		{ skip: !opened && !hasActive },
	);
	if (!useUserSearchQuery) return null;
	const users = data?.users || [];

	const toggle = (userId: string) => {
		const current = filters.user_id;
		const next = current.includes(userId) ? current.filter((v) => v !== userId) : [...current, userId];
		onFiltersChange({ ...filters, user_id: next });
	};

	return (
		<FilterSection title="Users" defaultOpen={hasActive} onOpenChange={setOpened} testId="mcp-sessions-filter-users-toggle">
			<SearchableCheckboxList
				inputRef={searchInputRef}
				placeholder="Search by name or email"
				items={users.map((user) => ({ key: user.id, label: user.label }))}
				isSelected={(key) => filters.user_id.includes(key)}
				onToggle={toggle}
				onSearch={setSearchQuery}
				fetching={isFetching}
				testIdPrefix="mcp-sessions-filter-user"
			/>
		</FilterSection>
	);
}

// Stand-in for useUserSearchQuery when no OSS hook is registered — keeps the
// hooks-must-run-unconditionally rule satisfied above without a real fetch.
function useNoopUserSearchQuery() {
	return { data: undefined, isFetching: false };
}
