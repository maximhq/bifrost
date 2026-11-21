import FullPageLoader from "@/components/fullPageLoader";
import { LoadErrorView } from "@/components/loadErrorView";
import PageTitle from "@/components/pageTitle";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useGetEvalKeysQuery, useGetEvaluatorCatalogQuery } from "@/lib/store";
import type { CatalogEntry } from "@/lib/types/evals";
import { showLoadError } from "@/lib/utils/loadError";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { useNavigate } from "@tanstack/react-router";
import { Plus, Search } from "lucide-react";
import { parseAsArrayOf, parseAsString, useQueryStates } from "nuqs";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { type CatalogFacetFilters, CatalogFilterSidebar } from "../views/catalogFilterSidebar";
import { EvaluatorTypeBadge } from "../views/evaluatorTypeBadge";
import { catalogModelNeed, filterCatalog, groupByCategory } from "../views/evaluatorUtils";
import { InstallBuiltinSheet } from "../views/installBuiltinSheet";
import { PassCriterionText } from "../views/passCriterionText";

export default function EvaluatorCatalogPage() {
	const { t } = useTranslation("config");
	const navigate = useNavigate();
	const canCreate = useRbac(RbacResource.Evaluators, RbacOperation.Create);
	const [urlState, setUrlState] = useQueryStates(
		{
			search: parseAsString.withDefault(""),
			categories: parseAsArrayOf(parseAsString).withDefault([]),
			types: parseAsArrayOf(parseAsString).withDefault([]),
			models: parseAsArrayOf(parseAsString).withDefault([]),
		},
		{ history: "replace" },
	);
	const { data, error, isLoading, refetch } = useGetEvaluatorCatalogQuery();
	const { data: evalKeysData } = useGetEvalKeysQuery(undefined, { refetchOnMountOrArgChange: true });
	const [installing, setInstalling] = useState<CatalogEntry | null>(null);

	const catalog = useMemo(() => data?.catalog ?? [], [data]);
	const categories = useMemo(() => groupByCategory(catalog).map(([category]) => category), [catalog]);
	const facets: CatalogFacetFilters = useMemo(
		() => ({ categories: urlState.categories, types: urlState.types, models: urlState.models }),
		[urlState.categories, urlState.types, urlState.models],
	);
	const groups = useMemo(
		() => groupByCategory(filterCatalog(catalog, { search: urlState.search, ...facets })),
		[catalog, urlState.search, facets],
	);
	const shown = groups.reduce((sum, [, entries]) => sum + entries.length, 0);

	const pageTitle = (
		<PageTitle
			breadcrumbs={[{ label: t("evals.list.title"), to: "/workspace/evals/evaluators" }, { label: t("evals.catalog.breadcrumb") }]}
		>
			{t("evals.catalog.description", { total: catalog.length })}
		</PageTitle>
	);

	if (isLoading) {
		return <FullPageLoader />;
	}

	if (showLoadError(error, data)) {
		return (
			<LoadErrorView entity={t("evals.catalog.title")} error={error} onRetry={() => void refetch()} testId="evaluator-catalog-load-error" />
		);
	}

	return (
		<div className="dark:bg-card no-padding-parent no-border-parent h-[calc(var(--app-content-viewport)_-_var(--app-bottom-padding))]">
			{pageTitle}
			<div className="bg-background flex h-full w-full grow gap-3">
				<CatalogFilterSidebar filters={facets} onFiltersChange={(next) => void setUrlState(next)} categories={categories} />
				<div className="bg-card h-full w-full overflow-hidden rounded-md border">
					<div className="flex h-full flex-col p-4">
						<div className="mb-3 flex flex-wrap items-center gap-3">
							<div className="relative max-w-sm flex-1">
								<Search className="text-muted-foreground absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2" />
								<Input
									aria-label={t("evals.catalog.searchPlaceholder")}
									placeholder={t("evals.catalog.searchPlaceholder")}
									value={urlState.search}
									onChange={(e) => void setUrlState({ search: e.target.value })}
									className="pl-9"
									data-testid="evaluator-catalog-search-input"
								/>
							</div>
							<span className="text-muted-foreground text-xs sm:ml-auto" data-testid="evaluator-catalog-count">
								{t("evals.catalog.showing", { shown, total: catalog.length })}
							</span>
						</div>

						<div className="flex grow flex-col gap-6 overflow-y-auto pr-1">
							{groups.length === 0 && (
								<div
									className="text-muted-foreground flex h-32 items-center justify-center text-sm"
									data-testid="evaluator-catalog-no-results"
								>
									{t("evals.catalog.noResults")}
								</div>
							)}
							{groups.map(([category, entries]) => (
								<section key={category} className="flex flex-col gap-2">
									<h2 className="text-muted-foreground text-xs font-semibold tracking-wide uppercase">{category}</h2>
									<div className="grid grid-cols-1 gap-3 lg:grid-cols-2 2xl:grid-cols-3">
										{entries.map((entry) => (
											<CatalogCard key={entry.key} entry={entry} canCreate={canCreate} onAdd={() => setInstalling(entry)} />
										))}
									</div>
								</section>
							))}
						</div>
					</div>
				</div>
			</div>

			<InstallBuiltinSheet
				entry={installing}
				evalKeys={evalKeysData?.keys ?? []}
				evalKeysKnown={Boolean(evalKeysData)}
				onClose={() => setInstalling(null)}
				onInstalled={(id) => {
					setInstalling(null);
					void navigate({ to: "/workspace/evals/evaluators/editor", search: { id } });
				}}
			/>
		</div>
	);
}

function CatalogCard({ entry, canCreate, onAdd }: { entry: CatalogEntry; canCreate: boolean; onAdd: () => void }) {
	const { t } = useTranslation("config");
	const need = catalogModelNeed(entry);

	return (
		<div className="flex flex-col gap-2 rounded-sm border p-3" data-testid={`evaluator-catalog-card-${entry.key}`}>
			<div className="flex items-start justify-between gap-2">
				<div className="flex min-w-0 flex-wrap items-center gap-2">
					<span className="truncate text-sm font-medium">{entry.name}</span>
					<EvaluatorTypeBadge type={entry.type} />
				</div>
				<Button
					variant="outline"
					size="sm"
					className="h-7 shrink-0"
					disabled={!canCreate}
					onClick={onAdd}
					aria-label={t("evals.catalog.addTitle", { name: entry.name })}
					data-testid={`evaluator-catalog-add-${entry.key}`}
				>
					<Plus className="size-3" />
					{t("evals.catalog.add")}
				</Button>
			</div>
			<p className="text-muted-foreground text-xs leading-relaxed">{entry.description}</p>
			<p className="font-mono text-xs">{entry.variables.join(" · ")}</p>
			<div className="mt-auto flex flex-wrap items-center gap-1.5 pt-1">
				<Badge variant="secondary" className="font-normal">
					{entry.lower_is_better ? t("evals.catalog.lowerIsBetter") : t("evals.catalog.scoreRange", { scale: entry.scale })}
				</Badge>
				{need === "none" && (
					<Badge variant="secondary" className="font-normal">
						{t("evals.modelNeeds.none")}
					</Badge>
				)}
				<span className="text-muted-foreground text-xs">
					{t("evals.catalog.passesAt")} <PassCriterionText criterion={entry.pass_criteria.entry_level} />
				</span>
			</div>
		</div>
	);
}