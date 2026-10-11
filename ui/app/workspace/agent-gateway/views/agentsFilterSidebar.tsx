import { FilterSidebarTrigger } from "@/components/filters/filterSidebarTrigger";
import { CheckboxFilterItem, FilterSection, SearchableCheckboxList, useAutoFocusOnOpen } from "@/components/filters/primitives";
import { Button } from "@/components/ui/button";
import { ScrollArea } from "@/components/ui/scrollArea";
import { useIsMobile } from "@/hooks/use-mobile";
import { useGetVirtualKeysQuery } from "@/lib/store";
import { PanelLeftClose, RotateCcw } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";

const COLLAPSE_STORAGE_KEY = "agents-filter-sidebar-collapsed";
const VK_PAGE_SIZE = 25;

// The /api/agents list has no server-side filtering, so every facet here is
// applied client-side by the page over the full registration list. Values are
// the underlying field values so one checkbox model covers all sections.
export interface AgentFilters {
	status: string[]; // subset of ["true", "false"] → enabled
	upstream_auth: string[]; // subset of ["discovery", "runtime", "none"]
	only_allowed_by_default: boolean;
	virtual_keys: string[];
}

export const EMPTY_AGENT_FILTERS: AgentFilters = {
	status: [],
	upstream_auth: [],
	only_allowed_by_default: false,
	virtual_keys: [],
};

interface FilterOption {
	value: string;
	label: string;
}

const STATUS_OPTIONS: FilterOption[] = [
	{ value: "true", label: "Enabled" },
	{ value: "false", label: "Disabled" },
];

// Upstream auth is derived from which credentials a registration carries:
// discovery_auth is sent when fetching the card, runtime_auth on proxied calls.
const UPSTREAM_AUTH_OPTIONS: FilterOption[] = [
	{ value: "discovery", label: "Discovery" },
	{ value: "runtime", label: "Runtime" },
	{ value: "none", label: "None" },
];

// Reserved key for the pinned "Allowed by default" row, namespaced so it can
// never collide with a real virtual key id.
const ALLOWED_BY_DEFAULT_KEY = "__allowed_by_default__";

interface SidebarProps {
	filters: AgentFilters;
	onFiltersChange: (filters: AgentFilters) => void;
}

export function AgentsFilterSidebar({ filters, onFiltersChange }: SidebarProps) {
	const isMobile = useIsMobile();
	const [collapsed, setCollapsed] = useState(false);

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

	const activeFilterCount = useMemo(
		() =>
			filters.status.length +
			filters.upstream_auth.length +
			filters.virtual_keys.length +
			(filters.only_allowed_by_default ? 1 : 0),
		[filters],
	);

	const handleReset = useCallback(() => onFiltersChange(EMPTY_AGENT_FILTERS), [onFiltersChange]);

	if (collapsed) {
		return <FilterSidebarTrigger activeFilterCount={activeFilterCount} onClick={toggleCollapsed} testId="agentsFilterSidebar-toggle-show" />;
	}

	return (
		<div className="bg-card fixed inset-y-2 left-2 z-40 flex h-auto w-[calc(100vw-1rem)] max-w-72 shrink-0 flex-col rounded-md border shadow-xl md:static md:h-full md:w-64 md:max-w-none md:rounded-md md:shadow-none">
			<div className="flex h-11 items-center justify-between border-b pr-2 pl-5">
				<span className="text-sm font-semibold">Filters</span>
				<div className="flex items-center gap-1">
					{activeFilterCount > 0 && (
						<Button
							variant="outline"
							size="sm"
							className="text-muted-foreground h-7 px-2 text-xs"
							onClick={handleReset}
							data-testid="agentsFilterSidebar-reset-button"
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
						data-testid="agentsFilterSidebar-toggle-hide"
					>
						<PanelLeftClose className="size-4" />
					</Button>
				</div>
			</div>

			<ScrollArea className="flex flex-1 overflow-y-auto p-2 pb-0" viewportClassName="no-table">
				<div className="flex grow flex-col gap-1">
					<CheckboxFilterSection
						title="Status"
						options={STATUS_OPTIONS}
						selected={filters.status}
						defaultOpen
						onChange={(status) => onFiltersChange({ ...filters, status })}
						testIdPrefix="agents-filter-status"
					/>
					<CheckboxFilterSection
						title="Upstream Auth"
						options={UPSTREAM_AUTH_OPTIONS}
						selected={filters.upstream_auth}
						onChange={(upstream_auth) => onFiltersChange({ ...filters, upstream_auth })}
						testIdPrefix="agents-filter-upstream-auth"
					/>
					<VKAccessFilterSection filters={filters} onFiltersChange={onFiltersChange} />
				</div>
			</ScrollArea>
		</div>
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
	const toggle = (value: string) => {
		onChange(selected.includes(value) ? selected.filter((v) => v !== value) : [...selected, value]);
	};

	return (
		<FilterSection
			title={title}
			defaultOpen={defaultOpen || selected.length > 0}
			testId={testIdPrefix ? `${testIdPrefix}-toggle` : undefined}
		>
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

// Access mirrors the MCP catalog: the pinned first row is "Allowed by default",
// the rest are individual virtual keys resolved via server-side search. They OR
// together — an agent matches if it is allowed by default or granted one of the
// selected keys.
function VKAccessFilterSection({ filters, onFiltersChange }: SidebarProps) {
	const hasActive = filters.only_allowed_by_default || filters.virtual_keys.length > 0;
	const [opened, setOpened] = useState(hasActive);
	const [searchQuery, setSearchQuery] = useState("");
	const searchInputRef = useAutoFocusOnOpen(opened);

	const { data, isFetching } = useGetVirtualKeysQuery(
		{ limit: VK_PAGE_SIZE, offset: 0, search: searchQuery || undefined },
		{ skip: !opened && !hasActive },
	);
	const virtualKeys = data?.virtual_keys || [];

	const isSelected = (key: string) =>
		key === ALLOWED_BY_DEFAULT_KEY ? filters.only_allowed_by_default : filters.virtual_keys.includes(key);

	const toggle = (key: string) => {
		if (key === ALLOWED_BY_DEFAULT_KEY) {
			onFiltersChange({ ...filters, only_allowed_by_default: !filters.only_allowed_by_default });
			return;
		}
		const next = filters.virtual_keys.includes(key) ? filters.virtual_keys.filter((v) => v !== key) : [...filters.virtual_keys, key];
		onFiltersChange({ ...filters, virtual_keys: next });
	};

	return (
		<FilterSection title="Access" defaultOpen={hasActive} onOpenChange={setOpened} testId="agents-filter-vk-access-toggle">
			<CheckboxFilterItem
				label="Allowed by default"
				checked={filters.only_allowed_by_default}
				onCheckedChange={() => toggle(ALLOWED_BY_DEFAULT_KEY)}
				testId={`agents-filter-vk-checkbox-${ALLOWED_BY_DEFAULT_KEY}`}
			/>
			<SearchableCheckboxList
				inputRef={searchInputRef}
				placeholder="Search virtual keys"
				items={virtualKeys.map((vk) => ({ key: vk.id, label: vk.name }))}
				isSelected={isSelected}
				onToggle={toggle}
				onSearch={setSearchQuery}
				fetching={isFetching}
				testIdPrefix="agents-filter-vk"
			/>
		</FilterSection>
	);
}
