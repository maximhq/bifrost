import { FilterSidebarTrigger } from "@/components/filters/filterSidebarTrigger";
import { CheckboxFilterItem, FilterSection, SearchableCheckboxList, useAutoFocusOnOpen } from "@/components/filters/primitives";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ScrollArea } from "@/components/ui/scrollArea";
import { useIsMobile } from "@/hooks/use-mobile";
import { useGetAgentFilterDataQuery } from "@/lib/store/apis/agentLogsApi";
import { useGetAgentsQuery } from "@/lib/store/apis/agentsApi";
import type { AgentLogFilters } from "@/lib/types/agentLogs";
import { PanelLeftClose, RotateCcw } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

const COLLAPSE_STORAGE_KEY = "agent-log-filter-sidebar-collapsed";

// Operations, event types and task states are closed protocol enums, so the
// sidebar offers them as options instead of free text. Listing them statically
// keeps the options identical across every log store and keeps states that have
// not occurred yet selectable.
const OPERATIONS = [
	"SendMessage",
	"SendStreamingMessage",
	"GetTask",
	"ListTasks",
	"CancelTask",
	"SubscribeToTask",
	"CreateTaskPushNotificationConfig",
	"GetTaskPushNotificationConfig",
	"ListTaskPushNotificationConfigs",
	"DeleteTaskPushNotificationConfig",
	"GetAgentCard",
	"GetExtendedAgentCard",
	"push_notification",
	"push_delivery",
] as const;

const STATUSES = ["success", "error"] as const;

const TASK_STATES = [
	"TASK_STATE_SUBMITTED",
	"TASK_STATE_WORKING",
	"TASK_STATE_INPUT_REQUIRED",
	"TASK_STATE_AUTH_REQUIRED",
	"TASK_STATE_COMPLETED",
	"TASK_STATE_CANCELED",
	"TASK_STATE_FAILED",
	"TASK_STATE_REJECTED",
] as const;

// Filter keys the sidebar owns; time range and paging live in the header/table.
type AgentTextFilterKey = "search" | "request_id" | "trace_id" | "task_id" | "context_id" | "push_config_id" | "delivery_id" | "attempt_id";
type AgentGovernanceFilterKey = "user_id" | "team_id" | "customer_id" | "business_unit_id" | "project_id";
type GovernanceDimension = "users" | "virtual_keys" | "teams" | "customers" | "business_units" | "projects";

interface AgentFilterSidebarProps {
	filters: AgentLogFilters;
	onFiltersChange: (filters: AgentLogFilters) => void;
}

