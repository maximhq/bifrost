import FullPageLoader from "@/components/fullPageLoader";
import { LoadErrorView } from "@/components/loadErrorView";
import { useDebouncedValue } from "@/hooks/useDebounce";
import { useGetEvalKeysQuery, useGetEvaluatorsQuery } from "@/lib/store";
import type { EvaluatorKeyStatus, EvaluatorSource } from "@/lib/types/evals";
import { showLoadError } from "@/lib/utils/loadError";
import { parseAsArrayOf, parseAsInteger, parseAsString, useQueryStates } from "nuqs";
import { useCallback, useEffect, useMemo } from "react";
import { useTranslation } from "react-i18next";
import { countEvaluatorFilters, type EvaluatorFilters, EvaluatorsFilterSidebar } from "./views/evaluatorsFilterSidebar";
import EvaluatorsTable from "./views/evaluatorsTable";
import { pageOffset } from "./views/evaluatorUtils";

const PAGE_SIZE = 25;

export default function EvaluatorsPage() {
	const { t } = useTranslation("config");
	const [urlState, setUrlState] = useQueryStates(
		{
			search: parseAsString.withDefault(""),
			types: parseAsArrayOf(parseAsString).withDefault([]),
			sources: parseAsArrayOf(parseAsString).withDefault([]),
			statuses: parseAsArrayOf(parseAsString).withDefault([]),
			tag: parseAsString.withDefault(""),
			offset: parseAsInteger.withDefault(0),
		},
		{ history: "replace" },
	);
	const debouncedSearch = useDebouncedValue(urlState.search, 300);

	const filters: EvaluatorFilters = useMemo(
		() => ({ types: urlState.types, sources: urlState.sources, statuses: urlState.statuses, tag: urlState.tag }),
		[urlState.types, urlState.sources, urlState.statuses, urlState.tag],
	);
	const setFilters = useCallback(
		(next: EvaluatorFilters) =>
			void setUrlState({ types: next.types, sources: next.sources, statuses: next.statuses, tag: next.tag || null, offset: 0 }),
		[setUrlState],
	);
	const filtersActive = countEvaluatorFilters(filters) > 0;

	// The offset is part of the URL, which anyone can edit.
	const offset = pageOffset(urlState.offset);
	const { data, currentData, error, isLoading, refetch } = useGetEvaluatorsQuery({
		limit: PAGE_SIZE,
		offset,
		search: debouncedSearch || undefined,
		types: filters.types.length > 0 ? filters.types : undefined,
		// Custom and built-in together are every evaluator, so only one of them is a filter.
		source: filters.sources.length === 1 ? (filters.sources[0] as EvaluatorSource) : undefined,
		status: filters.statuses.length === 1 ? (filters.statuses[0] as EvaluatorKeyStatus) : undefined,
		tag: filters.tag || undefined,
	});
	// Keys change on another page, so read them fresh on every visit.
	const { data: evalKeysData } = useGetEvalKeysQuery(undefined, { refetchOnMountOrArgChange: true });

	const evaluators = useMemo(() => data?.evaluators ?? [], [data]);
	const totalCount = data?.total ?? 0;

	// Snap the offset back when the total shrinks past the current page.
	useEffect(() => {
		if (!data || offset < totalCount) return;
		void setUrlState({ offset: totalCount === 0 ? 0 : Math.floor((totalCount - 1) / PAGE_SIZE) * PAGE_SIZE });
	}, [totalCount, offset, data, setUrlState]);

	const availableTags = useMemo(() => [...new Set(evaluators.flatMap((evaluator) => evaluator.tags ?? []))], [evaluators]);

	if (isLoading) {
		return <FullPageLoader />;
	}

	if (showLoadError(error, data)) {
		return <LoadErrorView entity={t("evals.list.title")} error={error} onRetry={() => void refetch()} testId="evaluators-load-error" />;
	}

	const table = (
		<EvaluatorsTable
			evaluators={evaluators}
			totalCount={totalCount}
			evalKeys={evalKeysData?.keys ?? []}
			evalKeysKnown={Boolean(evalKeysData)}
			pageLoading={!currentData}
			search={urlState.search}
			onSearchChange={(value) => void setUrlState({ search: value, offset: 0 })}
			filtersActive={filtersActive}
			offset={offset}
			limit={PAGE_SIZE}
			onOffsetChange={(offset) => void setUrlState({ offset }, { history: "push" })}
		/>
	);

	// Nothing exists yet and nothing is filtered: the table renders the onboarding state, full width.
	if (totalCount === 0 && !filtersActive && !debouncedSearch && !urlState.search) {
		return <div className="mx-auto flex h-[calc(var(--app-content-viewport)_-_50px)] w-full max-w-7xl flex-col">{table}</div>;
	}

	return (
		<div className="dark:bg-card no-padding-parent no-border-parent h-[calc(var(--app-content-viewport)_-_var(--app-bottom-padding))]">
			<div className="bg-background flex h-full w-full grow gap-3">
				<EvaluatorsFilterSidebar filters={filters} onFiltersChange={setFilters} availableTags={availableTags} />
				<div className="bg-card h-full w-full overflow-hidden rounded-md border">
					<div className="flex h-full flex-col p-4">{table}</div>
				</div>
			</div>
		</div>
	);
}