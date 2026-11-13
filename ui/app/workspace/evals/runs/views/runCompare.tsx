import FullPageLoader from "@/components/fullPageLoader";
import { LoadErrorView } from "@/components/loadErrorView";
import PageTitle from "@/components/pageTitle";
import { useCompareEvalRunsQuery } from "@/lib/store";
import type { Run, RunEvaluator } from "@/lib/types/evalRuns";
import { cn } from "@/lib/utils";
import { showLoadError } from "@/lib/utils/loadError";
import { ArrowDownRight, ArrowUpRight } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { RunsPagination } from "./runsPagination";
import { targetText } from "./runsList";
import { barShare, entryScore, RUN_SERIES_COLORS, scoreRange } from "./runUtils";

const PAGE_SIZE = 25;

// round keeps two decimals at most.
const round = (value: number) => Math.round(value * 100) / 100;

// signed renders a difference with its sign, or nothing when there is none to show.
function signed(value: number | null): string {
	if (value === null || round(value) === 0) return "";
	return `${value > 0 ? "+" : ""}${round(value)}`;
}

// evaluatorsOf lists every evaluator any of the runs has, in the order first seen.
function evaluatorsOf(runs: Run[]): RunEvaluator[] {
	const seen = new Map<string, RunEvaluator>();
	for (const run of runs) {
		for (const evaluator of run.evaluators) {
			if (!seen.has(evaluator.id)) seen.set(evaluator.id, evaluator);
		}
	}
	return [...seen.values()];
}

