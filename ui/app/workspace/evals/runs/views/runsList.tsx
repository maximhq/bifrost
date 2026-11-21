import PageTitle from "@/components/pageTitle";
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
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useToast } from "@/hooks/use-toast";
import { useDebouncedValue } from "@/hooks/useDebounce";
import { getLocale } from "@/lib/i18n";
import { dateFnsLocale } from "@/lib/i18n/dateLocale";
import {
	getErrorMessage,
	useDeleteEvalRunMutation,
	useGetEvalDatasetsQuery,
	useGetEvalRunsQuery,
	useRerunEvalRunMutation,
} from "@/lib/store";
import type { Run, RunStatus } from "@/lib/types/evalRuns";
import { formatDistanceToNow } from "date-fns";
import { Columns2, Download, FlaskConical, MoreHorizontal, Plus, RotateCw, Search, Trash2 } from "lucide-react";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { RunsPagination } from "./runsPagination";
import { NewRunSheet } from "./newRunSheet";
import { RunStatusLabel } from "./runStatus";
import {
	formatDuration,
	isRunActive,
	MAX_COMPARED_RUNS,
	runDurationMs,
	runExportUrl,
	runPassRate,
	runProgress,
	runTrend,
	toggleSelected,
} from "./runUtils";
import { RunsTrend, trendColor } from "./runsTrend";

const PAGE_SIZE = 25;
// How many evaluators get their own column and their own line on the trend chart.
const MAX_EVALUATOR_COLUMNS = 4;
const ANY_STATUS = "__any__";
const STATUSES: RunStatus[] = ["running", "completed", "failed", "stopped", "queued"];

interface RunsListProps {
	canCreate: boolean;
	canUpdate: boolean;
	canDelete: boolean;
	onOpen: (id: string) => void;
	onCompare: (ids: string[]) => void;
}

// targetText names what a run tests, in a few words.
export function targetText(run: Run, promptNames?: Map<string, string>): string {
	const target = run.target;
	switch (run.target_type) {
		case "prompt":
			return `${promptNames?.get(target.prompt_id ?? "") ?? target.model ?? ""} v${target.prompt_version ?? ""}`;
		case "model":
			return `${target.provider ?? ""} / ${target.model ?? ""}`;
		case "http":
			return `${target.http?.method ?? ""} ${target.http?.url ?? ""}`;
		default:
			return "";
	}
}

