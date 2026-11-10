// Inline filter row above the webhooks table: search input + event and
// status multi-selects + a clear-filters affordance shown only when
// something is active.

import { Button } from "@/components/ui/button";
import { ComboboxSelect } from "@/components/ui/combobox";
import { Input } from "@/components/ui/input";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { WEBHOOK_EVENTS } from "@/lib/types/webhooks";
import { Search, X } from "lucide-react";
import { useTranslation } from "react-i18next";

const EVENT_OPTIONS = WEBHOOK_EVENTS.map((event) => ({ label: event.value, value: event.value }));

export interface WebhooksFilterBarProps {
	search: string;
	onSearchChange: (value: string) => void;
	eventFilter: string[];
	onEventFilterChange: (value: string[]) => void;
	statusFilter: string[];
	onStatusFilterChange: (value: string[]) => void;
	hasActiveFilters: boolean;
	onClearFilters: () => void;
	/** Page-level actions rendered at the right end of the same row as the search. */
	actions?: React.ReactNode;
}

export default function WebhooksFilterBar(props: WebhooksFilterBarProps) {
	const { t } = useTranslation("governance");
	const statusOptions = [
		{ label: t("webhooks.filterBar.enabled"), value: "enabled" },
		{ label: t("webhooks.filterBar.disabled"), value: "disabled" },
	];

	return (
		<div className="@container/webhooks-toolbar flex shrink-0 flex-wrap items-center gap-3">
			<div className="relative max-w-sm min-w-0 grow basis-40">
				<Search className="text-muted-foreground absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2" />
				<Input
					aria-label={t("webhooks.filterBar.searchAria")}
					placeholder={t("webhooks.filterBar.searchPlaceholder")}
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
				placeholder={t("webhooks.filterBar.allEvents")}
				className="h-9 w-[180px] @5xl/webhooks-toolbar:w-[220px]"
			/>
			<ComboboxSelect
				multiple
				disableSearch
				compactTrigger
				data-testid="webhooks-status-filter"
				options={statusOptions}
				value={props.statusFilter}
				onValueChange={props.onStatusFilterChange}
				placeholder={t("webhooks.filterBar.allStatuses")}
				className="h-9 w-[150px] @5xl/webhooks-toolbar:w-[170px]"
			/>
			{props.hasActiveFilters && (
				<Tooltip>
					<TooltipTrigger asChild>
						<Button
							variant="ghost"
							onClick={props.onClearFilters}
							aria-label={t("webhooks.filterBar.clearFilters")}
							data-testid="webhooks-clear-filters-btn"
							className="size-9 px-0 @5xl/webhooks-toolbar:w-auto @5xl/webhooks-toolbar:px-3"
						>
							<X className="h-4 w-4" />
							<span className="hidden @5xl/webhooks-toolbar:inline">{t("webhooks.filterBar.clearFilters")}</span>
						</Button>
					</TooltipTrigger>
					<TooltipContent>{t("webhooks.filterBar.clearFilters")}</TooltipContent>
				</Tooltip>
			)}
			{props.actions && <div className="ml-auto flex shrink-0 items-center gap-2">{props.actions}</div>}
		</div>
	);
}