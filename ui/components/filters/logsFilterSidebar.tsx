import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Input } from "@/components/ui/input";
import { ScrollArea } from "@/components/ui/scrollArea";
import { Skeleton } from "@/components/ui/skeleton";
import { RequestTypeLabels, RequestTypes, RoutingEngineUsedLabels, Statuses } from "@/lib/constants/logs";
import { useGetAvailableFilterDataQuery, useGetProvidersQuery } from "@/lib/store";
import type { LogFilters } from "@/lib/types/logs";
import { cn } from "@/lib/utils";
import { ChevronDown, PanelLeftClose, PanelLeftOpen, RotateCcw } from "lucide-react";
import { Ref, useCallback, useEffect, useMemo, useRef, useState } from "react";

const COLLAPSE_STORAGE_KEY = "logs-filter-sidebar-collapsed";

// ---------------------------------------------------------------------------
// LogsSidebar – orchestrator
// ---------------------------------------------------------------------------

interface LogsSidebarProps {
	filters: LogFilters;
	onFiltersChange: (filters: LogFilters) => void;
}

export function LogsFilterSidebar({ filters, onFiltersChange }: LogsSidebarProps) {
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
		const excludedKeys = ["start_time", "end_time", "content_search", "metadata_filters", "period", "polling"];
		let count = Object.entries(filters).reduce((c, [key, value]) => {
			if (excludedKeys.includes(key)) return c;
			if (Array.isArray(value)) return c + value.length;
			return c + (value ? 1 : 0);
		}, 0);
		if (filters.metadata_filters) {
			count += Object.keys(filters.metadata_filters).length;
		}
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
				className="bg-card group flex h-full w-10 shrink-0 cursor-pointer flex-col items-center gap-3 rounded-r-md py-4 text-sm font-medium"
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
					<ModelsFilter filters={filters} onFiltersChange={onFiltersChange} defaultOpen />
					{/* Rest closed unless they have active filters */}
					<SelectedKeysFilter filters={filters} onFiltersChange={onFiltersChange} />
					<VirtualKeysFilter filters={filters} onFiltersChange={onFiltersChange} />
					<ProvidersFilter filters={filters} onFiltersChange={onFiltersChange} />
					<TypeFilter filters={filters} onFiltersChange={onFiltersChange} />
					<AliasesFilter filters={filters} onFiltersChange={onFiltersChange} />
					<RoutingEnginesFilter filters={filters} onFiltersChange={onFiltersChange} />
					<RoutingRulesFilter filters={filters} onFiltersChange={onFiltersChange} />
					<UserFilter filters={filters} onFiltersChange={onFiltersChange} />
					<SessionFilter filters={filters} onFiltersChange={onFiltersChange} />
					<CostFilter filters={filters} onFiltersChange={onFiltersChange} />
					<StopReasonFilter filters={filters} onFiltersChange={onFiltersChange} />
					<MetadataFilters filters={filters} onFiltersChange={onFiltersChange} />
				</div>
			</ScrollArea>
		</div>
	);
}

// ---------------------------------------------------------------------------
// Shared helpers & primitives
// ---------------------------------------------------------------------------

function groupByName(items: { name: string; id: string }[]) {
	const map = new Map<string, string[]>();
	for (const item of items) {
		const ids = map.get(item.name) || [];
		ids.push(item.id);
		map.set(item.name, ids);
	}
	return map;
}

function dedup(items: { name: string }[]) {
	return [...new Map(items.map((i) => [i.name, i])).values()].map((i) => i.name);
}

/** Shared props every individual filter component receives. */
interface FilterComponentProps {
	filters: LogFilters;
	onFiltersChange: (filters: LogFilters) => void;
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
	testId,
}: {
	title: string;
	children: React.ReactNode;
	defaultOpen?: boolean;
	loading?: boolean;
	onOpenChange?: (open: boolean) => void;
	testId?: string;
}) {
	const [open, setOpen] = useState(defaultOpen);

	// Force open when defaultOpen flips to true (e.g. a filter in this section becomes active)
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
				<div className="divide-border divide-y overflow-hidden rounded-sm border">{loading ? <FilterSectionSkeleton /> : children}</div>
			</CollapsibleContent>
		</Collapsible>
	);
}

// ---------------------------------------------------------------------------
// CheckboxFilterItem – single checkbox row
// ---------------------------------------------------------------------------