export function AgentFilterSidebar({ filters, onFiltersChange }: AgentFilterSidebarProps) {
	const isMobile = useIsMobile();
	const [collapsed, setCollapsed] = useState(false);

	// Load persisted collapsed state on mount
	useEffect(() => {
		if (typeof window === "undefined") return;
		if (isMobile) {
			setCollapsed(true);
			return;
		}
		const stored = window.localStorage.getItem(COLLAPSE_STORAGE_KEY);
		setCollapsed(stored === "true");
	}, [isMobile]);

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
		const excludedKeys = ["start_time", "end_time", "period", "record_kind"];
		return Object.entries(filters).reduce((c, [key, value]) => {
			if (excludedKeys.includes(key)) return c;
			// Multi-value filters arrive as arrays; an empty one is not a filter.
			if (Array.isArray(value)) return c + (value.length > 0 ? 1 : 0);
			return c + (value ? 1 : 0);
		}, 0);
	}, [filters]);

	const handleReset = useCallback(() => {
		onFiltersChange({
			period: filters.period,
			start_time: filters.start_time,
			end_time: filters.end_time,
		});
	}, [filters.period, filters.start_time, filters.end_time, onFiltersChange]);

	// Collapsed: thin rail with vertical "Filters" label — whole rail is clickable to expand
	if (collapsed) {
		return <FilterSidebarTrigger activeFilterCount={activeFilterCount} onClick={toggleCollapsed} />;
	}

	return (
		<div className="bg-card fixed inset-y-2 left-2 z-40 flex h-auto w-[calc(100vw-1rem)] max-w-72 shrink-0 flex-col rounded-md border shadow-xl md:static md:h-full md:w-64 md:max-w-none md:rounded-md md:shadow-none">
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
					<EnumFilterSection
						title="Status"
						filterKey="status"
						options={STATUSES}
						filters={filters}
						onFiltersChange={onFiltersChange}
						formatLabel={(option) => option.charAt(0).toUpperCase() + option.slice(1)}
						defaultOpen
					/>
					<AgentFilter filters={filters} onFiltersChange={onFiltersChange} />
					<VirtualKeyFilter filters={filters} onFiltersChange={onFiltersChange} />
					<GovernanceFilter title="User" dimension="users" filterKey="user_id" filters={filters} onFiltersChange={onFiltersChange} />
					<GovernanceFilter title="Teams" dimension="teams" filterKey="team_id" filters={filters} onFiltersChange={onFiltersChange} />
					<GovernanceFilter
						title="Customers"
						dimension="customers"
						filterKey="customer_id"
						filters={filters}
						onFiltersChange={onFiltersChange}
					/>
					<GovernanceFilter
						title="Business Units"
						dimension="business_units"
						filterKey="business_unit_id"
						filters={filters}
						onFiltersChange={onFiltersChange}
					/>
					<GovernanceFilter
						title="Projects"
						dimension="projects"
						filterKey="project_id"
						filters={filters}
						onFiltersChange={onFiltersChange}
					/>
					<EnumFilterSection
						title="Operation"
						filterKey="operation"
						options={OPERATIONS}
						filters={filters}
						onFiltersChange={onFiltersChange}
					/>
					<EnumFilterSection
						title="Task State"
						filterKey="task_state"
						options={TASK_STATES}
						filters={filters}
						onFiltersChange={onFiltersChange}
					/>
					<FilterSection title="Agent identifiers" defaultOpen={Boolean(filters.task_id || filters.context_id || filters.push_config_id)}>
						<TextFilterInput label="Context ID" filterKey="context_id" filters={filters} onFiltersChange={onFiltersChange} />
						<TextFilterInput label="Task ID" filterKey="task_id" filters={filters} onFiltersChange={onFiltersChange} />
						<TextFilterInput label="Push Config ID" filterKey="push_config_id" filters={filters} onFiltersChange={onFiltersChange} />
					</FilterSection>
					<FilterSection title="Bifrost identifiers" defaultOpen={Boolean(filters.search || filters.request_id || filters.trace_id)}>
						<TextFilterInput label="Request ID" filterKey="request_id" filters={filters} onFiltersChange={onFiltersChange} />
						<TextFilterInput label="Trace ID" filterKey="trace_id" filters={filters} onFiltersChange={onFiltersChange} />
						<TextFilterInput label="Record ID" filterKey="search" filters={filters} onFiltersChange={onFiltersChange} />
					</FilterSection>
					<FilterSection title="Push relay identifiers" defaultOpen={Boolean(filters.delivery_id || filters.attempt_id)}>
						<TextFilterInput label="Delivery ID" filterKey="delivery_id" filters={filters} onFiltersChange={onFiltersChange} />
						<TextFilterInput label="Attempt ID" filterKey="attempt_id" filters={filters} onFiltersChange={onFiltersChange} />
					</FilterSection>
				</div>
			</ScrollArea>
		</div>
	);
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

interface FilterComponentProps {
	filters: AgentLogFilters;
	onFiltersChange: (filters: AgentLogFilters) => void;
	defaultOpen?: boolean;
}

// ---------------------------------------------------------------------------
// AgentFilter – registered agent names with free-text fallback. Selecting
// several agents matches any of them.
// ---------------------------------------------------------------------------

