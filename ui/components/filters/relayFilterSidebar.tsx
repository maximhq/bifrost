import { FilterSidebarTrigger } from "@/components/filters/filterSidebarTrigger";
import {
	CheckboxFilterItem,
	EntityFilter,
	FilterSection,
	SearchableCheckboxList,
	useAutoFocusOnOpen,
} from "@/components/filters/primitives";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ScrollArea } from "@/components/ui/scrollArea";
import { useIsMobile } from "@/hooks/use-mobile";
import { useGetRelayLogFilterDataQuery } from "@/lib/store";
import type { RelayLogFilterDimension, RelayLogFilters } from "@/lib/types/relays";
import { formatCostBound, parseCostBound } from "@/lib/utils/relayLogQuery";
import { PanelLeftClose, RotateCcw, Search } from "lucide-react";
import { type KeyboardEvent, useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

const COLLAPSE_STORAGE_KEY = "relay-filter-sidebar-collapsed";

// ---------------------------------------------------------------------------
// RelayFilterSidebar – orchestrator
// ---------------------------------------------------------------------------

interface RelayFilterSidebarProps {
	filters: RelayLogFilters;
	onFiltersChange: (filters: RelayLogFilters) => void;
}

type MultiKey = "relay_names" | "methods" | "status" | "kinds" | "virtual_key_ids" | "apps";

/** The Relay Logs filter sidebar, built on the same primitives as the LLM and MCP log sidebars. */
export function RelayFilterSidebar({ filters, onFiltersChange }: RelayFilterSidebarProps) {
	const { t } = useTranslation("observability");
	const isMobile = useIsMobile();
	const [collapsed, setCollapsed] = useState(false);

	useEffect(() => {
		if (typeof window === "undefined") return;
		if (isMobile) {
			setCollapsed(true);
			return;
		}
		setCollapsed(window.localStorage.getItem(COLLAPSE_STORAGE_KEY) === "true");
	}, [isMobile]);

	const toggleCollapsed = useCallback(() => {
		setCollapsed((prev) => {
			const next = !prev;
			if (typeof window !== "undefined") window.localStorage.setItem(COLLAPSE_STORAGE_KEY, String(next));
			return next;
		});
	}, []);

	const activeFilterCount = useMemo(() => {
		const excluded = ["start_time", "end_time", "search", "content_search", "period"];
		return Object.entries(filters).reduce((c, [key, value]) => {
			if (excluded.includes(key)) return c;
			if (Array.isArray(value)) return c + value.length;
			return c + (value !== undefined && value !== "" ? 1 : 0);
		}, 0);
	}, [filters]);

	const toggle = (key: MultiKey, value: string) => {
		const current = filters[key] ?? [];
		const next = current.includes(value) ? current.filter((v) => v !== value) : [...current, value];
		onFiltersChange({ ...filters, [key]: next });
	};

	const handleReset = useCallback(() => {
		onFiltersChange({ start_time: filters.start_time, end_time: filters.end_time, period: filters.period });
	}, [filters.start_time, filters.end_time, filters.period, onFiltersChange]);

	if (collapsed) {
		return <FilterSidebarTrigger activeFilterCount={activeFilterCount} onClick={toggleCollapsed} testId="relay-filter-sidebar-trigger" />;
	}

	return (
		<div
			className="bg-card fixed inset-y-2 left-2 z-40 flex h-auto w-[calc(100vw-1rem)] max-w-72 shrink-0 flex-col rounded-md border shadow-xl md:static md:h-full md:w-64 md:max-w-none md:rounded-md md:shadow-none"
			data-testid="relay-filter-sidebar"
		>
			<div className="flex h-11 items-center justify-between border-b pr-2 pl-5">
				<span className="text-sm font-semibold">{t("filters.filters")}</span>
				<div className="flex items-center gap-1">
					{activeFilterCount > 0 && (
						<Button
							variant="outline"
							size="sm"
							className="text-muted-foreground h-7 px-2 text-xs"
							onClick={handleReset}
							data-testid="relay-filter-reset-btn"
						>
							<RotateCcw className="size-3" />
							{t("relayLogs.filters.reset")}
						</Button>
					)}
					<Button
						variant="ghost"
						size="icon"
						className="size-7"
						onClick={toggleCollapsed}
						title={t("filters.hideFilters")}
						aria-label={t("filters.hideFilters")}
					>
						<PanelLeftClose className="size-4" />
					</Button>
				</div>
			</div>
			<ScrollArea className="flex flex-1 overflow-y-auto p-2 pb-0" viewportClassName="no-table">
				<div className="flex grow flex-col gap-1">
					{/* First two open by default; the rest open only when they carry a value. */}
					<FilterSection title={t("filters.status")} defaultOpen testId="relay-status-filter-toggle">
						{(
							[
								["success", t("labels.success")],
								["error", t("labels.error")],
								["cancelled", t("labels.cancelled")],
								["processing", t("logs.statuses.processing")],
							] as const
						).map(([status, statusLabel]) => (
							<CheckboxFilterItem
								key={status}
								labelClassName="capitalize"
								label={statusLabel}
								checked={(filters.status ?? []).includes(status)}
								onCheckedChange={() => toggle("status", status)}
								testId={`relay-status-filter-checkbox-${status}`}
							/>
						))}
					</FilterSection>
					<NamesFilter
						filters={filters}
						onFiltersChange={onFiltersChange}
						filterKey="relay_names"
						dimension="relay_names"
						title={t("relayLogs.fields.relay")}
						placeholder={t("relayLogs.filters.searchRelays")}
						testIdPrefix="relay-filter"
						defaultOpen
					/>
					<NamesFilter
						filters={filters}
						onFiltersChange={onFiltersChange}
						filterKey="methods"
						dimension="methods"
						title={t("relayLogs.fields.method")}
						placeholder={t("relayLogs.filters.searchMethods")}
						testIdPrefix="relay-method-filter"
					/>
					<FilterSection
						title={t("relayLogs.filters.billing")}
						defaultOpen={filters.billable !== undefined}
						testId="relay-billing-filter-toggle"
					>
						<CheckboxFilterItem
							label={t("relayLogs.fields.billable")}
							checked={filters.billable === true}
							onCheckedChange={() => onFiltersChange({ ...filters, billable: filters.billable === true ? undefined : true })}
							testId="relay-billing-filter-checkbox-billable"
						/>
						<CheckboxFilterItem
							label={t("relayLogs.fields.notBillable")}
							checked={filters.billable === false}
							onCheckedChange={() => onFiltersChange({ ...filters, billable: filters.billable === false ? undefined : false })}
							testId="relay-billing-filter-checkbox-not-billable"
						/>
					</FilterSection>
					<FilterSection
						title={t("relayLogs.fields.kind")}
						defaultOpen={(filters.kinds ?? []).length > 0}
						testId="relay-kind-filter-toggle"
					>
						<CheckboxFilterItem
							label={t("relayLogs.filters.calls")}
							checked={(filters.kinds ?? []).includes("call")}
							onCheckedChange={() => toggle("kinds", "call")}
							testId="relay-kind-filter-checkbox-call"
						/>
						<CheckboxFilterItem
							label={t("relayLogs.filters.jobSettlements")}
							checked={(filters.kinds ?? []).includes("job")}
							onCheckedChange={() => toggle("kinds", "job")}
							testId="relay-kind-filter-checkbox-job"
						/>
					</FilterSection>
					<CostFilter filters={filters} onFiltersChange={onFiltersChange} />
					<ExactIDFilter
						filters={filters}
						onFiltersChange={onFiltersChange}
						filterKey="session_id"
						title={t("filters.sessionID")}
						placeholder={t("filters.exactSessionID")}
						testId="relay-session-id-filter"
					/>
					<ExactIDFilter
						filters={filters}
						onFiltersChange={onFiltersChange}
						filterKey="job_id"
						title={t("relayLogs.fields.job")}
						placeholder={t("relayLogs.filters.exactJobID")}
						testId="relay-job-id-filter"
					/>
					<ExactIDFilter
						filters={filters}
						onFiltersChange={onFiltersChange}
						filterKey="parent_request_id"
						title={t("logs.detail.parentRequestID")}
						placeholder={t("relayLogs.filters.exactParentRequestID")}
						testId="relay-parent-request-id-filter"
					/>
					<ExactIDFilter
						filters={filters}
						onFiltersChange={onFiltersChange}
						filterKey="agent_correlation_id"
						title={t("filters.agentCorrelationID")}
						placeholder={t("filters.exactAgentCorrelationID")}
						testId="relay-agent-correlation-id-filter"
					/>
					<NamesFilter
						filters={filters}
						onFiltersChange={onFiltersChange}
						filterKey="apps"
						dimension="apps"
						title={t("labels.app")}
						placeholder={t("filters.searchApps")}
						testIdPrefix="relay-app-filter"
						searchable={false}
					/>
					<EntityFilter
						filters={filters}
						onFiltersChange={onFiltersChange}
						filterKey="virtual_key_ids"
						useFilterData={useGetRelayLogFilterDataQuery}
						dimension="virtual_keys"
						title={t("relayLogs.fields.virtualKey")}
						placeholder={t("filters.searchVirtualKeys")}
						testIdPrefix="relay-vk-filter"
					/>
					<EntityFilter
						filters={filters}
						onFiltersChange={onFiltersChange}
						filterKey="user_ids"
						useFilterData={useGetRelayLogFilterDataQuery}
						dimension="users"
						title={t("labels.user")}
						placeholder={t("filters.searchOrAddAUser")}
						testIdPrefix="relay-user-filter"
						allowCustom
					/>
					<EntityFilter
						filters={filters}
						onFiltersChange={onFiltersChange}
						filterKey="team_ids"
						useFilterData={useGetRelayLogFilterDataQuery}
						dimension="teams"
						title={t("relayLogs.filters.teams")}
						placeholder={t("filters.searchOrAddATeam")}
						testIdPrefix="relay-teams-filter"
						allowCustom
					/>
					<EntityFilter
						filters={filters}
						onFiltersChange={onFiltersChange}
						filterKey="customer_ids"
						useFilterData={useGetRelayLogFilterDataQuery}
						dimension="customers"
						title={t("relayLogs.filters.customers")}
						placeholder={t("filters.searchOrAddACustomer")}
						testIdPrefix="relay-customers-filter"
						allowCustom
					/>
					<EntityFilter
						filters={filters}
						onFiltersChange={onFiltersChange}
						filterKey="business_unit_ids"
						useFilterData={useGetRelayLogFilterDataQuery}
						dimension="business_units"
						title={t("relayLogs.filters.businessUnits")}
						placeholder={t("filters.searchOrAddABusinessUnit")}
						testIdPrefix="relay-business-units-filter"
						allowCustom
					/>
					<EntityFilter
						filters={filters}
						onFiltersChange={onFiltersChange}
						filterKey="project_ids"
						useFilterData={useGetRelayLogFilterDataQuery}
						dimension="projects"
						title={t("relayLogs.filters.projects")}
						placeholder={t("filters.searchOrAddAProject")}
						testIdPrefix="relay-projects-filter"
						allowCustom
					/>
				</div>
			</ScrollArea>
		</div>
	);
}

// ---------------------------------------------------------------------------
// Shared helpers & primitives
// ---------------------------------------------------------------------------

interface FilterComponentProps {
	filters: RelayLogFilters;
	onFiltersChange: (filters: RelayLogFilters) => void;
	defaultOpen?: boolean;
}

// ---------------------------------------------------------------------------
// ExactIDFilter – one exact correlation id (session, parent request, agent correlation), as on
// the LLM and MCP sidebars.
// ---------------------------------------------------------------------------

interface ExactIDFilterProps extends FilterComponentProps {
	filterKey: "session_id" | "job_id" | "parent_request_id" | "agent_correlation_id";
	title: string;
	placeholder: string;
	/** Base test id: the section is `<testId>-toggle` and the field `<testId>-input`, as on the LLM sidebar. */
	testId: string;
}

function ExactIDFilter({ filters, onFiltersChange, filterKey, title, placeholder, testId }: ExactIDFilterProps) {
	const hasActive = !!filters[filterKey];
	return (
		<FilterSection title={title} defaultOpen={hasActive} testId={`${testId}-toggle`}>
			<div className="relative">
				<Search className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2" />
				<Input
					value={filters[filterKey] || ""}
					onChange={(event) => onFiltersChange({ ...filters, [filterKey]: event.target.value || undefined })}
					placeholder={placeholder}
					className="h-8 border-0 pl-8 text-sm"
					data-testid={`${testId}-input`}
				/>
			</div>
		</FilterSection>
	);
}

// ---------------------------------------------------------------------------
// NamesFilter – a plain string dimension (relay names, methods, apps). Fetches only once the
// section is opened or carries a value; search is server-side where the dimension supports it.
// ---------------------------------------------------------------------------

interface NamesFilterProps extends FilterComponentProps {
	filterKey: Extract<MultiKey, "relay_names" | "methods" | "apps">;
	dimension: Extract<RelayLogFilterDimension, "relay_names" | "methods" | "apps">;
	title: string;
	placeholder: string;
	testIdPrefix: string;
	/** False for small fixed lists (apps) where the backend does not search the dimension. */
	searchable?: boolean;
}

function NamesFilter({
	filters,
	onFiltersChange,
	defaultOpen,
	filterKey,
	dimension,
	title,
	placeholder,
	testIdPrefix,
	searchable = true,
}: NamesFilterProps) {
	const selectedValues = filters[filterKey];
	const selected = useMemo(() => selectedValues ?? [], [selectedValues]);
	const hasActive = selected.length > 0;
	const [opened, setOpened] = useState(!!defaultOpen || hasActive);
	const searchInputRef = useAutoFocusOnOpen(opened);
	const [searchQuery, setSearchQuery] = useState("");
	const {
		data: filterData,
		isUninitialized,
		isLoading,
		isFetching,
	} = useGetRelayLogFilterDataQuery(
		{ dimensions: [dimension], q: searchable && searchQuery ? searchQuery : undefined },
		{ skip: !opened && !hasActive },
	);
	const available = useMemo(() => filterData?.[dimension] ?? [], [filterData, dimension]);
	const items = useMemo(() => {
		const seen = new Set(available);
		const extras = selected.filter((n) => !seen.has(n));
		return [...available, ...extras].sort().map((n) => ({ key: n, label: n }));
	}, [available, selected]);

	// A section with nothing to offer folds away, unless a search is active: hiding it then would take
	// the search box with it and leave no way to clear the query.
	if (!defaultOpen && !isUninitialized && !isLoading && available.length === 0 && !hasActive && !opened && !searchQuery) return null;

	return (
		<FilterSection
			title={title}
			defaultOpen={!!defaultOpen || hasActive}
			loading={isLoading}
			onOpenChange={setOpened}
			testId={`${testIdPrefix}-toggle`}
		>
			<SearchableCheckboxList
				inputRef={searchInputRef}
				placeholder={placeholder}
				items={items}
				isSelected={(n) => selected.includes(n)}
				onToggle={(n) => {
					const next = selected.includes(n) ? selected.filter((v) => v !== n) : [...selected, n];
					onFiltersChange({ ...filters, [filterKey]: next });
				}}
				onSearch={searchable ? setSearchQuery : undefined}
				fetching={isFetching}
				testIdPrefix={testIdPrefix}
				normalizeTestIdKey
			/>
		</FilterSection>
	);
}

// ---------------------------------------------------------------------------
// EntityFilter – an {id, name} dimension (virtual keys, users, teams, ...). The filter holds ids;
// the list shows names and keeps an id that the search did not return as its own row so a value
// carried in the URL stays visible and clearable.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// CostFilter – min/max bounds in USD. Zero is a real bound (max 0 = free calls only), so the
// inputs commit on blur or Enter and an empty field clears that bound.
// ---------------------------------------------------------------------------

function CostFilter({ filters, onFiltersChange, defaultOpen }: FilterComponentProps) {
	const { t } = useTranslation("observability");
	const hasActive = filters.min_cost !== undefined || filters.max_cost !== undefined;
	const [minText, setMinText] = useState(formatCostBound(filters.min_cost));
	const [maxText, setMaxText] = useState(formatCostBound(filters.max_cost));

	// Mirror the URL back into the fields when a bound changes elsewhere (reset, clear, navigation).
	useEffect(() => setMinText(formatCostBound(filters.min_cost)), [filters.min_cost]);
	useEffect(() => setMaxText(formatCostBound(filters.max_cost)), [filters.max_cost]);

	const commit = (key: "min_cost" | "max_cost", raw: string) => {
		const next = parseCostBound(raw);
		if (next === filters[key]) return;
		onFiltersChange({ ...filters, [key]: next });
	};
	const onKeyDown = (key: "min_cost" | "max_cost") => (event: KeyboardEvent<HTMLInputElement>) => {
		if (event.key === "Enter") {
			event.preventDefault();
			commit(key, event.currentTarget.value);
		}
	};

	return (
		<FilterSection title={t("labels.cost")} defaultOpen={defaultOpen || hasActive} testId="relay-cost-filter-toggle">
			<div className="grid grid-cols-2 gap-2 px-3 py-2">
				<Input
					type="number"
					inputMode="decimal"
					min={0}
					step="any"
					value={minText}
					onChange={(event) => setMinText(event.target.value)}
					onBlur={(event) => commit("min_cost", event.target.value)}
					onKeyDown={onKeyDown("min_cost")}
					placeholder={t("relayLogs.filters.minCost")}
					aria-label={t("relayLogs.filters.minimumCost")}
					className="h-8 text-xs"
					data-testid="relay-cost-filter-min"
				/>
				<Input
					type="number"
					inputMode="decimal"
					min={0}
					step="any"
					value={maxText}
					onChange={(event) => setMaxText(event.target.value)}
					onBlur={(event) => commit("max_cost", event.target.value)}
					onKeyDown={onKeyDown("max_cost")}
					placeholder={t("relayLogs.filters.maxCost")}
					aria-label={t("relayLogs.filters.maximumCost")}
					className="h-8 text-xs"
					data-testid="relay-cost-filter-max"
				/>
			</div>
		</FilterSection>
	);
}