function CheckboxFilterItem({
	label,
	checked,
	onCheckedChange,
	labelClassName,
	testId,
}: {
	label: string;
	checked: boolean;
	onCheckedChange: (checked: boolean) => void;
	labelClassName?: string;
	testId?: string;
}) {
	return (
		<label className="hover:bg-muted/50 flex cursor-pointer items-center gap-2.5 px-3 py-2 text-sm" data-testid={testId}>
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
	testIdPrefix,
}: {
	items: { key: string; label: string }[];
	isSelected: (key: string) => boolean;
	onToggle: (key: string) => void;
	placeholder?: string;
	inputRef?: Ref<HTMLInputElement>;
	testIdPrefix?: string;
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
// StatusFilter
// ---------------------------------------------------------------------------

function StatusFilter({ filters, onFiltersChange, defaultOpen }: FilterComponentProps) {
	const hasActive = (filters.status || []).length > 0;
	return (
		<FilterSection title="Status" defaultOpen={defaultOpen || hasActive} testId="status-filter-toggle">
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
					testId={`status-filter-checkbox-${status}`}
				/>
			))}
		</FilterSection>
	);
}

// ---------------------------------------------------------------------------
// StopReasonFilter – fetches available stop reasons internally
// ---------------------------------------------------------------------------

function StopReasonFilter({ filters, onFiltersChange, defaultOpen }: FilterComponentProps) {
	const hasActive = (filters.stop_reasons || []).length > 0;
	const [opened, setOpened] = useState(defaultOpen || hasActive);
	const searchInputRef = useAutoFocusOnOpen(opened);
	const { data: filterData, isUninitialized, isLoading } = useGetAvailableFilterDataQuery(undefined, { skip: !opened && !hasActive });
	const availableStopReasons = filterData?.stop_reasons || [];

	if (!isUninitialized && !isLoading && availableStopReasons.length === 0 && !hasActive) return null;

	return (
		<FilterSection
			title="Stop Reason"
			defaultOpen={defaultOpen || hasActive}
			loading={isLoading}
			onOpenChange={setOpened}
			testId="stop-reason-filter-toggle"
		>
			<SearchableCheckboxList
				inputRef={searchInputRef}
				placeholder="Search stop reasons"
				items={availableStopReasons.map((reason) => ({ key: reason, label: reason }))}
				isSelected={(reason) => (filters.stop_reasons || []).includes(reason)}
				onToggle={(reason) => {
					const current = filters.stop_reasons || [];
					const next = current.includes(reason) ? current.filter((r) => r !== reason) : [...current, reason];
					onFiltersChange({ ...filters, stop_reasons: next });
				}}
				testIdPrefix="stop-reason-filter"
			/>
		</FilterSection>
	);
}

// ---------------------------------------------------------------------------
// ProvidersFilter – fetches providers internally
// ---------------------------------------------------------------------------

function ProvidersFilter({ filters, onFiltersChange, defaultOpen }: FilterComponentProps) {
	const hasActive = (filters.providers || []).length > 0;
	const [opened, setOpened] = useState(defaultOpen || hasActive);
	const searchInputRef = useAutoFocusOnOpen(opened);
	const { data: providersData, isUninitialized, isLoading } = useGetProvidersQuery(undefined, { skip: !opened && !hasActive });
	const availableProviders = providersData || [];

	// Hide only if data was fetched (not loading) and came back empty
	if (!isUninitialized && !isLoading && availableProviders.length === 0 && !hasActive) return null;

	return (
		<FilterSection
			title="Providers"
			defaultOpen={defaultOpen || hasActive}
			loading={isLoading}
			onOpenChange={setOpened}
			testId="providers-filter-toggle"
		>
			<SearchableCheckboxList
				inputRef={searchInputRef}
				placeholder="Search providers"
				items={availableProviders.map((p) => ({ key: p.name, label: p.name }))}
				isSelected={(name) => (filters.providers || []).includes(name)}
				onToggle={(name) => {
					const current = filters.providers || [];
					const next = current.includes(name) ? current.filter((p) => p !== name) : [...current, name];
					onFiltersChange({ ...filters, providers: next });
				}}
				testIdPrefix="providers-filter"
			/>
		</FilterSection>
	);
}

