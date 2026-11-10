import FullPageLoader from "@/components/fullPageLoader";
import { LoadErrorView } from "@/components/loadErrorView";
import PageTitle from "@/components/pageTitle";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
} from "@/components/ui/alertDialog";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { Label } from "@/components/ui/label";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useToast } from "@/hooks/use-toast";
import { useDebouncedValue } from "@/hooks/useDebounce";
import {
	getErrorMessage,
	useAddEvalRunEvaluatorsMutation,
	useDeleteEvalRunMutation,
	useGetEvalDatasetQuery,
	useGetEvalRunEntriesQuery,
	useCompareEvalRunsQuery,
	useGetEvalRunQuery,
	useGetEvalRunsQuery,
	useGetEvaluatorsQuery,
	useRerunEvalRunMutation,
	useReviewEvalRunResultMutation,
	useStopEvalRunMutation,
	useUpdateEvalRunMutation,
} from "@/lib/store";
import type { Run, RunEntry, RunEvaluator, RunEvaluatorSummary, RunResult } from "@/lib/types/evalRuns";
import { cn } from "@/lib/utils";
import { showLoadError } from "@/lib/utils/loadError";
import { Copy, Download, MoreHorizontal, Pencil, Plus, RotateCw, Search, Square, Trash2, TrendingDown, TrendingUp } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { RunsPagination } from "./runsPagination";
import { targetText } from "./runsList";
import {
	barShare,
	entryScore,
	formatDuration,
	formatScore,
	isRunActive,
	resultOf,
	resultTone,
	runDurationMs,
	runExportUrl,
	runPassRate,
	scoreDelta,
	scoreRange,
	signed,
	toneClass,
} from "./runUtils";

const PAGE_SIZE = 50;
// "worse" and "better" are measured against a baseline run, and are offered only
// while one is picked.
type EntryFilter = "all" | "notPassed" | "failed" | "worse" | "better";
const NO_BASELINE = "none";
// The variable a dataset's expected output arrives in.
const EXPECTED = "expected_output";

interface RunReportProps {
	id: string;
	canUpdate: boolean;
	canDelete: boolean;
	// Called after the run was deleted, to go back to the list.
	onDeleted: () => void;
}

