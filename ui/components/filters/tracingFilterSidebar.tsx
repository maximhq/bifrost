import { FilterSidebarTrigger } from "@/components/filters/filterSidebarTrigger";
import { CheckboxFilterItem, FilterSection, SearchableCheckboxList } from "@/components/filters/primitives";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ScrollArea } from "@/components/ui/scrollArea";
import { useIsMobile } from "@/hooks/use-mobile";
import { useGetTraceFilterDataQuery } from "@/lib/store";
import type { TraceProject } from "@/lib/types/traces";
import { PanelLeftClose, RotateCcw, Search, FolderCog } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

const COLLAPSE_STORAGE_KEY = "tracing-filter-sidebar-collapsed";

/** The filters the sidebar edits. Time range and search live in the header, as on the log pages. */
export interface TracingSidebarFilters {
	project_ids: string[];
	status: string;
	models: string[];
	kinds: string[];
	environments: string[];
	session_id: string;
	trace_id: string;
}

interface TracingFilterSidebarProps {
	filters: TracingSidebarFilters;
	onFiltersChange: (filters: TracingSidebarFilters) => void;
	projects: TraceProject[];
	/** Opens the projects popup: list, create, and each project's sheet. */
	onManageProjects?: () => void;
}

/** The Tracing filter sidebar, built on the same primitives as the LLM, MCP and relay log sidebars. */
export function TracingFilterSidebar({ filters, onFiltersChange, projects, onManageProjects }: TracingFilterSidebarProps) {
	const { t } = useTranslation("observability");
	const isMobile = useIsMobile();
	const [collapsed, setCollapsed] = useState(false);
	const { data: filterData, isLoading } = useGetTraceFilterDataQuery({ projectIds: filters.project_ids });

	useEffect(() => {
		if (typeof window === "undefined") return;
		if (isMobile) {
			setCollapsed(true);
			return;
		}
		try {
			setCollapsed(window.localStorage.getItem(COLLAPSE_STORAGE_KEY) === "true");
		} catch {
			setCollapsed(false);
		}
	}, [isMobile]);

	const toggleCollapsed = useCallback(() => {
		setCollapsed((prev) => {
			const next = !prev;
			try {
				window.localStorage.setItem(COLLAPSE_STORAGE_KEY, String(next));
			} catch {
				// Storage can be unavailable; the sidebar still toggles for this visit.
			}
			return next;
		});
	}, []);

	const activeFilterCount = useMemo(
		() =>
			filters.project_ids.length +
			filters.models.length +
			filters.kinds.length +
			filters.environments.length +
			(filters.status ? 1 : 0) +
			(filters.session_id ? 1 : 0) +
			(filters.trace_id ? 1 : 0),
		[filters],
	);

	const toggle = (key: "project_ids" | "models" | "kinds" | "environments", value: string) => {
		const current = filters[key];
		onFiltersChange({ ...filters, [key]: current.includes(value) ? current.filter((v) => v !== value) : [...current, value] });
	};

	const reset = () =>
		onFiltersChange({ project_ids: [], status: "", models: [], kinds: [], environments: [], session_id: "", trace_id: "" });

	if (collapsed) {
		return <FilterSidebarTrigger activeFilterCount={activeFilterCount} onClick={toggleCollapsed} testId="tracing-filter-sidebar-trigger" />;
	}

	const statuses: [string, string][] = [
		["error", t("tracing.status.error")],
		["ok", t("tracing.status.ok")],
		["in_progress", t("tracing.inProgress")],
	];

	return (
		<div
			className="bg-card fixed inset-y-2 left-2 z-40 flex h-auto w-[calc(100vw-1rem)] max-w-72 shrink-0 flex-col rounded-md border shadow-xl md:static md:h-full md:w-64 md:max-w-none md:rounded-md md:shadow-none"
			data-testid="tracing-filter-sidebar"
		>
			<div className="flex h-11 items-center justify-between border-b pr-2 pl-5">
				<span className="text-sm font-semibold">{t("filters.filters")}</span>
				<div className="flex items-center gap-1">
					{activeFilterCount > 0 && (
						<Button
							variant="outline"
							size="sm"
							className="text-muted-foreground h-7 px-2 text-xs"
							onClick={reset}
							data-testid="tracing-filter-reset-btn"
						>
							<RotateCcw className="size-3" />
							{t("tracing.filters.reset")}
						</Button>
					)}
					<Button
						variant="ghost"
						size="icon"
						className="size-7"
						onClick={toggleCollapsed}
						title={t("filters.hideFilters")}
						aria-label={t("filters.hideFilters")}
						data-testid="tracing-filter-collapse-btn"
					>
						<PanelLeftClose className="size-4" />
					</Button>
				</div>
			</div>
			<ScrollArea className="flex flex-1 overflow-y-auto p-2 pb-0" viewportClassName="no-table">
				<div className="flex grow flex-col gap-1">
					<FilterSection title={t("filters.status")} defaultOpen testId="tracing-status-filter-toggle">
						{statuses.map(([status, label]) => (
							<CheckboxFilterItem
								key={status}
								label={label}
								checked={filters.status === status}
								// One status at a time: the three are not combinable.
								onCheckedChange={() => onFiltersChange({ ...filters, status: filters.status === status ? "" : status })}
								testId={`tracing-status-filter-checkbox-${status}`}
							/>
						))}
					</FilterSection>
					<FilterSection title={t("tracing.filters.projects")} defaultOpen testId="tracing-project-filter-toggle">
						{projects.length === 0 ? (
							<p className="text-muted-foreground px-3 py-2 text-xs">{t("tracing.filters.noProjects")}</p>
						) : (
							<SearchableCheckboxList
								items={projects.map((project) => ({ key: project.id, label: project.name }))}
								isSelected={(id) => filters.project_ids.includes(id)}
								onToggle={(id) => toggle("project_ids", id)}
								placeholder={t("tracing.filters.searchProjects")}
								testIdPrefix="tracing-project-filter"
							/>
						)}
						{onManageProjects && (
							<button
								type="button"
								onClick={onManageProjects}
								className="text-muted-foreground hover:text-foreground hover:bg-muted/60 mt-1 flex w-full items-center gap-2 rounded-md px-3 py-1.5 text-xs font-medium"
								data-testid="tracing-manage-projects"
							>
								<FolderCog className="size-3.5" />
								{t("tracing.projects.manage")}
							</button>
						)}
					</FilterSection>
					<FilterSection
						title={t("tracing.columns.model")}
						defaultOpen={filters.models.length > 0}
						loading={isLoading}
						testId="tracing-model-filter-toggle"
					>
						<SearchableCheckboxList
							items={(filterData?.models ?? []).map((model) => ({ key: model, label: model }))}
							isSelected={(model) => filters.models.includes(model)}
							onToggle={(model) => toggle("models", model)}
							placeholder={t("tracing.filters.searchModels")}
							testIdPrefix="tracing-model-filter"
						/>
					</FilterSection>
					<FilterSection
						title={t("tracing.filters.spanKind")}
						defaultOpen={filters.kinds.length > 0}
						loading={isLoading}
						testId="tracing-kind-filter-toggle"
					>
						{(filterData?.kinds ?? []).map((kind) => (
							<CheckboxFilterItem
								key={kind}
								labelClassName="capitalize"
								label={kind}
								checked={filters.kinds.includes(kind)}
								onCheckedChange={() => toggle("kinds", kind)}
								testId={`tracing-kind-filter-checkbox-${kind}`}
							/>
						))}
					</FilterSection>
					{(filterData?.environments ?? []).length > 0 && (
						<FilterSection
							title={t("tracing.filters.environment")}
							defaultOpen={filters.environments.length > 0}
							testId="tracing-environment-filter-toggle"
						>
							{(filterData?.environments ?? []).map((environment) => (
								<CheckboxFilterItem
									key={environment}
									label={environment}
									checked={filters.environments.includes(environment)}
									onCheckedChange={() => toggle("environments", environment)}
									testId={`tracing-environment-filter-checkbox-${environment}`}
								/>
							))}
						</FilterSection>
					)}
					<ExactFilter
						title={t("filters.sessionID")}
						placeholder={t("filters.exactSessionID")}
						value={filters.session_id}
						onChange={(session_id) => onFiltersChange({ ...filters, session_id })}
						testId="tracing-session-id-filter"
					/>
					<ExactFilter
						title={t("tracing.filters.traceID")}
						placeholder={t("tracing.filters.exactTraceID")}
						value={filters.trace_id}
						onChange={(trace_id) => onFiltersChange({ ...filters, trace_id })}
						testId="tracing-trace-id-filter"
					/>
				</div>
			</ScrollArea>
		</div>
	);
}

function ExactFilter({
	title,
	placeholder,
	value,
	onChange,
	testId,
}: {
	title: string;
	placeholder: string;
	value: string;
	onChange: (value: string) => void;
	testId: string;
}) {
	return (
		<FilterSection title={title} defaultOpen={!!value} testId={`${testId}-toggle`}>
			<div className="relative">
				<Search className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2" />
				<Input
					value={value}
					onChange={(event) => onChange(event.target.value.trim())}
					placeholder={placeholder}
					className="h-8 border-0 pl-8 text-sm"
					data-testid={`${testId}-input`}
				/>
			</div>
		</FilterSection>
	);
}