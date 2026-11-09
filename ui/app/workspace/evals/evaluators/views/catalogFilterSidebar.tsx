import { FilterSidebarTrigger } from "@/components/filters/filterSidebarTrigger";
import { CheckboxFilterItem, FilterSection } from "@/components/filters/primitives";
import { Button } from "@/components/ui/button";
import { ScrollArea } from "@/components/ui/scrollArea";
import { useIsMobile } from "@/hooks/use-mobile";
import { PanelLeftClose, RotateCcw } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { CATALOG_MODEL_NEEDS } from "./evaluatorUtils";

const COLLAPSE_STORAGE_KEY = "evaluator-catalog-filter-sidebar-collapsed";

const CATALOG_TYPES = ["ai", "statistical"] as const;

export interface CatalogFacetFilters {
	categories: string[];
	types: string[];
	models: string[];
}

export const EMPTY_CATALOG_FILTERS: CatalogFacetFilters = { categories: [], types: [], models: [] };

function toggle(selected: string[], value: string): string[] {
	return selected.includes(value) ? selected.filter((v) => v !== value) : [...selected, value];
}

interface CatalogFilterSidebarProps {
	filters: CatalogFacetFilters;
	onFiltersChange: (filters: CatalogFacetFilters) => void;
	// Categories in catalog order. They are names the gateway ships.
	categories: string[];
}

export function CatalogFilterSidebar({ filters, onFiltersChange, categories }: CatalogFilterSidebarProps) {
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

	const activeFilterCount = filters.categories.length + filters.types.length + filters.models.length;

	if (collapsed) {
		return (
			<FilterSidebarTrigger
				activeFilterCount={activeFilterCount}
				onClick={toggleCollapsed}
				testId="evaluator-catalog-filter-sidebar-trigger"
			/>
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
							onClick={() => onFiltersChange(EMPTY_CATALOG_FILTERS)}
							data-testid="evaluator-catalog-filter-reset"
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
						data-testid="evaluator-catalog-filter-hide"
					>
						<PanelLeftClose className="size-4" />
					</Button>
				</div>
			</div>

			<ScrollArea className="flex flex-1 overflow-y-auto p-2 pb-0" viewportClassName="no-table">
				<div className="flex grow flex-col gap-1">
					<FilterSection title={t("evals.filters.category")} defaultOpen testId="evaluator-catalog-filter-category">
						{categories.map((category) => (
							<CheckboxFilterItem
								key={category}
								label={category}
								checked={filters.categories.includes(category)}
								onCheckedChange={() => onFiltersChange({ ...filters, categories: toggle(filters.categories, category) })}
								testId={`evaluator-catalog-filter-category-${category.toLowerCase().replace(/[^a-z0-9]+/g, "-")}`}
							/>
						))}
					</FilterSection>
					<FilterSection title={t("evals.filters.type")} defaultOpen testId="evaluator-catalog-filter-type">
						{CATALOG_TYPES.map((type) => (
							<CheckboxFilterItem
								key={type}
								label={t(`evals.types.${type}`)}
								checked={filters.types.includes(type)}
								onCheckedChange={() => onFiltersChange({ ...filters, types: toggle(filters.types, type) })}
								testId={`evaluator-catalog-filter-type-${type}`}
							/>
						))}
					</FilterSection>
					<FilterSection title={t("evals.filters.modelNeeded")} defaultOpen testId="evaluator-catalog-filter-model">
						{CATALOG_MODEL_NEEDS.map((need) => (
							<CheckboxFilterItem
								key={need}
								label={t(`evals.modelNeeds.${need}`)}
								checked={filters.models.includes(need)}
								onCheckedChange={() => onFiltersChange({ ...filters, models: toggle(filters.models, need) })}
								testId={`evaluator-catalog-filter-model-${need}`}
							/>
						))}
					</FilterSection>
				</div>
			</ScrollArea>
		</div>
	);
}