function AgentFilter({ filters, onFiltersChange, defaultOpen }: FilterComponentProps) {
	const hasActive = (filters.agent_name || []).length > 0;
	const [opened, setOpened] = useState(defaultOpen || hasActive);
	const searchInputRef = useAutoFocusOnOpen(opened);
	const { data, isLoading } = useGetAgentsQuery(undefined, { skip: !opened && !hasActive });
	const items = useMemo(() => {
		const names = (data?.agents ?? []).map((agent) => agent.name);
		const extras = (filters.agent_name || []).filter((name) => !names.includes(name));
		return [...names, ...extras].map((name) => ({ key: name, label: name }));
	}, [data, filters.agent_name]);

	return (
		<FilterSection title="Agent" defaultOpen={defaultOpen || hasActive} loading={isLoading} onOpenChange={setOpened}>
			<SearchableCheckboxList
				inputRef={searchInputRef}
				placeholder="Search or add an agent"
				items={items}
				allowCustom
				isSelected={(name) => (filters.agent_name || []).includes(name)}
				onToggle={(name) => {
					const current = filters.agent_name || [];
					const next = current.includes(name) ? current.filter((n) => n !== name) : [...current, name];
					onFiltersChange({ ...filters, agent_name: next.length > 0 ? next : undefined });
				}}
			/>
		</FilterSection>
	);
}

// ---------------------------------------------------------------------------
// VirtualKeyFilter – historical virtual key snapshots, matching LLM Logs.
// ---------------------------------------------------------------------------

function VirtualKeyFilter({ filters, onFiltersChange, defaultOpen }: FilterComponentProps) {
	const active = useMemo(() => filters.virtual_key_id || [], [filters.virtual_key_id]);
	const hasActive = active.length > 0;
	const [opened, setOpened] = useState(defaultOpen || hasActive);
	const [searchQuery, setSearchQuery] = useState("");
	const searchInputRef = useAutoFocusOnOpen(opened);
	const { data, isLoading, isFetching } = useGetAgentFilterDataQuery(
		{ dimensions: ["virtual_keys"], q: searchQuery || undefined },
		{ skip: !opened && !hasActive },
	);
	const available = useMemo(() => data?.virtual_keys ?? [], [data]);
	const nameToIds = useMemo(() => {
		const result = new Map<string, string[]>();
		for (const key of available) {
			const ids = result.get(key.name) || [];
			ids.push(key.id);
			result.set(key.name, ids);
		}
		return result;
	}, [available]);
	const items = useMemo(() => {
		const names = [...nameToIds.keys()];
		const knownIDs = new Set(available.map((key) => key.id));
		return [
			...names.map((name) => ({ key: name, label: name })),
			...active.filter((id) => !knownIDs.has(id)).map((id) => ({ key: id, label: id })),
		];
	}, [active, available, nameToIds]);

	const resolveIDs = (name: string) => nameToIds.get(name) || [name];

	return (
		<FilterSection title="Virtual Key" defaultOpen={defaultOpen || hasActive} loading={isLoading} onOpenChange={setOpened}>
			<SearchableCheckboxList
				inputRef={searchInputRef}
				placeholder="Search virtual keys"
				items={items}
				isSelected={(name) => resolveIDs(name).every((id) => active.includes(id))}
				onToggle={(name) => {
					const ids = resolveIDs(name);
					const allSelected = ids.every((id) => active.includes(id));
					const next = allSelected ? active.filter((id) => !ids.includes(id)) : [...active, ...ids.filter((id) => !active.includes(id))];
					onFiltersChange({ ...filters, virtual_key_id: next.length > 0 ? next : undefined });
				}}
				onSearch={setSearchQuery}
				fetching={isFetching}
				testIdPrefix="a2a-virtual-key-filter"
			/>
		</FilterSection>
	);
}