// RunReport is one run: its summary, each evaluator's results, and the entries row by
// row, with one entry open beside the table. It refreshes while the run is active.
export function RunReport({ id, canUpdate, canDelete, onDeleted }: RunReportProps) {
	const { t, i18n } = useTranslation("config");
	const { t: tCommon } = useTranslation("common");
	const { toast } = useToast();

	const { data, error, isLoading, refetch } = useGetEvalRunQuery(id);
	const run = data?.run;
	const active = run ? isRunActive(run.status) : false;
	useGetEvalRunQuery(id, { pollingInterval: active ? 2000 : 0, skip: !active });
	const { data: datasetData } = useGetEvalDatasetQuery(run?.dataset_id ?? "", { skip: !run });

	const [filter, setFilter] = useState<EntryFilter>("all");
	// Another run on the same dataset that this one is read against, or "".
	const [baseline, setBaseline] = useState("");
	const [search, setSearch] = useState("");
	const [offset, setOffset] = useState(0);
	const [openEntry, setOpenEntry] = useState("");
	const [addOpen, setAddOpen] = useState(false);
	const [renameOpen, setRenameOpen] = useState(false);
	const [deleteOpen, setDeleteOpen] = useState(false);
	const debouncedSearch = useDebouncedValue(search, 300);
	useEffect(() => setOffset(0), [filter, debouncedSearch, baseline]);

	// A baseline is picked from the other runs on this run's dataset: rows are matched
	// on the dataset row they came from.
	const { data: siblingData } = useGetEvalRunsQuery({ dataset_id: run?.dataset_id, limit: 50 }, { skip: !run });
	const baselineOptions = (siblingData?.runs ?? []).filter((other) => other.id !== id);
	const { data: comparison } = useCompareEvalRunsQuery({ ids: [baseline, id], limit: 1 }, { skip: !baseline });
	const baselineRun = baseline ? comparison?.runs.find((other) => other.id === baseline) : undefined;
	const changes = baseline ? comparison?.changes[id] : undefined;
	const pickBaseline = (value: string) => {
		const next = value === NO_BASELINE ? "" : value;
		setBaseline(next);
		if (!next && (filter === "worse" || filter === "better")) setFilter("all");
	};

	const entryParams = {
		id,
		offset,
		limit: PAGE_SIZE,
		search: debouncedSearch || undefined,
		passed: filter === "notPassed" ? false : undefined,
		status: filter === "failed" ? "failed" : undefined,
		baseline: baseline || undefined,
		change: baseline && (filter === "worse" || filter === "better") ? filter : undefined,
	};
	const { data: entriesData, currentData: currentEntries } = useGetEvalRunEntriesQuery(entryParams);
	useGetEvalRunEntriesQuery(entryParams, { pollingInterval: active ? 2000 : 0, skip: !active });
	const entries = entriesData?.entries ?? [];
	const entryTotal = entriesData?.total ?? 0;
	const selectedEntry = entries.find((entry) => entry.id === openEntry);

	const [stopRun] = useStopEvalRunMutation();
	const [rerunRun] = useRerunEvalRunMutation();
	const [deleteRun] = useDeleteEvalRunMutation();

	const attempt = async (action: () => Promise<unknown>, done: string) => {
		try {
			await action();
			toast({ title: done });
		} catch (actionError) {
			toast({ title: tCommon("error"), description: getErrorMessage(actionError), variant: "destructive" });
		}
	};

	// The input column comes from the rows themselves. It is remembered, so a filter
	// that matches nothing does not take the column away and reshuffle the table. This
	// sits above the early returns: every hook must run on every render.
	const shownInput = Object.keys(entries[0]?.variables ?? {}).filter((name) => name !== "output" && name !== EXPECTED)[0];
	const [inputColumn, setInputColumn] = useState<string | undefined>(undefined);
	if (shownInput && shownInput !== inputColumn) setInputColumn(shownInput);
	// The expected output gets a column of its own when the dataset has one. It is
	// remembered for the same reason.
	const [expectedColumn, setExpectedColumn] = useState(false);
	if (!expectedColumn && entries.some((entry) => EXPECTED in entry.variables)) setExpectedColumn(true);

	if (isLoading) return <FullPageLoader />;
	if (showLoadError(error, data) || !run) {
		return <LoadErrorView entity={t("evals.runs.list.title")} error={error} onRetry={() => void refetch()} testId="run-load-error" />;
	}

	const passRate = runPassRate(run);
	const completed = run.counts.completed ?? 0;
	const failed = run.counts.failed ?? 0;
	const summary = run.summary;
	const number = (value: number) => value.toLocaleString(i18n.language);
	const money = (value: number) => `$${value.toFixed(value < 1 ? 4 : 2)}`;
	const summaryOf = (evaluator: RunEvaluator): RunEvaluatorSummary | undefined =>
		summary?.evaluators.find((item) => item.evaluator_id === evaluator.id);
	// Each evaluator's bars are drawn within the range of its scores on this page.
	const ranges = new Map(
		run.evaluators.map((evaluator) => [evaluator.id, scoreRange(entries.map((entry) => entryScore(entry, evaluator.id)))]),
	);
	const inputColumns = inputColumn ? [inputColumn] : [];
	const baselineSummaryOf = (evaluator: RunEvaluator): RunEvaluatorSummary | undefined =>
		baselineRun?.summary?.evaluators.find((item) => item.evaluator_id === evaluator.id);
	const filters: EntryFilter[] = baseline ? ["all", "notPassed", "failed", "worse", "better"] : ["all", "notPassed", "failed"];
	const columnCount = 3 + inputColumns.length + (expectedColumn ? 1 : 0) + run.evaluators.length;
	const filtering = filter !== "all" || search !== "";

	return (
		<div className="flex h-[calc(var(--app-content-viewport)_-_var(--app-bottom-padding))] gap-2" data-testid="run-report-page">
			<PageTitle
				breadcrumbs={[{ label: t("evals.runs.list.title"), to: "/workspace/evals/runs" }, { label: run.name }]}
				tags={[
					{
						label: t(`evals.runs.status.${run.status}`),
						tone: run.status === "completed" ? "success" : run.status === "failed" ? "danger" : "neutral",
						pulse: active,
						"data-testid": "run-status-tag",
					},
					{ label: t("evals.datasets.versions.rows", { count: run.total }) },
					{ label: t("evals.runs.report.passedCount", { count: run.passed }) },
				]}
			/>

			<section className="flex min-w-0 grow flex-col gap-3 overflow-y-auto">
				<div className="flex flex-wrap items-center justify-between gap-2">
					<span className="text-muted-foreground text-sm">
						<Badge variant="outline" className="mr-1.5 rounded-sm">
							{t(`evals.runs.targets.${run.target_type}`)}
						</Badge>
						{targetText(run)} · {datasetData?.dataset.name ?? run.dataset_id} v{run.dataset_version} ·{" "}
						{formatDuration(runDurationMs(run, Date.now()))}
					</span>
					<span className="flex flex-wrap gap-2">
						{canUpdate && active && (
							<Button
								variant="outline"
								onClick={() => void attempt(() => stopRun(id).unwrap(), t("evals.runs.report.stopped"))}
								data-testid="run-stop-btn"
							>
								<Square className="h-4 w-4" />
								{t("evals.runs.report.stop")}
							</Button>
						)}
						{canUpdate && !active && (
							<>
								<Button
									variant="outline"
									disabled={failed + (run.counts.stopped ?? 0) === 0}
									onClick={() => void attempt(() => rerunRun({ id, scope: "failed" }).unwrap(), t("evals.runs.report.rerunStarted"))}
									data-testid="run-rerun-failed-btn"
								>
									<RotateCw className="h-4 w-4" />
									{t("evals.runs.report.rerunFailed")}
								</Button>
								<Button variant="outline" onClick={() => setAddOpen(true)} data-testid="run-add-evaluators-btn">
									<Plus className="h-4 w-4" />
									{t("evals.runs.report.addEvaluators")}
								</Button>
							</>
						)}
						<DropdownMenu>
							<DropdownMenuTrigger asChild>
								<Button
									variant="outline"
									size="icon"
									className="size-7.5"
									aria-label={t("evals.runs.list.actionsFor", { name: run.name })}
									data-testid="run-menu-btn"
								>
									<MoreHorizontal className="h-4 w-4" />
								</Button>
							</DropdownMenuTrigger>
							<DropdownMenuContent align="end">
								{canUpdate && (
									<DropdownMenuItem onClick={() => setRenameOpen(true)} data-testid="run-menu-rename">
										<Pencil className="h-4 w-4" />
										{t("promptRepo.rename")}
									</DropdownMenuItem>
								)}
								<DropdownMenuItem asChild>
									<a href={runExportUrl(id)} download data-testid="run-export-btn">
										<Download className="h-4 w-4" />
										{t("evals.runs.report.exportCsv")}
									</a>
								</DropdownMenuItem>
								<DropdownMenuItem
									onClick={() => void navigator.clipboard.writeText(run.id).then(() => toast({ title: tCommon("idCopiedGeneric") }))}
									data-testid="run-menu-copy-id"
								>
									<Copy className="h-4 w-4" />
									{tCommon("copyIdGeneric")}
								</DropdownMenuItem>
								{canDelete && !active && (
									<DropdownMenuItem variant="destructive" onClick={() => setDeleteOpen(true)} data-testid="run-menu-delete">
										<Trash2 className="h-4 w-4" />
										{tCommon("delete")}
									</DropdownMenuItem>
								)}
							</DropdownMenuContent>
						</DropdownMenu>
					</span>
				</div>

				{run.error && <p className="text-destructive text-sm">{run.error}</p>}

				<div className="grid grid-cols-2 gap-2 lg:grid-cols-4">
					<Card label={t("evals.runs.list.columns.passed")} value={passRate === null ? "–" : `${passRate}%`} testId="run-card-pass-rate">
						{t("evals.runs.list.passedOf", { passed: number(run.passed), total: number(completed) })}
					</Card>
					<Card label={t("evals.runs.report.entries")} value={number(completed)}>
						{failed > 0 ? (
							<span className="text-destructive">{t("evals.runs.report.failedToRun", { count: failed })}</span>
						) : (
							`${number(run.total)}`
						)}
					</Card>
					<Card
						label={t("evals.runs.report.latency")}
						value={summary?.latency ? formatDuration(summary.latency.p50) : "–"}
						unit={summary?.latency ? "p50" : undefined}
					>
						{summary?.latency ? `p90 ${formatDuration(summary.latency.p90)} · p99 ${formatDuration(summary.latency.p99)}` : ""}
					</Card>
					<Card
						label={t("evals.runs.report.cost")}
						value={summary ? money(summary.totals.target_cost + summary.totals.evaluator_cost) : "–"}
					>
						{summary
							? t("evals.runs.report.costSplit", {
									target: money(summary.totals.target_cost),
									evaluators: money(summary.totals.evaluator_cost),
								})
							: ""}
					</Card>
				</div>

				<div className="shrink-0 overflow-x-auto rounded-sm border">
					<Table data-testid="run-evaluators-table">
						<TableHeader>
							<TableRow>
								<TableHead>{t("evals.runs.report.evaluator")}</TableHead>
								<TableHead>{t("evals.runs.report.passRate")}</TableHead>
								<TableHead>{t("evals.runs.report.mean")}</TableHead>
								<TableHead>{t("evals.runs.report.median")}</TableHead>
								<TableHead>{t("evals.runs.report.notScored")}</TableHead>
								<TableHead>{t("evals.runs.report.runVerdict")}</TableHead>
							</TableRow>
						</TableHeader>
						<TableBody>
							{run.evaluators.map((evaluator) => {
								const item = summaryOf(evaluator);
								const notScored = [
									item?.errors ? t("evals.runs.report.errorsCount", { count: item.errors }) : "",
									item?.na ? t("evals.runs.report.naCount", { count: item.na }) : "",
									item?.pending ? t("evals.runs.report.pendingCount", { count: item.pending }) : "",
								].filter(Boolean);
								return (
									<TableRow key={evaluator.id} data-testid={`run-evaluator-row-${evaluator.name}`}>
										<TableCell>
											<span className="font-medium">{evaluator.name}</span>{" "}
											<Badge variant="outline" className="rounded-sm">
												{evaluator.type}
											</Badge>
										</TableCell>
										<TableCell className="font-mono">{item && item.scored > 0 ? `${Math.round(item.pass_rate)}%` : "–"}</TableCell>
										<TableCell className="font-mono">{item?.mean !== undefined ? Math.round(item.mean * 100) / 100 : "–"}</TableCell>
										<TableCell className="font-mono">{item?.median !== undefined ? Math.round(item.median * 100) / 100 : "–"}</TableCell>
										<TableCell className="text-muted-foreground">{notScored.join(" · ") || "–"}</TableCell>
										<TableCell>
											{item?.run_pass === undefined ? (
												<span className="text-muted-foreground">–</span>
											) : (
												<span className={cn("rounded-sm px-1.5 py-0.5 font-medium", toneClass[item.run_pass ? "pass" : "fail"])}>
													{item.run_pass ? t("evals.runs.report.pass") : t("evals.runs.report.fail")}
												</span>
											)}
										</TableCell>
									</TableRow>
								);
							})}
						</TableBody>
					</Table>
				</div>

				<div className="flex flex-wrap items-center justify-end gap-2">
					{baselineOptions.length > 0 && (
						<div className="mr-auto flex items-center gap-2">
							<span className="text-muted-foreground text-sm" id="run-baseline-label">
								{t("evals.runs.report.compareWith")}
							</span>
							<Select value={baseline || NO_BASELINE} onValueChange={pickBaseline}>
								<SelectTrigger className="h-8 w-64" aria-labelledby="run-baseline-label" data-testid="run-baseline-select">
									<SelectValue />
								</SelectTrigger>
								<SelectContent>
									<SelectItem value={NO_BASELINE}>{t("evals.runs.report.noBaseline")}</SelectItem>
									{baselineOptions.map((other) => (
										<SelectItem key={other.id} value={other.id}>
											{other.name}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
						</div>
					)}
					<div className="relative">
						<Search className="text-muted-foreground absolute top-1/2 left-2.5 h-4 w-4 -translate-y-1/2" />
						<Input
							className="h-8 w-60 pl-8"
							value={search}
							placeholder={t("evals.runs.report.searchEntries")}
							aria-label={t("evals.runs.report.searchEntries")}
							onChange={(event) => setSearch(event.target.value)}
							data-testid="run-entries-search-input"
						/>
					</div>
					<Tabs value={filter} onValueChange={(value) => setFilter(value as EntryFilter)}>
						<TabsList className="h-8 rounded-sm">
							{filters.map((item) => (
								<TabsTrigger key={item} value={item} className="flex-none rounded-sm px-3" data-testid={`run-entries-filter-${item}`}>
									{t(`evals.runs.report.filters.${item}`)}
								</TabsTrigger>
							))}
						</TabsList>
					</Tabs>
				</div>

				<div className="shrink-0 overflow-x-auto rounded-sm border">
					<Table data-testid="run-entries-table">
						<TableHeader>
							<TableRow>
								<TableHead className="w-12">#</TableHead>
								{inputColumns.map((name) => (
									<TableHead key={name} className="w-1/4">
										{name}
									</TableHead>
								))}
								<TableHead>{t("evals.runs.report.output")}</TableHead>
								{expectedColumn && <TableHead className="w-1/6">{t("evals.runs.report.expected")}</TableHead>}
								{run.evaluators.map((evaluator) => {
									const item = summaryOf(evaluator);
									const before = baselineSummaryOf(evaluator);
									const change = changes?.[evaluator.id];
									const moved = item?.mean !== undefined && before?.mean !== undefined ? item.mean - before.mean : null;
									return (
										<TableHead key={evaluator.id} className="w-48 py-2 align-top" data-testid={`run-entries-head-${evaluator.name}`}>
											<div>{evaluator.name}</div>
											{/* The column's average sits in its header, as the comparison shows it. */}
											<div className="text-foreground font-mono text-base font-semibold">
												{item?.mean !== undefined ? Math.round(item.mean * 100) / 100 : "–"}{" "}
												<span className="text-muted-foreground text-xs font-normal">{t("evals.runs.compare.avg")}</span>
											</div>
											{baseline && (
												<div
													className="flex flex-wrap items-center gap-x-2 font-mono text-xs font-normal"
													data-testid={`run-entries-head-baseline-${evaluator.name}`}
												>
													{/* The baseline's average, then how far this run moved from it. */}
													<span className="text-muted-foreground">
														{before?.mean !== undefined ? Math.round(before.mean * 100) / 100 : "–"} {t("evals.runs.compare.baseline")}
													</span>
													{moved !== null && moved !== 0 && (
														<span className={moved > 0 ? "text-emerald-700 dark:text-emerald-400" : "text-red-700 dark:text-red-400"}>
															{signed(moved)}
														</span>
													)}
													{/* The counts are over every row of the run, and open the rows they count. */}
													{change && (
														<span className="flex items-center gap-1.5">
															<button
																type="button"
																className="flex items-center gap-0.5 text-emerald-700 hover:underline dark:text-emerald-400"
																title={t("evals.runs.compare.improved", { count: change.improved })}
																aria-label={t("evals.runs.compare.improved", { count: change.improved })}
																onClick={() => setFilter("better")}
																data-testid={`run-entries-improved-${evaluator.name}`}
															>
																<TrendingUp className="h-3 w-3" />
																{change.improved}
															</button>
															<button
																type="button"
																className="flex items-center gap-0.5 text-red-700 hover:underline dark:text-red-400"
																title={t("evals.runs.compare.regressed", { count: change.regressed })}
																aria-label={t("evals.runs.compare.regressed", { count: change.regressed })}
																onClick={() => setFilter("worse")}
																data-testid={`run-entries-regressed-${evaluator.name}`}
															>
																<TrendingDown className="h-3 w-3" />
																{change.regressed}
															</button>
														</span>
													)}
												</div>
											)}
										</TableHead>
									);
								})}
								<TableHead className="w-28">{t("evals.runs.report.latency")}</TableHead>
							</TableRow>
						</TableHeader>
						<TableBody className={currentEntries ? undefined : "opacity-60"}>
							{entries.length === 0 && (
								<TableRow>
									<TableCell colSpan={columnCount} className="h-32 text-center">
										<div className="flex flex-col items-center gap-2" data-testid="run-entries-empty">
											<span className="text-muted-foreground">{t("evals.runs.report.noEntries")}</span>
											{filtering && (
												<Button
													variant="outline"
													onClick={() => {
														setFilter("all");
														setSearch("");
													}}
													data-testid="run-entries-reset-btn"
												>
													{t("evals.datasets.list.resetFilters")}
												</Button>
											)}
										</div>
									</TableCell>
								</TableRow>
							)}
							{entries.map((entry, index) => (
								<TableRow
									key={entry.id}
									data-state={entry.id === openEntry ? "selected" : undefined}
									className="cursor-pointer"
									onClick={() => setOpenEntry(entry.id)}
									data-testid={`run-entry-row-${offset + index + 1}`}
								>
									<TableCell className="text-muted-foreground font-mono">{offset + index + 1}</TableCell>
									{inputColumns.map((name) => (
										<TableCell key={name} className="max-w-64 truncate">
											{entry.variables[name]}
										</TableCell>
									))}
									<TableCell className={cn("max-w-72", entry.status === "failed" && "text-destructive")}>
										<div className="truncate">{entry.status === "failed" ? entry.error : entry.output}</div>
										{/* With a baseline, its output sits under this run's when the two differ. */}
										{entry.baseline && entry.baseline.output !== entry.output && (
											<div className="text-muted-foreground flex items-center gap-1.5 truncate" data-testid="run-entry-baseline-output">
												<span className="bg-muted-foreground/60 size-1.5 shrink-0 rounded-full" aria-hidden />
												<span className="truncate">
													{entry.baseline.status === "failed" ? entry.baseline.error : entry.baseline.output}
												</span>
											</div>
										)}
									</TableCell>
									{expectedColumn && <TableCell className="text-muted-foreground max-w-56 truncate">{entry.variables[EXPECTED]}</TableCell>}
									{run.evaluators.map((evaluator) => (
										<ResultCell
											key={evaluator.id}
											result={resultOf(entry, evaluator.id)}
											baseline={entry.baseline ? resultOf(entry.baseline, evaluator.id) : undefined}
											delta={scoreDelta(entry, entry.baseline, evaluator.id)}
											share={barShare(entryScore(entry, evaluator.id), ranges.get(evaluator.id) ?? { min: 0, max: 1 })}
										/>
									))}
									<TableCell className="text-muted-foreground font-mono">{formatDuration(entry.target_latency_ms)}</TableCell>
								</TableRow>
							))}
						</TableBody>
					</Table>
				</div>

				<RunsPagination offset={offset} limit={PAGE_SIZE} total={entryTotal} onOffsetChange={setOffset} testId="run-entries" />
			</section>

			<EntryPanel
				run={run}
				entry={selectedEntry}
				number={selectedEntry ? entries.indexOf(selectedEntry) + offset + 1 : 0}
				canUpdate={canUpdate && !active}
				onClose={() => setOpenEntry("")}
				onRerun={(entryId) => void attempt(() => rerunRun({ id, entryIds: [entryId] }).unwrap(), t("evals.runs.report.rerunStarted"))}
			/>
			<AddEvaluatorsDialog run={run} open={addOpen} onOpenChange={setAddOpen} />
			<RenameRunDialog run={run} open={renameOpen} onOpenChange={setRenameOpen} />
			<AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>{t("evals.datasets.list.deleteTitle", { name: run.name })}</AlertDialogTitle>
						<AlertDialogDescription>{t("evals.runs.list.deleteDescription")}</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel>{tCommon("cancel")}</AlertDialogCancel>
						<AlertDialogAction
							onClick={() =>
								void deleteRun(id)
									.unwrap()
									.then(() => {
										toast({ title: t("evals.datasets.list.deleted", { name: run.name }) });
										onDeleted();
									})
									.catch((deleteError) =>
										toast({ title: tCommon("error"), description: getErrorMessage(deleteError), variant: "destructive" }),
									)
							}
							data-testid="run-delete-confirm-btn"
						>
							{tCommon("delete")}
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</div>
	);
}

function Card({
	label,
	value,
	unit,
	children,
	testId,
}: {
	label: string;
	value: string;
	unit?: string;
	children?: React.ReactNode;
	testId?: string;
}) {
	return (
		<div className="rounded-sm border px-3 py-2" data-testid={testId}>
			<div className="text-muted-foreground text-xs">{label}</div>
			<div className="font-mono text-xl font-semibold">
				{value} {unit && <span className="text-muted-foreground text-xs font-normal">{unit}</span>}
			</div>
			<div className="text-muted-foreground min-h-4 text-xs">{children}</div>
		</div>
	);
}

// resultLabel is the text of a result's cell: its score and verdict, or its state.
function useResultLabel() {
	const { t } = useTranslation("config");
	return (result: RunResult | undefined): string => {
		if (!result) return t("evals.runs.report.notRun");
		if (result.status === "awaiting_review") return t("evals.runs.report.waiting");
		if (result.status === "not_applicable") return t("evals.runs.report.notApplicable");
		if (result.status === "failed") return t("evals.runs.report.fail");
		if (result.status !== "completed") return t(`evals.runs.status.${result.status === "queued" ? "queued" : "running"}`);
		const verdict = result.pass === undefined ? "" : ` · ${result.pass ? t("evals.runs.report.pass") : t("evals.runs.report.fail")}`;
		return `${formatScore(result.score)}${verdict}`;
	};
}

// ResultCell is one evaluator's score for one entry, tinted by its state: green for
// pass, red for fail, yellow for waiting or not applicable. A thin bar under the
// value shows the score against the others on the page.
interface ResultCellProps {
	result: RunResult | undefined;
	share: number;
	// The baseline run's result for the same row and evaluator, when there is one.
	baseline?: RunResult;
	// How far the score moved from the baseline's, or null when it cannot be told.
	delta?: number | null;
}

function ResultCell({ result, share, baseline, delta }: ResultCellProps) {
	const label = useResultLabel();
	const tone = resultTone(result);
	return (
		<TableCell className={cn("font-mono whitespace-nowrap", toneClass[tone], tone === "none" && "text-muted-foreground")} data-tone={tone}>
			{label(result)}
			{result?.score && (
				<div className="mt-1 h-0.5 w-24 rounded-full bg-current/15" aria-hidden>
					<div className="h-0.5 rounded-full bg-current" style={{ width: `${share}%` }} />
				</div>
			)}
			{baseline && (
				<div className="text-muted-foreground mt-1 flex items-center gap-1.5 text-xs" data-testid="run-entry-baseline-score">
					<span className="bg-muted-foreground/60 size-1.5 shrink-0 rounded-full" aria-hidden />
					{label(baseline)}
					{delta !== null && delta !== undefined && delta !== 0 && (
						<span className={cn("rounded-sm px-1", toneClass[delta > 0 ? "pass" : "fail"])}>{signed(delta)}</span>
					)}
				</div>
			)}
		</TableCell>
	);
}

interface EntryPanelProps {
	run: Run;
	// The entry to show, or undefined while the sheet is closed.
	entry: RunEntry | undefined;
	number: number;
	canUpdate: boolean;
	onClose: () => void;
	onRerun: (entryId: string) => void;
}

// EntryPanel shows one entry in full, in a sheet over the report: the row's variables,
// the output, and each evaluator's score with its reasoning. A human evaluator is
// scored here. It is closed while there is no entry.
function EntryPanel({ run, entry, number, canUpdate, onClose, onRerun }: EntryPanelProps) {
	return (
		<Sheet open={Boolean(entry)} onOpenChange={(open) => !open && onClose()}>
			<SheetContent className="p-0 pt-4 sm:max-w-xl" data-testid="run-entry-panel">
				{entry && <EntryDetail run={run} entry={entry} number={number} canUpdate={canUpdate} onRerun={onRerun} />}
			</SheetContent>
		</Sheet>
	);
}

function EntryDetail({ run, entry, number, canUpdate, onRerun }: Omit<EntryPanelProps, "entry" | "onClose"> & { entry: RunEntry }) {
	const { t, i18n } = useTranslation("config");
	const label = useResultLabel();
	const names = Object.keys(entry.variables)
		.filter((name) => name !== "output")
		.sort();
	const failed = entry.status === "failed";
	const outputFacts = [
		formatDuration(entry.target_latency_ms),
		t("evals.runs.report.tokens", { count: entry.target_total_tokens }),
		entry.target_cost ? `$${entry.target_cost.toFixed(6)}` : "",
	].filter(Boolean);
	return (
		<>
			<SheetHeader className="flex flex-col items-start px-0 py-4" headerClassName="mb-0 sticky -top-4 bg-card z-10 px-4 md:px-8">
				<SheetTitle className="flex flex-wrap items-center gap-2">
					{t("evals.runs.report.row", { number })}
					<Badge variant={failed ? "destructive" : "secondary"} className="rounded-sm font-normal" data-testid="run-entry-status">
						{t(`evals.runs.entryStatus.${entry.status}`)}
					</Badge>
					{entry.passed !== undefined && entry.passed !== null && (
						<span
							className={cn("rounded-sm px-1.5 py-0.5 text-xs font-medium", toneClass[entry.passed ? "pass" : "fail"])}
							data-testid="run-entry-verdict"
						>
							{entry.passed ? t("evals.runs.report.pass") : t("evals.runs.report.fail")}
						</span>
					)}
				</SheetTitle>
				<SheetDescription>{t("evals.runs.report.attempt", { number: entry.attempt })}</SheetDescription>
			</SheetHeader>
			<div className="flex flex-1 flex-col gap-6 overflow-y-auto px-4 pt-2 pb-6 text-sm md:px-8">
				<div className="flex flex-col gap-4">
					{names.map((name) => (
						<Field key={name} label={name} value={entry.variables[name]} mono />
					))}
					<Field
						label={t("evals.runs.report.output")}
						facts={outputFacts.join(" · ")}
						value={failed ? (entry.error ?? "") : entry.output}
						destructive={failed}
					/>
					{entry.baseline && (
						<Field
							label={`${t("evals.runs.report.output")} · ${t("evals.runs.compare.baseline")}`}
							value={entry.baseline.status === "failed" ? (entry.baseline.error ?? "") : entry.baseline.output}
							destructive={entry.baseline.status === "failed"}
						/>
					)}
					{entry.tool_calls && <Field label="tool_calls" value={entry.tool_calls} mono code />}
				</div>

				<div className="flex flex-col gap-2">
					<h3 className="text-sm font-medium">{t("evals.runs.form.evaluators")}</h3>
					{run.evaluators.map((evaluator) => {
						const result = resultOf(entry, evaluator.id);
						const tone = resultTone(result);
						const detail = result?.error || result?.reasoning;
						const facts = result
							? [
									result.latency_ms ? formatDuration(result.latency_ms) : "",
									result.total_tokens ? t("evals.runs.report.tokens", { count: result.total_tokens }) : "",
									result.cost ? `$${result.cost.toFixed(6)}` : "",
								].filter(Boolean)
							: [];
						return (
							<div key={evaluator.id} className="rounded-sm border" data-testid={`run-entry-result-${evaluator.name}`}>
								<div className="bg-muted/30 flex flex-wrap items-center justify-between gap-2 px-3 py-2">
									<span className="flex min-w-0 items-center gap-2">
										<span className="truncate font-medium">{evaluator.name}</span>
										<Badge variant="outline" className="rounded-sm font-normal">
											{evaluator.type}
										</Badge>
									</span>
									<span className={cn("rounded-sm px-1.5 py-0.5 font-mono text-xs", toneClass[tone])}>{label(result)}</span>
								</div>
								{(detail || facts.length > 0 || (evaluator.type === "human" && result && canUpdate)) && (
									<div className="flex flex-col gap-2 border-t px-3 py-2.5">
										{detail && (
											<p className={cn("leading-relaxed break-words whitespace-pre-wrap", result?.error && "text-destructive")}>{detail}</p>
										)}
										{facts.length > 0 && <p className="text-muted-foreground font-mono text-xs">{facts.join(" · ")}</p>}
										{evaluator.type === "human" && result && canUpdate && (
											<ReviewForm key={result.id} runId={run.id} entryId={entry.id} evaluatorId={evaluator.id} language={i18n.language} />
										)}
									</div>
								)}
							</div>
						);
					})}
				</div>
			</div>
			{canUpdate && (
				<SheetFooter className="bg-card sticky bottom-0 flex-row justify-end gap-2 border-t px-4 py-4 md:px-8">
					<Button variant="outline" onClick={() => onRerun(entry.id)} data-testid="run-entry-rerun-btn">
						<RotateCw className="h-4 w-4" />
						{t("evals.runs.report.rerunRow")}
					</Button>
				</SheetFooter>
			)}
		</>
	);
}

// Field shows one value of an entry in a box, under its name. Long values scroll
// inside the box, so one large cell does not push the scores off the screen.
function Field({
	label,
	value,
	facts,
	mono,
	code,
	destructive,
}: {
	label: string;
	value: string;
	// Small print beside the label, such as how long the call took.
	facts?: string;
	// The label is a variable name.
	mono?: boolean;
	// The value is JSON or code.
	code?: boolean;
	destructive?: boolean;
}) {
	return (
		<div className="flex flex-col gap-1.5">
			<div className="flex flex-wrap items-baseline justify-between gap-2">
				<span className={cn("text-xs font-medium", mono && "font-mono")}>{label}</span>
				{facts && <span className="text-muted-foreground font-mono text-xs">{facts}</span>}
			</div>
			<div
				className={cn(
					"bg-muted/40 max-h-72 overflow-y-auto rounded-sm border px-3 py-2 leading-relaxed break-words whitespace-pre-wrap",
					code && "font-mono text-xs",
					destructive && "text-destructive",
					!value && "text-muted-foreground",
				)}
			>
				{value || "–"}
			</div>
		</div>
	);
}

// ReviewForm takes a reviewer's score for a human evaluator: yes or no, or any other
// value typed in, which the server checks against the evaluator's type.
function ReviewForm({ runId, entryId, evaluatorId }: { runId: string; entryId: string; evaluatorId: string; language: string }) {
	const { t } = useTranslation("config");
	const { t: tCommon } = useTranslation("common");
	const { toast } = useToast();
	const [review, { isLoading }] = useReviewEvalRunResultMutation();
	const [value, setValue] = useState("");
	const [note, setNote] = useState("");

	const send = async (score: boolean | number | string) => {
		try {
			await review({ id: runId, entryId, evaluatorId, score, reasoning: note || undefined }).unwrap();
			toast({ title: t("evals.runs.report.reviewed") });
		} catch (error) {
			toast({ title: tCommon("error"), description: getErrorMessage(error), variant: "destructive" });
		}
	};
	// A typed value that reads as a number is sent as one; anything else as text.
	const typed = () => (value.trim() !== "" && !Number.isNaN(Number(value)) ? Number(value) : value.trim());

	return (
		<div className="mt-1 flex flex-col gap-2" data-testid="run-review-form">
			<div role="group" aria-label={t("evals.runs.report.yourScore")} className="flex flex-wrap items-center gap-2">
				<Button variant="outline" disabled={isLoading} onClick={() => void send(true)} data-testid="run-review-yes-btn">
					{t("evals.runs.report.yes")}
				</Button>
				<Button variant="outline" disabled={isLoading} onClick={() => void send(false)} data-testid="run-review-no-btn">
					{t("evals.runs.report.no")}
				</Button>
				<Input
					className="h-7.5 w-28"
					value={value}
					placeholder={t("evals.runs.report.scorePlaceholder")}
					aria-label={t("evals.runs.report.scorePlaceholder")}
					onChange={(event) => setValue(event.target.value)}
					data-testid="run-review-score-input"
				/>
				<Button
					variant="outline"
					disabled={isLoading || value.trim() === ""}
					onClick={() => void send(typed())}
					data-testid="run-review-save-btn"
				>
					{tCommon("save")}
				</Button>
			</div>
			<Input
				className="h-7.5"
				value={note}
				placeholder={t("evals.runs.report.note")}
				aria-label={t("evals.runs.report.note")}
				onChange={(event) => setNote(event.target.value)}
				data-testid="run-review-note-input"
			/>
		</div>
	);
}

// AddEvaluatorsDialog scores a finished run's stored outputs with more evaluators.
function AddEvaluatorsDialog({ run, open, onOpenChange }: { run: Run; open: boolean; onOpenChange: (open: boolean) => void }) {
	const { t } = useTranslation("config");
	const { t: tCommon } = useTranslation("common");
	const { toast } = useToast();
	const { data } = useGetEvaluatorsQuery({ limit: 200 }, { skip: !open });
	const [addEvaluators, { isLoading }] = useAddEvalRunEvaluatorsMutation();
	const [picked, setPicked] = useState<string[]>([]);
	useEffect(() => {
		if (open) setPicked([]);
	}, [open]);
	const taken = new Set(run.evaluators.map((evaluator) => evaluator.id));
	const available = (data?.evaluators ?? []).filter((evaluator) => !taken.has(evaluator.id));

	const submit = async () => {
		try {
			await addEvaluators({ id: run.id, evaluators: picked.map((evaluatorId) => ({ id: evaluatorId })) }).unwrap();
			onOpenChange(false);
		} catch (error) {
			toast({ title: tCommon("error"), description: getErrorMessage(error), variant: "destructive" });
		}
	};

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent className="max-w-md" data-testid="run-add-evaluators-dialog">
				<DialogHeader>
					<DialogTitle>{t("evals.runs.report.addEvaluators")}</DialogTitle>
				</DialogHeader>
				{available.length === 0 ? (
					<p className="text-muted-foreground text-sm">{t("evals.runs.form.noEvaluators")}</p>
				) : (
					<ul className="max-h-72 overflow-y-auto rounded-sm border">
						{available.map((evaluator) => (
							<li key={evaluator.id} className="border-b last:border-b-0">
								<label className="flex cursor-pointer items-center gap-2 px-3 py-2 text-sm">
									<Checkbox
										checked={picked.includes(evaluator.id)}
										onCheckedChange={(checked) =>
											setPicked((current) => (checked ? [...current, evaluator.id] : current.filter((item) => item !== evaluator.id)))
										}
										data-testid={`run-add-evaluator-${evaluator.name}`}
									/>
									<span className="font-medium">{evaluator.name}</span>
									<Badge variant="outline" className="rounded-sm">
										{evaluator.type}
									</Badge>
								</label>
							</li>
						))}
					</ul>
				)}
				<DialogFooter>
					<Button variant="outline" onClick={() => onOpenChange(false)}>
						{tCommon("cancel")}
					</Button>
					<Button disabled={picked.length === 0 || isLoading} onClick={() => void submit()} data-testid="run-add-evaluators-submit-btn">
						{t("evals.runs.report.addEvaluators")}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
// RenameRunDialog changes a run's name. Its tags are kept.
function RenameRunDialog({ run, open, onOpenChange }: { run: Run; open: boolean; onOpenChange: (open: boolean) => void }) {
	const { t } = useTranslation("config");
	const { t: tCommon } = useTranslation("common");
	const { toast } = useToast();
	const [updateRun, { isLoading }] = useUpdateEvalRunMutation();
	const [name, setName] = useState(run.name);
	useEffect(() => {
		if (open) setName(run.name);
	}, [open, run.name]);

	const submit = async () => {
		try {
			await updateRun({ id: run.id, name: name.trim(), tags: run.tags }).unwrap();
			onOpenChange(false);
		} catch (error) {
			toast({ title: tCommon("error"), description: getErrorMessage(error), variant: "destructive" });
		}
	};

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent className="max-w-md" data-testid="run-rename-dialog">
				<DialogHeader>
					<DialogTitle>{t("promptRepo.rename")}</DialogTitle>
				</DialogHeader>
				<div className="flex flex-col gap-2">
					<Label htmlFor="run-rename">{t("evals.datasets.form.name")}</Label>
					<Input id="run-rename" value={name} onChange={(event) => setName(event.target.value)} data-testid="run-rename-input" />
				</div>
				<DialogFooter>
					<Button variant="outline" onClick={() => onOpenChange(false)}>
						{tCommon("cancel")}
					</Button>
					<Button disabled={name.trim() === "" || isLoading} onClick={() => void submit()} data-testid="run-rename-submit-btn">
						{tCommon("save")}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}