// ---------------------------------------------------------------------------
// TypeFilter
// ---------------------------------------------------------------------------

function TypeFilter({ filters, onFiltersChange, defaultOpen }: FilterComponentProps) {
	const hasActive = (filters.objects || []).length > 0;
	return (
		<FilterSection title="Type" defaultOpen={defaultOpen || hasActive} testId="type-filter-toggle">
			{RequestTypes.map((type) => {
				const label = RequestTypeLabels[type as keyof typeof RequestTypeLabels] ?? type;
				return (
					<CheckboxFilterItem
						key={type}
						label={label}
						checked={(filters.objects || []).includes(type)}
						onCheckedChange={() => {
							const current = filters.objects || [];
							const next = current.includes(type) ? current.filter((t) => t !== type) : [...current, type];
							onFiltersChange({ ...filters, objects: next });
						}}
						testId={`type-filter-checkbox-${type}`}
					/>
				);
			})}
		</FilterSection>
	);
}

// ---------------------------------------------------------------------------
// ModelsFilter – fetches available models internally
// ---------------------------------------------------------------------------

function ModelsFilter({ filters, onFiltersChange, defaultOpen }: FilterComponentProps) {
	const hasActive = (filters.models || []).length > 0;
	const [opened, setOpened] = useState(defaultOpen || hasActive);
	const searchInputRef = useAutoFocusOnOpen(opened);
	const { data: filterData, isUninitialized, isLoading } = useGetAvailableFilterDataQuery(undefined, { skip: !opened && !hasActive });
	const availableModels = filterData?.models || [];

	if (!isUninitialized && !isLoading && availableModels.length === 0 && !hasActive) return null;

	return (
		<FilterSection
			title="Models"
			defaultOpen={defaultOpen || hasActive}
			loading={isLoading}
			onOpenChange={setOpened}
			testId="models-filter-toggle"
		>
			<SearchableCheckboxList
				inputRef={searchInputRef}
				placeholder="Search models"
				items={availableModels.map((m) => ({ key: m, label: m }))}
				isSelected={(model) => (filters.models || []).includes(model)}
				onToggle={(model) => {
					const current = filters.models || [];
					const next = current.includes(model) ? current.filter((m) => m !== model) : [...current, model];
					onFiltersChange({ ...filters, models: next });
				}}
				testIdPrefix="models-filter"
			/>
		</FilterSection>
	);
}

// ---------------------------------------------------------------------------
// AliasesFilter – fetches available aliases internally
// ---------------------------------------------------------------------------

function AliasesFilter({ filters, onFiltersChange, defaultOpen }: FilterComponentProps) {
	const hasActive = (filters.aliases || []).length > 0;
	const [opened, setOpened] = useState(defaultOpen || hasActive);
	const searchInputRef = useAutoFocusOnOpen(opened);
	const { data: filterData, isUninitialized, isLoading } = useGetAvailableFilterDataQuery(undefined, { skip: !opened && !hasActive });
	const availableAliases = filterData?.aliases || [];

	if (!isUninitialized && !isLoading && availableAliases.length === 0 && !hasActive) return null;

	return (
		<FilterSection
			title="Aliases"
			defaultOpen={defaultOpen || hasActive}
			loading={isLoading}
			onOpenChange={setOpened}
			testId="aliases-filter-toggle"
		>
			<SearchableCheckboxList
				inputRef={searchInputRef}
				placeholder="Search aliases"
				items={availableAliases.map((a) => ({ key: a, label: a }))}
				isSelected={(alias) => (filters.aliases || []).includes(alias)}
				onToggle={(alias) => {
					const current = filters.aliases || [];
					const next = current.includes(alias) ? current.filter((a) => a !== alias) : [...current, alias];
					onFiltersChange({ ...filters, aliases: next });
				}}
				testIdPrefix="aliases-filter"
			/>
		</FilterSection>
	);
}

// ---------------------------------------------------------------------------
// SelectedKeysFilter – fetches keys, resolves name→IDs for deduplication
// ---------------------------------------------------------------------------