// RunsList is the table of test runs: status with progress, rows passed, and a
// selection to compare runs with.
export function RunsList({ canCreate, canUpdate, canDelete, onOpen, onCompare }: RunsListProps) {
	const { t } = useTranslation("config");
	const { t: tCommon } = useTranslation("common");
	const { toast } = useToast();
	const [search, setSearch] = useState("");
	const [status, setStatus] = useState(ANY_STATUS);
	const [offset, setOffset] = useState(0);
	const [selected, setSelected] = useState<string[]>([]);
	const [formOpen, setFormOpen] = useState(false);
	const [toDelete, setToDelete] = useState<Run | null>(null);
	const debouncedSearch = useDebouncedValue(search, 300);

	const params = { limit: PAGE_SIZE, offset, search: debouncedSearch || undefined, status: status === ANY_STATUS ? undefined : status };
	const { data, currentData } = useGetEvalRunsQuery(params);
	const runs = useMemo(() => data?.runs ?? [], [data]);
	// The list keeps itself fresh while a run on it is still going.
	const active = runs.some((run) => isRunActive(run.status));
	useGetEvalRunsQuery(params, { pollingInterval: active ? 2000 : 0, skip: !active });
	const { data: datasetsData } = useGetEvalDatasetsQuery({ limit: 200 });
	const datasetNames = useMemo(() => new Map((datasetsData?.datasets ?? []).map((dataset) => [dataset.id, dataset.name])), [datasetsData]);
	const [deleteRun] = useDeleteEvalRunMutation();
	const [rerunRun] = useRerunEvalRunMutation();
	const total = data?.total ?? 0;
	const trend = useMemo(() => runTrend(runs, MAX_EVALUATOR_COLUMNS), [runs]);
	const filtered = Boolean(debouncedSearch) || status !== ANY_STATUS;
	const now = Date.now();

	const attempt = async (action: () => Promise<unknown>, done: string) => {
		try {
			await action();
			toast({ title: done });
		} catch (error) {
			toast({ title: tCommon("error"), description: getErrorMessage(error), variant: "destructive" });
		}
	};

	const sheet = <NewRunSheet open={formOpen} onOpenChange={setFormOpen} onCreated={(run) => onOpen(run.id)} />;

	if (data && total === 0 && !filtered) {
		return (
			<div className="flex min-h-[80vh] w-full flex-col items-center justify-center gap-4 py-16 text-center" data-testid="runs-empty-state">
				<PageTitle title={t("evals.runs.list.title")} />
				<FlaskConical className="text-muted-foreground h-[5.5rem] w-[5.5rem]" strokeWidth={1} />
				<h1 className="text-muted-foreground text-xl font-medium">{t("evals.runs.list.emptyTitle")}</h1>
				<p className="text-muted-foreground mx-auto max-w-[600px] text-sm">{t("evals.runs.list.emptyDescription")}</p>
				{canCreate && (
					<Button onClick={() => setFormOpen(true)} data-testid="runs-empty-create-btn">
						<Plus className="h-4 w-4" />
						{t("evals.runs.list.newRun")}
					</Button>
				)}
				{sheet}
			</div>
		);
	}

	return (
		<div className="flex h-[calc(var(--app-content-viewport)_-_var(--app-bottom-padding))] flex-col gap-3">
			<PageTitle title={t("evals.runs.list.title")}>{t("evals.runs.list.description")}</PageTitle>
			{sheet}

			<div className="flex flex-wrap items-center gap-2">
				<div className="relative">
					<Search className="text-muted-foreground absolute top-1/2 left-2.5 h-4 w-4 -translate-y-1/2" />
					<Input
						className="h-7.5 w-64 pl-8"
						value={search}
						placeholder={t("evals.runs.list.searchPlaceholder")}
						aria-label={t("evals.runs.list.searchPlaceholder")}
						onChange={(event) => {
							setSearch(event.target.value);
							setOffset(0);
						}}
						data-testid="runs-search-input"
					/>
				</div>
				<Select
					value={status}
					onValueChange={(value) => {
						setStatus(value);
						setOffset(0);
					}}
				>
					<SelectTrigger className="h-7.5 w-40" aria-label={t("evals.runs.list.columns.status")} data-testid="runs-status-filter">
						<SelectValue />
					</SelectTrigger>
					<SelectContent>
						<SelectItem value={ANY_STATUS}>{t("evals.runs.list.anyStatus")}</SelectItem>
						{STATUSES.map((item) => (
							<SelectItem key={item} value={item}>
								{t(`evals.runs.status.${item}`)}
							</SelectItem>
						))}
					</SelectContent>
				</Select>
				<span className="grow" />
				{selected.length > 0 && (
					<span className="text-muted-foreground text-sm" data-testid="runs-selected-count">
						{t("evals.runs.list.selected", { count: selected.length })}
					</span>
				)}
				<Button variant="outline" disabled={selected.length < 2} onClick={() => onCompare(selected)} data-testid="runs-compare-btn">
					<Columns2 className="h-4 w-4" />
					{t("evals.runs.list.compare")}
				</Button>
				{canCreate && (
					<Button onClick={() => setFormOpen(true)} data-testid="runs-create-btn">
						<Plus className="h-4 w-4" />
						{t("evals.runs.list.newRun")}
					</Button>
				)}
			</div>

			{trend.points.length >= 2 && <RunsTrend points={trend.points} series={trend.series} />}

			<div className="min-h-0 grow overflow-auto rounded-sm border">
				<Table data-testid="runs-table">
					<TableHeader>
						<TableRow>
							<TableHead className="w-10" />
							<TableHead>{t("evals.runs.list.columns.run")}</TableHead>
							<TableHead>{t("evals.runs.list.columns.target")}</TableHead>
							<TableHead>{t("evals.runs.list.columns.dataset")}</TableHead>
							<TableHead>{t("evals.runs.list.columns.status")}</TableHead>
							<TableHead>{t("evals.runs.list.columns.passed")}</TableHead>
							{trend.series.map((series, index) => (
								<TableHead key={series.id} className="py-2" data-testid={`runs-evaluator-column-${series.name}`}>
									<span className="flex items-center gap-1.5">
										<span className="h-2 w-2 shrink-0 rounded-full" style={{ background: trendColor(index + 1) }} />
										<span className="max-w-32 truncate">{series.name}</span>
									</span>
									<span className="text-muted-foreground font-mono text-xs font-normal">
										{Math.round(series.average)}% {t("evals.runs.compare.avg")}
									</span>
								</TableHead>
							))}
							<TableHead>{t("evals.runs.list.columns.started")}</TableHead>
							<TableHead>{t("evals.runs.list.columns.duration")}</TableHead>
							<TableHead className="w-12" />
						</TableRow>
					</TableHeader>
					<TableBody className={currentData ? undefined : "opacity-60"}>
						{runs.length === 0 && (
							<TableRow>
								<TableCell colSpan={9 + trend.series.length} className="text-muted-foreground h-24 text-center">
									{t("evals.runs.list.noResults")}
								</TableCell>
							</TableRow>
						)}
						{runs.map((run) => {
							const progress = runProgress(run);
							const passRate = runPassRate(run);
							return (
								<TableRow key={run.id} data-state={selected.includes(run.id) ? "selected" : undefined} data-testid={`run-row-${run.name}`}>
									<TableCell>
										<Checkbox
											checked={selected.includes(run.id)}
											onCheckedChange={() => setSelected((current) => toggleSelected(current, run.id, MAX_COMPARED_RUNS))}
											aria-label={t("evals.runs.list.selectRun", { name: run.name })}
											data-testid={`run-select-${run.name}`}
										/>
									</TableCell>
									<TableCell>
										<button
											type="button"
											className="font-medium hover:underline"
											onClick={() => onOpen(run.id)}
											data-testid={`run-link-${run.name}`}
										>
											{run.name}
										</button>
										<div className="text-muted-foreground text-xs">{t("evals.runs.list.evaluators", { count: run.evaluators.length })}</div>
									</TableCell>
									<TableCell>
										<Badge variant="outline" className="mr-1.5 rounded-sm">
											{t(`evals.runs.targets.${run.target_type}`)}
										</Badge>
										<span className="text-sm">{targetText(run)}</span>
									</TableCell>
									<TableCell>
										{datasetNames.get(run.dataset_id) ?? run.dataset_id}{" "}
										<span className="text-muted-foreground">v{run.dataset_version}</span>
									</TableCell>
									<TableCell>
										<RunStatusLabel status={run.status} />
										{isRunActive(run.status) && (
											<>
												<div
													role="progressbar"
													aria-valuenow={progress.percent}
													aria-valuemin={0}
													aria-valuemax={100}
													className="bg-muted mt-1 h-1 w-36 rounded-full"
												>
													<div className="h-1 rounded-full bg-blue-600" style={{ width: `${progress.percent}%` }} />
												</div>
												<div className="text-muted-foreground font-mono text-xs">
													{t("evals.runs.list.progress", { done: progress.done, total: progress.total })}
												</div>
											</>
										)}
										{run.error && <div className="text-muted-foreground max-w-56 truncate text-xs">{run.error}</div>}
									</TableCell>
									<TableCell className="font-mono">
										{passRate === null ? (
											<span className="text-muted-foreground font-sans">{t("evals.runs.list.notScored")}</span>
										) : (
											<>
												{passRate}%{" "}
												<span className="text-muted-foreground">
													{t("evals.runs.list.passedOf", { passed: run.passed, total: run.counts.completed ?? 0 })}
												</span>
											</>
										)}
									</TableCell>
									{trend.series.map((series, index) => {
										const scored = run.summary?.evaluators.find((item) => item.evaluator_id === series.id && item.scored > 0);
										return (
											<TableCell key={series.id} className="font-mono whitespace-nowrap">
												{scored ? (
													<>
														{Math.round(scored.pass_rate)}%
														{scored.mean !== undefined && (
															<span className="text-muted-foreground"> · {Math.round(scored.mean * 100) / 100}</span>
														)}
														<div className="bg-muted mt-1 h-0.5 w-20 rounded-full" aria-hidden>
															<div
																className="h-0.5 rounded-full"
																style={{ width: `${Math.max(0, Math.min(100, scored.pass_rate))}%`, background: trendColor(index + 1) }}
															/>
														</div>
													</>
												) : (
													<span className="text-muted-foreground">–</span>
												)}
											</TableCell>
										);
									})}
									<TableCell className="text-muted-foreground whitespace-nowrap">
										{formatDistanceToNow(new Date(run.created_at), { addSuffix: true, locale: dateFnsLocale(getLocale()) })}
									</TableCell>
									<TableCell className="text-muted-foreground font-mono">{formatDuration(runDurationMs(run, now))}</TableCell>
									<TableCell>
										<DropdownMenu>
											<DropdownMenuTrigger asChild>
												<Button
													variant="ghost"
													size="icon"
													className="size-7.5"
													aria-label={t("evals.runs.list.actionsFor", { name: run.name })}
													data-testid={`run-actions-${run.name}`}
												>
													<MoreHorizontal className="h-4 w-4" />
												</Button>
											</DropdownMenuTrigger>
											<DropdownMenuContent align="end">
												{canUpdate && !isRunActive(run.status) && (
													<DropdownMenuItem
														onClick={() =>
															void attempt(() => rerunRun({ id: run.id, scope: "failed" }).unwrap(), t("evals.runs.report.rerunStarted"))
														}
													>
														<RotateCw className="h-4 w-4" />
														{t("evals.runs.report.rerunFailed")}
													</DropdownMenuItem>
												)}
												<DropdownMenuItem asChild>
													<a href={runExportUrl(run.id)} download>
														<Download className="h-4 w-4" />
														{t("evals.runs.report.exportCsv")}
													</a>
												</DropdownMenuItem>
												{canDelete && !isRunActive(run.status) && (
													<DropdownMenuItem variant="destructive" onClick={() => setToDelete(run)} data-testid={`run-delete-${run.name}`}>
														<Trash2 className="h-4 w-4" />
														{tCommon("delete")}
													</DropdownMenuItem>
												)}
											</DropdownMenuContent>
										</DropdownMenu>
									</TableCell>
								</TableRow>
							);
						})}
					</TableBody>
				</Table>
			</div>

			<RunsPagination offset={offset} limit={PAGE_SIZE} total={total} onOffsetChange={setOffset} testId="runs" />

			<AlertDialog open={toDelete !== null} onOpenChange={(open) => !open && setToDelete(null)}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>{t("evals.datasets.list.deleteTitle", { name: toDelete?.name ?? "" })}</AlertDialogTitle>
						<AlertDialogDescription>{t("evals.runs.list.deleteDescription")}</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel>{tCommon("cancel")}</AlertDialogCancel>
						<AlertDialogAction
							onClick={() => {
								const run = toDelete;
								setToDelete(null);
								if (run) {
									setSelected((current) => current.filter((id) => id !== run.id));
									void attempt(() => deleteRun(run.id).unwrap(), t("evals.datasets.list.deleted", { name: run.name }));
								}
							}}
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