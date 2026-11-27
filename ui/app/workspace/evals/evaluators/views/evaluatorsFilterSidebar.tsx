import { FilterSidebarTrigger } from "@/components/filters/filterSidebarTrigger";
import { CheckboxFilterItem, FilterSection, SearchableCheckboxList } from "@/components/filters/primitives";
import { Button } from "@/components/ui/button";
import { ScrollArea } from "@/components/ui/scrollArea";
import { useIsMobile } from "@/hooks/use-mobile";
import type { EvaluatorKeyStatus, EvaluatorSource } from "@/lib/types/evals";
import { PanelLeftClose, RotateCcw } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { EVALUATOR_TYPES } from "./evaluatorUtils";

const COLLAPSE_STORAGE_KEY = "evaluators-filter-sidebar-collapsed";

const SOURCES: EvaluatorSource[] = ["custom", "builtin"];
const STATUSES: EvaluatorKeyStatus[] = ["ready", "no_key"];

export interface EvaluatorFilters {
	types: string[];
	// Both or neither selected means "no filter"; the page sends a source only for one.
	sources: string[];
	// Same rule as sources: one selected is a filter, both or neither is not.
	statuses: string[];
	// The API matches one exact tag.
	tag: string;
}

export const EMPTY_EVALUATOR_FILTERS: EvaluatorFilters = { types: [], sources: [], statuses: [], tag: "" };

export function countEvaluatorFilters(filters: EvaluatorFilters): number {
	return filters.types.length + (filters.sources.length === 1 ? 1 : 0) + (filters.statuses.length === 1 ? 1 : 0) + (filters.tag ? 1 : 0);
}

function toggle(selected: string[], value: string): string[] {
	return selected.includes(value) ? selected.filter((v) => v !== value) : [...selected, value];
}

interface EvaluatorsFilterSidebarProps {
	filters: EvaluatorFilters;
	onFiltersChange: (filters: EvaluatorFilters) => void;
	// Tags to offer. Any other tag can be typed in.
	availableTags: string[];
}

export function EvaluatorsFilterSidebar({ filters, onFiltersChange, availableTags }: EvaluatorsFilterSidebarProps) {
	const { t } = useTranslation("config");
	const { t: tFilters } = useTranslation("governance");
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

	// The write to storage stays out of the state updater: React may run an updater
	// more than once, and it must have no effect but its return value.
	const toggleCollapsed = useCallback(() => {
		const next = !collapsed;
		setCollapsed(next);
		if (typeof window !== "undefined") {
			window.localStorage.setItem(COLLAPSE_STORAGE_KEY, String(next));
		}
	}, [collapsed]);

	const activeFilterCount = countEvaluatorFilters(filters);

	// The selected tag stays listed even when no evaluator on this page carries it.
	const tagItems = useMemo(() => {
		const tags = new Set(availableTags);
		if (filters.tag) tags.add(filters.tag);
		return [...tags].sort((a, b) => a.localeCompare(b)).map((tag) => ({ key: tag, label: tag }));
	}, [availableTags, filters.tag]);

	if (collapsed) {
		return (
			<FilterSidebarTrigger activeFilterCount={activeFilterCount} onClick={toggleCollapsed} testId="evaluators-filter-sidebar-trigger" />
		);
	}

	return (
		<div className="bg-card fixed inset-y-2 left-2 z-40 flex h-auto w-[calc(100vw-1rem)] max-w-72 shrink-0 flex-col rounded-md border shadow-xl md:static md:h-full md:w-64 md:max-w-none md:rounded-md md:shadow-none">
			<div className="flex h-11 items-center justify-between border-b pr-2 pl-5">
				<span className="text-sm font-semibold">{tFilters("filters.filters")}</span>
				<div className="flex items-center gap-1">
					{activeFilterCount > 0 && (
						<Button
							variant="outline"
							size="sm"
							className="text-muted-foreground h-7 px-2 text-xs"
							onClick={() => onFiltersChange(EMPTY_EVALUATOR_FILTERS)}
							data-testid="evaluators-filter-reset"
						>
							<RotateCcw className="size-3" />
							{tFilters("filters.reset")}
						</Button>
					)}
					<Button
						variant="ghost"
						size="icon"
						className="size-7"
						onClick={toggleCollapsed}
						title={tFilters("filters.hideFilters")}
						aria-label={tFilters("filters.hideFilters")}
						data-testid="evaluators-filter-hide"
					>
						<PanelLeftClose className="size-4" />
					</Button>
				</div>
			</div>

			<ScrollArea className="flex flex-1 overflow-y-auto p-2 pb-0" viewportClassName="no-table">
				<div className="flex grow flex-col gap-1">
					<FilterSection title={t("evals.filters.type")} defaultOpen testId="evaluators-filter-type">
						{EVALUATOR_TYPES.map((type) => (
							<CheckboxFilterItem
								key={type}
								label={t(`evals.types.${type}`)}
								checked={filters.types.includes(type)}
								onCheckedChange={() => onFiltersChange({ ...filters, types: toggle(filters.types, type) })}
								testId={`evaluators-filter-type-${type}`}
							/>
						))}
					</FilterSection>
					<FilterSection title={t("evals.filters.source")} defaultOpen testId="evaluators-filter-source">
						{SOURCES.map((source) => (
							<CheckboxFilterItem
								key={source}
								label={t(`evals.sources.${source}`)}
								checked={filters.sources.includes(source)}
								onCheckedChange={() => onFiltersChange({ ...filters, sources: toggle(filters.sources, source) })}
								testId={`evaluators-filter-source-${source}`}
							/>
						))}
					</FilterSection>
					<FilterSection title={t("evals.filters.status")} defaultOpen testId="evaluators-filter-status">
						{STATUSES.map((status) => (
							<CheckboxFilterItem
								key={status}
								label={t(`evals.statuses.${status}`)}
								checked={filters.statuses.includes(status)}
								onCheckedChange={() => onFiltersChange({ ...filters, statuses: toggle(filters.statuses, status) })}
								testId={`evaluators-filter-status-${status}`}
							/>
						))}
					</FilterSection>
					<FilterSection title={t("evals.filters.tags")} defaultOpen={Boolean(filters.tag)} testId="evaluators-filter-tags">
						<SearchableCheckboxList
							items={tagItems}
							isSelected={(tag) => filters.tag === tag}
							onToggle={(tag) => onFiltersChange({ ...filters, tag: filters.tag === tag ? "" : tag })}
							placeholder={t("evals.filters.searchTags")}
							allowCustom
							normalizeTestIdKey
							testIdPrefix="evaluators-filter-tag"
						/>
					</FilterSection>
				</div>
			</ScrollArea>
		</div>
	);
}