function SelectedKeysFilter({ filters, onFiltersChange, defaultOpen }: FilterComponentProps) {
	const hasActive = (filters.selected_key_ids || []).length > 0;
	const [opened, setOpened] = useState(defaultOpen || hasActive);
	const searchInputRef = useAutoFocusOnOpen(opened);
	const { data: filterData, isUninitialized, isLoading } = useGetAvailableFilterDataQuery(undefined, { skip: !opened && !hasActive });
	const availableSelectedKeys = filterData?.selected_keys || [];
	const nameToIds = useMemo(() => groupByName(availableSelectedKeys), [availableSelectedKeys]);

	if (!isUninitialized && !isLoading && availableSelectedKeys.length === 0 && !hasActive) return null;

	const toggle = (name: string) => {
		const resolvedIds = nameToIds.get(name) || [name];
		const current = filters.selected_key_ids || [];
		const allSelected = resolvedIds.every((id) => current.includes(id));
		const next = allSelected
			? current.filter((v) => !resolvedIds.includes(v))
			: [...current, ...resolvedIds.filter((id) => !current.includes(id))];
		onFiltersChange({ ...filters, selected_key_ids: next });
	};

	const isSelected = (name: string) => {
		const resolvedIds = nameToIds.get(name) || [name];
		const current = filters.selected_key_ids || [];
		return resolvedIds.every((id) => current.includes(id));
	};

	return (
		<FilterSection
			title="Selected Keys"
			defaultOpen={defaultOpen || hasActive}
			loading={isLoading}
			onOpenChange={setOpened}
			testId="selected-keys-filter-toggle"
		>
			<SearchableCheckboxList
				inputRef={searchInputRef}
				placeholder="Search keys"
				items={dedup(availableSelectedKeys).map((name) => ({ key: name, label: name }))}
				isSelected={isSelected}
				onToggle={toggle}
				testIdPrefix="selected-keys-filter"
			/>
		</FilterSection>
	);
}

// ---------------------------------------------------------------------------
// VirtualKeysFilter
// ---------------------------------------------------------------------------

function VirtualKeysFilter({ filters, onFiltersChange, defaultOpen }: FilterComponentProps) {
	const hasActive = (filters.virtual_key_ids || []).length > 0;
	const [opened, setOpened] = useState(defaultOpen || hasActive);
	const searchInputRef = useAutoFocusOnOpen(opened);
	const { data: filterData, isUninitialized, isLoading } = useGetAvailableFilterDataQuery(undefined, { skip: !opened && !hasActive });
	const availableVirtualKeys = filterData?.virtual_keys || [];
	const nameToIds = useMemo(() => groupByName(availableVirtualKeys), [availableVirtualKeys]);

	if (!isUninitialized && !isLoading && availableVirtualKeys.length === 0 && !hasActive) return null;

	const toggle = (name: string) => {
		const resolvedIds = nameToIds.get(name) || [name];
		const current = filters.virtual_key_ids || [];
		const allSelected = resolvedIds.every((id) => current.includes(id));
		const next = allSelected
			? current.filter((v) => !resolvedIds.includes(v))
			: [...current, ...resolvedIds.filter((id) => !current.includes(id))];
		onFiltersChange({ ...filters, virtual_key_ids: next });
	};

	const isSelected = (name: string) => {
		const resolvedIds = nameToIds.get(name) || [name];
		const current = filters.virtual_key_ids || [];
		return resolvedIds.every((id) => current.includes(id));
	};

	return (
		<FilterSection
			title="Virtual Keys"
			defaultOpen={defaultOpen || hasActive}
			loading={isLoading}
			onOpenChange={setOpened}
			testId="virtual-keys-filter-toggle"
		>
			<SearchableCheckboxList
				inputRef={searchInputRef}
				placeholder="Search virtual keys"
				items={dedup(availableVirtualKeys).map((name) => ({ key: name, label: name }))}
				isSelected={isSelected}
				onToggle={toggle}
				testIdPrefix="virtual-keys-filter"
			/>
		</FilterSection>
	);
}

// ---------------------------------------------------------------------------
// RoutingEnginesFilter
// ---------------------------------------------------------------------------

