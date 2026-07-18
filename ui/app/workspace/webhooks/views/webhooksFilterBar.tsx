// Inline filter row above the webhooks table: search input + event and
// status multi-selects + a clear-filters affordance shown only when
// something is active.

import { Button } from "@/components/ui/button";
import { ComboboxSelect } from "@/components/ui/combobox";
import { Input } from "@/components/ui/input";
import { WEBHOOK_EVENTS } from "@/lib/types/webhooks";
import { Search, X } from "lucide-react";

const EVENT_OPTIONS = WEBHOOK_EVENTS.map((event) => ({ label: event.value, value: event.value }));

const STATUS_OPTIONS = [
	{ label: "Enabled", value: "enabled" },
	{ label: "Disabled", value: "disabled" },
];

export interface WebhooksFilterBarProps {
	search: string;
	onSearchChange: (value: string) => void;
	eventFilter: string[];
	onEventFilterChange: (value: string[]) => void;
	statusFilter: string[];
	onStatusFilterChange: (value: string[]) => void;
	hasActiveFilters: boolean;
	onClearFilters: () => void;
}

export default function WebhooksFilterBar(props: WebhooksFilterBarProps) {
	return (
		<div className="flex shrink-0 flex-wrap items-center gap-3">
			<div className="relative max-w-sm min-w-[200px] flex-1">
				<Search className="text-muted-foreground absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2" />
				<Input
					aria-label="Search webhook endpoints"
					placeholder="Search by name or URL"
					value={props.search}
					onChange={(e) => props.onSearchChange(e.target.value)}
					className="pl-9"
					data-testid="webhook-search-input"
				/>
			</div>
			<ComboboxSelect
				multiple
				disableSearch
				compactTrigger
				data-testid="webhooks-event-filter"
				options={EVENT_OPTIONS}
				value={props.eventFilter}
				onValueChange={props.onEventFilterChange}
				placeholder="All events"
				className="h-9 w-[220px]"
			/>
			<ComboboxSelect
				multiple
				disableSearch
				compactTrigger
				data-testid="webhooks-status-filter"
				options={STATUS_OPTIONS}
				value={props.statusFilter}
				onValueChange={props.onStatusFilterChange}
				placeholder="All statuses"
				className="h-9 w-[170px]"
			/>
			{props.hasActiveFilters && (
				<Button variant="ghost" size="sm" onClick={props.onClearFilters} data-testid="webhooks-clear-filters-btn" className="h-9">
					<X className="h-4 w-4" />
					Clear filters
				</Button>
			)}
		</div>
	);
}