function GovernanceFilter({
	title,
	dimension,
	filterKey,
	filters,
	onFiltersChange,
}: FilterComponentProps & { title: string; dimension: GovernanceDimension; filterKey: AgentGovernanceFilterKey }) {
	const active = useMemo(() => filters[filterKey] || [], [filterKey, filters]);
	const hasActive = active.length > 0;
	const [opened, setOpened] = useState(hasActive);
	const [searchQuery, setSearchQuery] = useState("");
	const searchInputRef = useAutoFocusOnOpen(opened);
	const { data, isLoading, isFetching } = useGetAgentFilterDataQuery(
		{ dimensions: [dimension], q: searchQuery || undefined },
		{ skip: !opened && !hasActive },
	);
	const available = useMemo(() => data?.[dimension] ?? [], [data, dimension]);
	const items = useMemo(() => {
		const seen = new Set(available.map((entity) => entity.id));
		return [
			...available.map((entity) => ({ key: entity.id, label: entity.name || entity.id })),
			...active.filter((id) => !seen.has(id)).map((id) => ({ key: id, label: id })),
		];
	}, [active, available]);

	return (
		<FilterSection title={title} defaultOpen={hasActive} loading={isLoading} onOpenChange={setOpened}>
			<SearchableCheckboxList
				inputRef={searchInputRef}
				placeholder={`Search or add ${title.toLowerCase().replace(/s$/, "")}`}
				items={items}
				allowCustom
				isSelected={(id) => active.includes(id)}
				onToggle={(id) => {
					const next = active.includes(id) ? active.filter((value) => value !== id) : [...active, id];
					onFiltersChange({ ...filters, [filterKey]: next.length > 0 ? next : undefined });
				}}
				onSearch={setSearchQuery}
				fetching={isFetching}
				testIdPrefix={`a2a-${filterKey.replaceAll("_", "-")}-filter`}
			/>
		</FilterSection>
	);
}

// ---------------------------------------------------------------------------
// Enum filters – closed option lists. Any number of options may be selected;
// the backend matches rows carrying any of them.
// ---------------------------------------------------------------------------

function EnumFilterSection({
	title,
	filterKey,
	options,
	filters,
	onFiltersChange,
	defaultOpen,
	formatLabel,
}: FilterComponentProps & {
	title: string;
	filterKey: "operation" | "task_state" | "status";
	options: readonly string[];
	defaultOpen?: boolean;
	// Values that are Bifrost's own vocabulary get a display label; Agent protocol
	// enums are rendered verbatim so they match the wire and the logs.
	formatLabel?: (option: string) => string;
}) {
	const active = filters[filterKey] || [];
	return (
		<FilterSection title={title} defaultOpen={defaultOpen || active.length > 0}>
			{options.map((option) => (
				<CheckboxFilterItem
					key={option}
					label={formatLabel ? formatLabel(option) : option}
					checked={active.includes(option)}
					onCheckedChange={() => {
						const next = active.includes(option) ? active.filter((value) => value !== option) : [...active, option];
						onFiltersChange({ ...filters, [filterKey]: next.length > 0 ? next : undefined });
					}}
					testId={`a2a-filter-${filterKey.replaceAll("_", "-")}-${option}`}
				/>
			))}
		</FilterSection>
	);
}

// ---------------------------------------------------------------------------
// Text filters – debounced free-text inputs for single-value backend params.
// ---------------------------------------------------------------------------

function TextFilterInput({
	label,
	placeholder,
	filterKey,
	filters,
	onFiltersChange,
}: FilterComponentProps & { label?: string; placeholder?: string; filterKey: AgentTextFilterKey }) {
	const [localValue, setLocalValue] = useState(filters[filterKey] || "");
	const timeoutRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
	const filtersRef = useRef(filters);

	useEffect(() => {
		filtersRef.current = filters;
	}, [filters]);
	useEffect(() => {
		setLocalValue(filters[filterKey] || "");
	}, [filters, filterKey]);
	useEffect(() => {
		return () => {
			if (timeoutRef.current) clearTimeout(timeoutRef.current);
		};
	}, []);

	const handleChange = (value: string) => {
		setLocalValue(value);
		if (timeoutRef.current) clearTimeout(timeoutRef.current);
		timeoutRef.current = setTimeout(() => {
			onFiltersChange({ ...filtersRef.current, [filterKey]: value.trim() || undefined });
		}, 500);
	};

	return (
		<div className="space-y-1 px-3 py-2">
			{label && <div className="text-muted-foreground text-[11px] font-medium">{label}</div>}
			<Input
				value={localValue}
				onChange={(e) => handleChange(e.target.value)}
				placeholder={placeholder ?? "Exact value"}
				className="h-8 font-mono text-xs"
				data-testid={`a2a-filter-${filterKey.replaceAll("_", "-")}`}
			/>
		</div>
	);
}