import { FilterSidebarTrigger } from "@/components/filters/filterSidebarTrigger";
import { CheckboxFilterItem, FilterSection, SearchableCheckboxList, useAutoFocusOnOpen } from "@/components/filters/primitives";
import { Button } from "@/components/ui/button";
import { ScrollArea } from "@/components/ui/scrollArea";
import { useIsMobile } from "@/hooks/use-mobile";
import { useGetWebhookEndpointsQuery } from "@/lib/store";
import {
	WEBHOOK_DELIVERY_OUTCOMES,
	WEBHOOK_DELIVERY_STATUS_CLASSES,
	WEBHOOK_EVENTS,
	WebhookDeliveryFilters,
	WebhookDeliveryOutcome,
	WebhookDeliveryStatusClass,
	WebhookEvent,
} from "@/lib/types/webhooks";
import { PanelLeftClose, RotateCcw } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";

const COLLAPSE_STORAGE_KEY = "webhook-deliveries-filter-sidebar-collapsed";

interface WebhookDeliveriesFilterSidebarProps {
	filters: WebhookDeliveryFilters;
	onFiltersChange: (filters: WebhookDeliveryFilters) => void;
}

export function WebhookDeliveriesFilterSidebar({ filters, onFiltersChange }: WebhookDeliveriesFilterSidebarProps) {
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

	// The time range and the free-text searches live in the header, not here, so
	// they are excluded from the rail's active-filter badge.
	const activeFilterCount = useMemo(
		() =>
			(filters.endpoint_ids?.length ?? 0) +
			(filters.events?.length ?? 0) +
			(filters.outcomes?.length ?? 0) +
			(filters.status_class?.length ?? 0),
		[filters],
	);

	const handleReset = useCallback(() => {
		onFiltersChange({
			start_time: filters.start_time,
			end_time: filters.end_time,
			period: filters.period,
			request_id: filters.request_id,
			delivery_id: filters.delivery_id,
		});
	}, [filters.start_time, filters.end_time, filters.period, filters.request_id, filters.delivery_id, onFiltersChange]);

	if (collapsed) {
		return (
			<FilterSidebarTrigger
				activeFilterCount={activeFilterCount}
				onClick={toggleCollapsed}
				testId="webhook-deliveries-filter-sidebar-trigger"
			/>
		);
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
							data-testid="webhook-deliveries-filter-reset"
						>
							<RotateCcw className="size-3" />
							Reset
						</Button>
					)}
					<Button variant="ghost" size="icon" className="size-7" onClick={toggleCollapsed} title="Hide filters" aria-label="Hide filters">
						<PanelLeftClose className="size-4" />
					</Button>
				</div>
			</div>

			<ScrollArea className="flex flex-1 overflow-y-auto p-2 pb-0" viewportClassName="no-table">
				<div className="flex grow flex-col gap-1">
					<OutcomeFilter filters={filters} onFiltersChange={onFiltersChange} defaultOpen />
					<EventFilter filters={filters} onFiltersChange={onFiltersChange} defaultOpen />
					<StatusClassFilter filters={filters} onFiltersChange={onFiltersChange} />
					<WebhooksFilter filters={filters} onFiltersChange={onFiltersChange} />
				</div>
			</ScrollArea>
		</div>
	);
}

interface FilterComponentProps {
	filters: WebhookDeliveryFilters;
	onFiltersChange: (filters: WebhookDeliveryFilters) => void;
	defaultOpen?: boolean;
}

// Toggles one value in a string-array filter, dropping the key when it empties
// so an exhausted filter disappears from the URL instead of lingering empty.
function toggleValue<T extends string>(selected: T[] | undefined, value: T): T[] | undefined {
	const current = selected ?? [];
	const next = current.includes(value) ? current.filter((v) => v !== value) : [...current, value];
	return next.length ? next : undefined;
}

function OutcomeFilter({ filters, onFiltersChange, defaultOpen }: FilterComponentProps) {
	const selected = filters.outcomes ?? [];
	return (
		<FilterSection title="Outcome" defaultOpen={defaultOpen || selected.length > 0} testId="webhook-deliveries-filter-outcome">
			{WEBHOOK_DELIVERY_OUTCOMES.map((outcome) => (
				<CheckboxFilterItem
					key={outcome.value}
					label={outcome.label}
					checked={selected.includes(outcome.value)}
					onCheckedChange={() =>
						onFiltersChange({ ...filters, outcomes: toggleValue<WebhookDeliveryOutcome>(filters.outcomes, outcome.value) })
					}
					testId={`webhook-deliveries-filter-outcome-${outcome.value}`}
				/>
			))}
		</FilterSection>
	);
}

function EventFilter({ filters, onFiltersChange, defaultOpen }: FilterComponentProps) {
	const selected = filters.events ?? [];
	return (
		<FilterSection title="Event" defaultOpen={defaultOpen || selected.length > 0} testId="webhook-deliveries-filter-event">
			{WEBHOOK_EVENTS.map((event) => (
				<CheckboxFilterItem
					key={event.value}
					label={event.label}
					checked={selected.includes(event.value)}
					onCheckedChange={() => onFiltersChange({ ...filters, events: toggleValue<WebhookEvent>(filters.events, event.value) })}
					testId={`webhook-deliveries-filter-event-${event.value}`}
				/>
			))}
		</FilterSection>
	);
}

function StatusClassFilter({ filters, onFiltersChange, defaultOpen }: FilterComponentProps) {
	const selected = filters.status_class ?? [];
	return (
		<FilterSection title="Response status" defaultOpen={defaultOpen || selected.length > 0} testId="webhook-deliveries-filter-status">
			{WEBHOOK_DELIVERY_STATUS_CLASSES.map((statusClass) => (
				<CheckboxFilterItem
					key={statusClass.value}
					label={statusClass.label}
					checked={selected.includes(statusClass.value)}
					onCheckedChange={() =>
						onFiltersChange({
							...filters,
							status_class: toggleValue<WebhookDeliveryStatusClass>(filters.status_class, statusClass.value),
						})
					}
					testId={`webhook-deliveries-filter-status-${statusClass.value}`}
				/>
			))}
		</FilterSection>
	);
}

function WebhooksFilter({ filters, onFiltersChange, defaultOpen }: FilterComponentProps) {
	const selected = filters.endpoint_ids ?? [];
	const [opened, setOpened] = useState(false);
	const hasActive = selected.length > 0;
	const inputRef = useAutoFocusOnOpen(opened);

	// Endpoints are a short list, so fetch one page and search client-side
	// rather than round-tripping on every keystroke.
	const { data, isLoading, isFetching } = useGetWebhookEndpointsQuery({ limit: 100, offset: 0 }, { skip: !opened && !hasActive });

	const items = useMemo(
		() => (data?.endpoints ?? []).map((endpoint) => ({ key: endpoint.id, label: endpoint.name || endpoint.url })),
		[data],
	);

	return (
		<FilterSection
			title="Webhooks"
			defaultOpen={defaultOpen || hasActive}
			loading={isLoading}
			onOpenChange={(open) => open && setOpened(true)}
			testId="webhook-deliveries-filter-webhooks"
		>
			<SearchableCheckboxList
				items={items}
				isSelected={(key) => selected.includes(key)}
				onToggle={(key) => onFiltersChange({ ...filters, endpoint_ids: toggleValue(filters.endpoint_ids, key) })}
				placeholder="Search webhooks..."
				inputRef={inputRef}
				testIdPrefix="webhook-deliveries-filter-webhooks"
				fetching={isFetching}
			/>
		</FilterSection>
	);
}