function RoutingEnginesFilter({ filters, onFiltersChange, defaultOpen }: FilterComponentProps) {
	const hasActive = (filters.routing_engine_used || []).length > 0;
	const [opened, setOpened] = useState(defaultOpen || hasActive);
	const searchInputRef = useAutoFocusOnOpen(opened);
	const { data: filterData, isUninitialized, isLoading } = useGetAvailableFilterDataQuery(undefined, { skip: !opened && !hasActive });
	const availableRoutingEngines = filterData?.routing_engines || [];

	if (!isUninitialized && !isLoading && availableRoutingEngines.length === 0 && !hasActive) return null;

	return (
		<FilterSection
			title="Routing Engines"
			defaultOpen={defaultOpen || hasActive}
			loading={isLoading}
			onOpenChange={setOpened}
			testId="routing-engines-filter-toggle"
		>
			<SearchableCheckboxList
				inputRef={searchInputRef}
				placeholder="Search engines"
				items={availableRoutingEngines.map((engine) => ({
					key: engine,
					label: RoutingEngineUsedLabels[engine as keyof typeof RoutingEngineUsedLabels] ?? engine,
				}))}
				isSelected={(engine) => (filters.routing_engine_used || []).includes(engine)}
				onToggle={(engine) => {
					const current = filters.routing_engine_used || [];
					const next = current.includes(engine) ? current.filter((e) => e !== engine) : [...current, engine];
					onFiltersChange({ ...filters, routing_engine_used: next });
				}}
				testIdPrefix="routing-engines-filter"
			/>
		</FilterSection>
	);
}

// ---------------------------------------------------------------------------
// RoutingRulesFilter
// ---------------------------------------------------------------------------

function RoutingRulesFilter({ filters, onFiltersChange, defaultOpen }: FilterComponentProps) {
	const hasActive = (filters.routing_rule_ids || []).length > 0;
	const [opened, setOpened] = useState(defaultOpen || hasActive);
	const searchInputRef = useAutoFocusOnOpen(opened);
	const { data: filterData, isUninitialized, isLoading } = useGetAvailableFilterDataQuery(undefined, { skip: !opened && !hasActive });
	const availableRoutingRules = filterData?.routing_rules || [];
	const nameToIds = useMemo(() => groupByName(availableRoutingRules), [availableRoutingRules]);

	if (!isUninitialized && !isLoading && availableRoutingRules.length === 0 && !hasActive) return null;

	const toggle = (name: string) => {
		const resolvedIds = nameToIds.get(name) || [name];
		const current = filters.routing_rule_ids || [];
		const allSelected = resolvedIds.every((id) => current.includes(id));
		const next = allSelected
			? current.filter((v) => !resolvedIds.includes(v))
			: [...current, ...resolvedIds.filter((id) => !current.includes(id))];
		onFiltersChange({ ...filters, routing_rule_ids: next });
	};

	const isSelected = (name: string) => {
		const resolvedIds = nameToIds.get(name) || [name];
		const current = filters.routing_rule_ids || [];
		return resolvedIds.every((id) => current.includes(id));
	};

	return (
		<FilterSection
			title="Routing Rules"
			defaultOpen={defaultOpen || hasActive}
			loading={isLoading}
			onOpenChange={setOpened}
			testId="routing-rules-filter-toggle"
		>
			<SearchableCheckboxList
				inputRef={searchInputRef}
				placeholder="Search rules"
				items={dedup(availableRoutingRules).map((name) => ({ key: name, label: name }))}
				isSelected={isSelected}
				onToggle={toggle}
				testIdPrefix="routing-rules-filter"
			/>
		</FilterSection>
	);
}

// ---------------------------------------------------------------------------
// SessionFilter
// ---------------------------------------------------------------------------

function SessionFilter({ filters, onFiltersChange, defaultOpen }: FilterComponentProps) {
	const hasActive = !!filters.parent_request_id;
	return (
		<FilterSection title="Session" defaultOpen={defaultOpen || hasActive} testId="session-filter-toggle">
			<Input
				value={filters.parent_request_id || ""}
				onChange={(e) => onFiltersChange({ ...filters, parent_request_id: e.target.value })}
				placeholder="Parent request ID"
				className="h-8 border-0 text-sm"
				data-testid="session-filter-input"
				autoFocus
			/>
		</FilterSection>
	);
}

// ---------------------------------------------------------------------------
// UserFilter
// ---------------------------------------------------------------------------

function UserFilter({ filters, onFiltersChange, defaultOpen }: FilterComponentProps) {
	const hasActive = !!filters.user_ids?.length;
	return (
		<FilterSection title="User" defaultOpen={defaultOpen || hasActive} testId="user-filter-toggle">
			<Input
				value={filters.user_ids?.[0] || ""}
				onChange={(e) => onFiltersChange({ ...filters, user_ids: e.target.value ? [e.target.value] : [] })}
				placeholder="User ID"
				className="h-8 border-0 text-sm"
				data-testid="user-id-filter-input"
			/>
		</FilterSection>
	);
}