// RunCompare puts runs on the same dataset side by side. Each run is a coloured
// series and each evaluator a column. The top block has every run's average, its
// change against the baseline (the first run) and how many rows moved up or down;
// below it, each dataset row shows every run's score as a number and a bar.
export function RunCompare({ ids }: { ids: string[] }) {
	const { t } = useTranslation("config");
	const [offset, setOffset] = useState(0);
	const { data, error, isLoading, currentData, refetch } = useCompareEvalRunsQuery(
		{ ids, offset, limit: PAGE_SIZE },
		{ skip: ids.length < 2 },
	);

	const title = (
		<PageTitle
			breadcrumbs={[{ label: t("evals.runs.list.title"), to: "/workspace/evals/runs" }, { label: t("evals.runs.list.compare") }]}
			tags={[{ label: t("evals.runs.compare.runs", { count: ids.length }) }]}
		/>
	);
	if (ids.length < 2) {
		return (
			<div className="bg-card text-muted-foreground rounded-sm border p-8 text-center text-sm" data-testid="run-compare-need-two">
				{title}
				{t("evals.runs.compare.needTwo")}
			</div>
		);
	}
	if (isLoading) return <FullPageLoader />;
	if (showLoadError(error, data) || !data) {
		return (
			<LoadErrorView entity={t("evals.runs.list.compare")} error={error} onRetry={() => void refetch()} testId="run-compare-load-error" />
		);
	}

	const { runs, rows, total, changes } = data;
	const evaluators = evaluatorsOf(runs);
	const color = (index: number) => RUN_SERIES_COLORS[index % RUN_SERIES_COLORS.length];
	const mean = (run: Run, evaluatorId: string) => run.summary?.evaluators.find((item) => item.evaluator_id === evaluatorId)?.mean ?? null;
	const columns = { gridTemplateColumns: `minmax(11rem, 1.2fr) repeat(${evaluators.length}, minmax(12rem, 1fr))` };

	return (
		<div className="flex h-[calc(var(--app-content-viewport)_-_var(--app-bottom-padding))] flex-col gap-4" data-testid="run-compare-page">
			{title}

			<ul className="flex flex-wrap gap-x-6 gap-y-2" data-testid="run-compare-legend">
				{runs.map((run, index) => (
					<li key={run.id} className="flex items-center gap-2 text-sm">
						<span className="h-2.5 w-2.5 rounded-full" style={{ background: color(index) }} />
						<span className="font-medium">{run.name}</span>
						<span className="text-muted-foreground">
							{t(`evals.runs.targets.${run.target_type}`)} {targetText(run)}
						</span>
						{index === 0 && <span className="text-muted-foreground text-xs">({t("evals.runs.compare.baseline")})</span>}
					</li>
				))}
			</ul>

			<div className={cn("min-h-0 grow overflow-auto", currentData ? undefined : "opacity-60")}>
				<div className="grid min-w-max gap-x-6" style={columns}>
					<div />
					{evaluators.map((evaluator) => (
						<div key={evaluator.id} className="text-muted-foreground pb-2 text-sm">
							{evaluator.name}
						</div>
					))}

					{/* Averages: the baseline in bold, each other run with its change and the rows that moved. */}
					<div className="text-muted-foreground border-b pb-3 text-xs">{t("evals.runs.compare.averageHint")}</div>
					{evaluators.map((evaluator) => {
						const base = mean(runs[0], evaluator.id);
						return (
							<div key={evaluator.id} className="flex flex-col gap-1 border-b pb-3" data-testid={`run-compare-average-${evaluator.name}`}>
								{runs.map((run, index) => {
									const value = mean(run, evaluator.id);
									const change = index === 0 ? undefined : changes[run.id]?.[evaluator.id];
									return (
										<div key={run.id} className="flex items-baseline justify-between gap-3">
											<span className="font-mono" style={{ color: index === 0 ? undefined : color(index) }}>
												<span className={cn(index === 0 ? "text-xl font-semibold" : "text-lg")}>{value === null ? "–" : round(value)}</span>{" "}
												{index === 0 ? (
													<span className="text-muted-foreground text-xs">{t("evals.runs.compare.avg")}</span>
												) : (
													<span className="text-muted-foreground text-xs">
														{value !== null && base !== null ? signed(value - base) : ""}
													</span>
												)}
											</span>
											{change && (
												<span className="flex items-center gap-2 font-mono text-xs">
													<span
														className="flex items-center text-emerald-700 dark:text-emerald-400"
														title={t("evals.runs.compare.improved", { count: change.improved })}
													>
														<ArrowUpRight className="h-3.5 w-3.5" aria-hidden />
														<span className="sr-only">{t("evals.runs.compare.improved", { count: change.improved })}</span>
														<span aria-hidden>{change.improved}</span>
													</span>
													<span
														className="flex items-center text-red-700 dark:text-red-400"
														title={t("evals.runs.compare.regressed", { count: change.regressed })}
													>
														<ArrowDownRight className="h-3.5 w-3.5" aria-hidden />
														<span className="sr-only">{t("evals.runs.compare.regressed", { count: change.regressed })}</span>
														<span aria-hidden>{change.regressed}</span>
													</span>
												</span>
											)}
										</div>
									);
								})}
							</div>
						);
					})}

					{/* One block per dataset row: every run's score as a number and a bar. */}
					{rows.map((row, rowIndex) => {
						const input = row.variables.input ?? Object.values(row.variables)[0] ?? "";
						return (
							<div key={row.dataset_row_id} className="contents" data-testid={`run-compare-row-${offset + rowIndex + 1}`}>
								<div className="border-b py-3 pr-2">
									<div className="text-muted-foreground font-mono text-xs">{offset + rowIndex + 1}</div>
									<div className="line-clamp-3 text-sm">{input}</div>
								</div>
								{evaluators.map((evaluator) => {
									const scores = row.entries.map((entry) => entryScore(entry, evaluator.id));
									const range = scoreRange(scores);
									return (
										<div key={evaluator.id} className="flex flex-col gap-1.5 border-b py-3">
											{runs.map((run, index) => {
												const value = scores[index];
												const entry = row.entries[index];
												return (
													<div key={run.id}>
														<div className="font-mono" style={{ color: index === 0 ? undefined : color(index) }}>
															{value === null ? (
																<span className="text-muted-foreground font-sans text-xs">
																	{entry ? t(`evals.runs.entryStatus.${entry.status}`) : t("evals.runs.compare.missing")}
																</span>
															) : (
																<>
																	{round(value)}{" "}
																	<span className="text-muted-foreground text-xs">
																		{index > 0 && scores[0] !== null ? signed(value - scores[0]) : ""}
																	</span>
																</>
															)}
														</div>
														<div className="bg-muted mt-0.5 h-0.5 rounded-full">
															<div
																className="h-0.5 rounded-full"
																style={{ width: `${barShare(value, range)}%`, background: color(index) }}
															/>
														</div>
													</div>
												);
											})}
										</div>
									);
								})}
							</div>
						);
					})}
				</div>
			</div>

			<RunsPagination offset={offset} limit={PAGE_SIZE} total={total} onOffsetChange={setOffset} testId="run-compare" />
		</div>
	);
}