// ---------------------------------------------------------------------------
// CostFilter
// ---------------------------------------------------------------------------

function CostFilter({ filters, onFiltersChange, defaultOpen }: FilterComponentProps) {
	const hasActive = !!filters.missing_cost_only;
	return (
		<FilterSection title="Cost" defaultOpen={defaultOpen || hasActive} testId="cost-filter-toggle">
			<CheckboxFilterItem
				label="Show missing cost only"
				checked={!!filters.missing_cost_only}
				onCheckedChange={(checked) => onFiltersChange({ ...filters, missing_cost_only: !!checked })}
				testId="cost-filter-missing-only-checkbox"
			/>
		</FilterSection>
	);
}

// ---------------------------------------------------------------------------
// MetadataFilters – fetches metadata keys internally
// ---------------------------------------------------------------------------

function MetadataFilters({ filters, onFiltersChange, defaultOpen }: FilterComponentProps) {
	const hasActive = !!filters.metadata_filters && Object.keys(filters.metadata_filters).length > 0;
	const [opened, setOpened] = useState(defaultOpen || hasActive);
	const { data: filterData, isUninitialized, isLoading } = useGetAvailableFilterDataQuery(undefined, { skip: !opened && !hasActive });
	const availableMetadataKeys = filterData?.metadata_keys || {};
	const [customInputs, setCustomInputs] = useState<Record<string, string>>({});

	const handleChange = useCallback(
		(metadataKey: string, value: string | undefined) => {
			const current = { ...(filters.metadata_filters || {}) };
			if (value === undefined) {
				delete current[metadataKey];
			} else {
				current[metadataKey] = value;
			}
			onFiltersChange({
				...filters,
				metadata_filters: Object.keys(current).length > 0 ? current : undefined,
			});
		},
		[filters, onFiltersChange],
	);

	const entries = Object.entries(availableMetadataKeys);
	const isEmpty = !isUninitialized && !isLoading && entries.length === 0 && !hasActive;

	return (
		<FilterSection
			title="Metadata"
			defaultOpen={defaultOpen || hasActive}
			loading={isLoading}
			onOpenChange={setOpened}
			testId="metadata-filter-toggle"
		>
			{isEmpty ? (
				<div className="text-muted-foreground px-3 py-2 text-xs">No metadata keys</div>
			) : (
				entries.map(([metadataKey, values]) => (
					<div key={metadataKey} data-testid={`metadata-${metadataKey}-filter-group`}>
						<div className="text-muted-foreground px-3 pt-2 pb-1 text-xs font-medium">{metadataKey}</div>
						{values.map((value: string) => (
							<CheckboxFilterItem
								key={value}
								label={value}
								checked={filters.metadata_filters?.[metadataKey] === value}
								onCheckedChange={() => {
									const currentValue = filters.metadata_filters?.[metadataKey];
									handleChange(metadataKey, currentValue === value ? undefined : value);
								}}
								testId={`metadata-${metadataKey}-filter-checkbox-${value}`}
							/>
						))}
						<div className="px-3 py-2.5">
							<Input
								className="placeholder:text-muted-foreground h-7 w-full rounded border bg-transparent px-2 text-sm"
								placeholder="Custom value..."
								value={
									customInputs[metadataKey] ??
									(filters.metadata_filters?.[metadataKey] && !values.includes(filters.metadata_filters[metadataKey])
										? filters.metadata_filters[metadataKey]
										: "")
								}
								onChange={(e) => {
									const newVal = e.target.value;
									setCustomInputs((prev) => ({ ...prev, [metadataKey]: newVal }));
									if (newVal === "" && filters.metadata_filters?.[metadataKey]) {
										handleChange(metadataKey, undefined);
									}
								}}
								onKeyDown={(e) => {
									if (e.key === "Enter" && customInputs[metadataKey]?.trim()) {
										handleChange(metadataKey, customInputs[metadataKey].trim());
									}
								}}
								data-testid={`metadata-${metadataKey}-filter-custom-input`}
							/>
						</div>
					</div>
				))
			)}
		</FilterSection>
	);
}