import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import {
	getErrorMessage,
	useExecuteDraftEvaluatorMutation,
	useLazyGetEvalDatasetRowsQuery,
	useLazyGetEvalDatasetRowVariablesQuery,
	useLazyGetEvaluatorExecutionQuery,
} from "@/lib/store";
import type { DraftEvaluator, EvalKey, EvaluationExecution, EvaluationResult } from "@/lib/types/evals";
import { cn } from "@/lib/utils";
import { LoaderCircle, Play, Square } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { type ExecutionOutcome, executionOutcome, scoreText } from "./evaluatorUtils";
import { type DatasetBatch, LoadFromDataset } from "../../datasets/views/loadFromDataset";

// How long the gateway holds the execute call before answering "still running".
const EXECUTE_WAIT_MS = 15000;
const POLL_INTERVAL_MS = 1000;
// A run that has not finished by then is reported, not waited on forever.
const POLL_LIMIT_MS = 180000;

// Rows of a dataset or split are read a page at a time and scored a few at a time.
// The gateway still holds every model call to the eval key's own concurrency.
const BATCH_PAGE = 100;
const BATCH_WORKERS = 3;

const OUTCOMES: ExecutionOutcome[] = ["passed", "failed", "scored", "na", "error"];

// One row of a dataset or split test: its position, and its result or why it has none.
interface BatchRow {
	position: number;
	result?: EvaluationResult;
	failure?: string;
}

const OUTCOME_BADGES: Record<ExecutionOutcome, "success" | "destructive" | "secondary" | "warning"> = {
	passed: "success",
	failed: "destructive",
	scored: "secondary",
	na: "secondary",
	error: "warning",
};

interface EvaluatorTestPanelProps {
	draft: DraftEvaluator;
	variables: string[];
	optionalVariables: string[];
	// Why the evaluator cannot run right now, or undefined when it can.
	blockedReason?: string;
	evalKeys: EvalKey[];
	// Leave out the title block, when the panel sits in a sheet that has its own.
	hideHeader?: boolean;
}

/** Runs the evaluator as it is in the editor, saved or not, on values the user types. */
export function EvaluatorTestPanel({ draft, variables, optionalVariables, blockedReason, evalKeys, hideHeader }: EvaluatorTestPanelProps) {
	const { t } = useTranslation("config");
	const [values, setValues] = useState<Record<string, string>>({});
	const [execution, setExecution] = useState<EvaluationExecution | null>(null);
	const [running, setRunning] = useState(false);
	const [failure, setFailure] = useState<string | null>(null);
	const [executeDraft] = useExecuteDraftEvaluatorMutation();
	const [fetchExecution] = useLazyGetEvaluatorExecutionQuery();
	const [fetchRows] = useLazyGetEvalDatasetRowsQuery();
	const [fetchRowVariables] = useLazyGetEvalDatasetRowVariablesQuery();
	// Set when the test reads a whole dataset or a split, not the typed values.
	const [batch, setBatch] = useState<DatasetBatch | null>(null);
	const [batchRows, setBatchRows] = useState<BatchRow[] | null>(null);
	const [batchTotal, setBatchTotal] = useState(0);
	// Bumped on every run and on unmount, so a superseded run stops polling.
	const runToken = useRef(0);

	useEffect(
		() => () => {
			runToken.current += 1;
		},
		[],
	);

	// execute runs the draft once on the given variables and waits for it to finish.
	// It returns nothing when a newer run has taken over.
	const execute = async (
		token: number,
		sent: Record<string, string>,
		onProgress?: (current: EvaluationExecution) => void,
	): Promise<EvaluationExecution | undefined> => {
		let current = (await executeDraft({ draft, variables: sent, wait_ms: EXECUTE_WAIT_MS }).unwrap()).execution;
		const deadline = Date.now() + POLL_LIMIT_MS;
		while (current.status !== "completed") {
			if (token !== runToken.current) return undefined;
			if (Date.now() > deadline) throw new Error(t("evals.test.timedOut"));
			onProgress?.(current);
			await new Promise((resolve) => setTimeout(resolve, POLL_INTERVAL_MS));
			if (token !== runToken.current) return undefined;
			current = (await fetchExecution(current.id).unwrap()).execution;
		}
		return token === runToken.current ? current : undefined;
	};

	// Only the variables the evaluator reads are sent; empty ones are left out.
	const pick = (source: Record<string, string>) => {
		const sent: Record<string, string> = {};
		for (const name of [...variables, ...optionalVariables]) {
			if (source[name]) sent[name] = source[name];
		}
		return sent;
	};

	const handleRun = async () => {
		const token = ++runToken.current;
		setRunning(true);
		setFailure(null);
		setExecution(null);
		setBatchRows(null);
		try {
			const finished = await execute(token, pick(values), setExecution);
			if (finished) setExecution(finished);
		} catch (error) {
			if (token === runToken.current) setFailure(getErrorMessage(error));
		} finally {
			if (token === runToken.current) setRunning(false);
		}
	};

	// handleBatchRun scores every row of the chosen dataset or split. A row that
	// cannot be scored is listed with its error; the other rows still run.
	const handleBatchRun = async (target: DatasetBatch) => {
		const token = ++runToken.current;
		setRunning(true);
		setFailure(null);
		setExecution(null);
		setBatchRows([]);
		setBatchTotal(target.total);
		try {
			const rowIds: string[] = [];
			for (;;) {
				const page = await fetchRows({ id: target.datasetId, split: target.splitId, offset: rowIds.length, limit: BATCH_PAGE }).unwrap();
				if (token !== runToken.current) return;
				for (const row of page.rows) rowIds.push(row.id);
				if (page.rows.length === 0 || rowIds.length >= page.total) break;
			}
			setBatchTotal(rowIds.length);
			if (rowIds.length === 0) {
				setFailure(t("evals.test.noRows"));
				return;
			}
			let next = 0;
			const worker = async () => {
				while (token === runToken.current && next < rowIds.length) {
					const position = next++;
					let row: BatchRow;
					try {
						const loaded = await fetchRowVariables({ id: target.datasetId, rowId: rowIds[position] }).unwrap();
						const finished = await execute(token, pick(loaded.variables));
						if (!finished) return;
						row = { position, result: finished.result };
					} catch (error) {
						row = { position, failure: getErrorMessage(error) };
					}
					if (token !== runToken.current) return;
					setBatchRows((prev) => [...(prev ?? []), row]);
				}
			};
			await Promise.all(Array.from({ length: Math.min(BATCH_WORKERS, rowIds.length) }, worker));
		} catch (error) {
			if (token === runToken.current) setFailure(getErrorMessage(error));
		} finally {
			if (token === runToken.current) setRunning(false);
		}
	};

	// Stopping keeps the rows scored so far. Calls already sent finish on the gateway.
	const handleStop = () => {
		runToken.current += 1;
		setRunning(false);
	};

	const result = execution?.status === "completed" ? execution.result : undefined;
	const outcome = result ? executionOutcome(result) : undefined;
	const ranOn = execution?.key_id ? evalKeys.find((key) => key.key_id === execution.key_id) : undefined;

	return (
		<aside className="flex min-h-0 grow flex-col" data-testid="evaluator-test-panel">
			{!hideHeader && (
				<div className="flex shrink-0 flex-col gap-0.5 border-b px-4 py-3">
					<h2 className="text-base font-semibold">{t("evals.test.title")}</h2>
					<p className="text-muted-foreground text-xs">{t("evals.test.description")}</p>
				</div>
			)}
			<div className={cn("relative flex min-h-0 grow flex-col gap-4 overflow-y-auto", hideHeader ? "px-4 py-4 md:px-8" : "p-4")}>
				{(variables.length > 0 || optionalVariables.length > 0) && (
					<LoadFromDataset onLoad={(loaded) => setValues((prev) => ({ ...prev, ...loaded }))} onBatchChange={setBatch} disabled={running} />
				)}
				{variables.length === 0 && optionalVariables.length === 0 && (
					<p className="text-muted-foreground text-xs" data-testid="evaluator-test-no-variables">
						{t("evals.test.noVariables")}
					</p>
				)}

				{(batch
					? []
					: [...variables.map((name) => ({ name, optional: false })), ...optionalVariables.map((name) => ({ name, optional: true }))]
				).map(({ name, optional }) => (
					<div key={name} className="flex flex-col gap-1.5">
						<Label htmlFor={`evaluator-test-variable-${name}`} className="flex items-center gap-2 font-mono text-xs">
							{name}
							{optional && <span className="text-muted-foreground font-sans font-normal">{t("evals.test.optional")}</span>}
						</Label>
						<Textarea
							id={`evaluator-test-variable-${name}`}
							value={values[name] ?? ""}
							onChange={(e) => setValues((prev) => ({ ...prev, [name]: e.target.value }))}
							className="max-h-48 min-h-16 text-sm"
							data-testid={`evaluator-test-variable-${name}`}
						/>
					</div>
				))}

				<div className="flex flex-col gap-1.5">
					<Button
						type="button"
						onClick={() => void (batch ? handleBatchRun(batch) : handleRun())}
						disabled={running || Boolean(blockedReason)}
						data-testid="evaluator-test-run-button"
					>
						{running ? <LoaderCircle className="size-4 animate-spin" /> : <Play className="size-4" />}
						{running ? t("evals.test.running") : t("evals.test.run")}
					</Button>
					{running && batchRows && (
						<Button type="button" variant="outline" onClick={handleStop} data-testid="evaluator-test-stop-button">
							<Square className="size-4" />
							{t("evals.test.stop")}
						</Button>
					)}
					{blockedReason && (
						<p className="text-muted-foreground text-xs" data-testid="evaluator-test-blocked">
							{blockedReason}
						</p>
					)}
				</div>

				{running && execution && execution.status !== "completed" && (
					<p className="text-muted-foreground text-xs" data-testid="evaluator-test-status">
						{t(execution.status === "queued" ? "evals.test.queued" : "evals.test.stillRunning")}
					</p>
				)}

				{failure && (
					<div
						className="rounded-sm border border-red-200/40 bg-red-50/40 p-3 text-sm dark:border-red-800/40 dark:bg-red-950/40"
						data-testid="evaluator-test-failure"
					>
						{failure}
					</div>
				)}

				{batchRows && batchTotal > 0 && <BatchResults rows={batchRows} total={batchTotal} />}

				{result && outcome && (
					<div className="flex flex-col gap-3 rounded-sm border p-3" data-testid="evaluator-test-result">
						<div className="flex flex-wrap items-center gap-2">
							<Badge variant={OUTCOME_BADGES[outcome]} data-testid="evaluator-test-outcome">
								{t(`evals.test.outcomes.${outcome}`)}
							</Badge>
							{result.score.status === "ok" && (
								<span className="font-mono text-sm" data-testid="evaluator-test-score">
									{t("evals.test.score", { score: scoreText(result.score) })}
								</span>
							)}
						</div>
						{result.reasoning && (
							<div className="flex flex-col gap-1">
								<span className="text-muted-foreground text-xs font-medium">
									{t(outcome === "error" ? "evals.test.cause" : outcome === "na" ? "evals.test.why" : "evals.test.reasoning")}
								</span>
								<p className="text-sm break-words whitespace-pre-wrap" data-testid="evaluator-test-reasoning">
									{result.reasoning}
								</p>
							</div>
						)}
						{result.logs && result.logs.length > 0 && (
							<div className="flex flex-col gap-1">
								<span className="text-muted-foreground text-xs font-medium">{t("evals.test.logs")}</span>
								<pre
									className="bg-muted max-h-40 overflow-auto rounded-sm p-2 font-mono text-xs whitespace-pre-wrap"
									data-testid="evaluator-test-logs"
								>
									{result.logs.join("\n")}
								</pre>
							</div>
						)}
						<dl className="grid grid-cols-2 gap-x-4 gap-y-2 text-xs">
							{ranOn && (
								<Fact label={t("evals.test.ranOn")} value={t("evals.test.ranOnKey", { provider: ranOn.provider, name: ranOn.name })} />
							)}
							<Fact label={t("evals.test.latency")} value={t("evals.test.latencyMs", { ms: result.latency_ms })} />
							{result.usage && (
								<Fact
									label={t("evals.test.tokens")}
									value={t("evals.test.tokensInOut", { input: result.usage.prompt_tokens, output: result.usage.completion_tokens })}
								/>
							)}
							{result.cost !== undefined && result.cost !== null && (
								<Fact label={t("evals.test.cost")} value={`$${result.cost.toFixed(6)}`} className="font-mono" />
							)}
						</dl>
					</div>
				)}
			</div>
		</aside>
	);
}

// BatchResults shows a dataset or split test: how many rows landed on each outcome,
// the average of the numeric scores, and every row with its score and reasoning.
function BatchResults({ rows, total }: { rows: BatchRow[]; total: number }) {
	const { t } = useTranslation("config");
	const sorted = [...rows].sort((a, b) => a.position - b.position);
	const counts: Record<ExecutionOutcome, number> = { passed: 0, failed: 0, scored: 0, na: 0, error: 0 };
	let sum = 0;
	let numbers = 0;
	for (const row of rows) {
		counts[row.result ? executionOutcome(row.result) : "error"] += 1;
		const number = row.result?.score.status === "ok" ? row.result.score.number : undefined;
		if (number !== undefined && number !== null) {
			sum += number;
			numbers += 1;
		}
	}
	return (
		<div className="flex flex-col gap-3 rounded-sm border p-3" data-testid="evaluator-test-batch-result">
			<div className="flex flex-wrap items-center gap-2">
				<span className="text-sm font-medium" data-testid="evaluator-test-batch-progress">
					{t("evals.test.progress", { done: rows.length, total })}
				</span>
				{numbers > 0 && (
					<span className="text-muted-foreground font-mono text-xs" data-testid="evaluator-test-batch-average">
						{t("evals.test.average", { score: Number((sum / numbers).toFixed(4)) })}
					</span>
				)}
			</div>
			<div className="flex flex-wrap gap-1.5">
				{OUTCOMES.filter((outcome) => counts[outcome] > 0).map((outcome) => (
					<Badge key={outcome} variant={OUTCOME_BADGES[outcome]} data-testid={`evaluator-test-batch-count-${outcome}`}>
						{t(`evals.test.outcomes.${outcome}`)} {counts[outcome]}
					</Badge>
				))}
			</div>
			<ul className="max-h-96 overflow-y-auto rounded-sm border">
				{sorted.map((row) => {
					const outcome = row.result ? executionOutcome(row.result) : "error";
					const detail = row.result ? row.result.reasoning : row.failure;
					return (
						<li key={row.position} className="border-b last:border-b-0" data-testid="evaluator-test-batch-row">
							<details>
								<summary className="flex cursor-pointer items-center gap-2 px-2 py-1.5 text-sm">
									<span className="text-muted-foreground w-16 shrink-0 text-xs">
										{t("evals.test.rowNumber", { number: row.position + 1 })}
									</span>
									<Badge variant={OUTCOME_BADGES[outcome]}>{t(`evals.test.outcomes.${outcome}`)}</Badge>
									{row.result?.score.status === "ok" && <span className="font-mono text-xs">{scoreText(row.result.score)}</span>}
								</summary>
								{detail && <p className="text-muted-foreground px-2 pb-2 text-xs break-words whitespace-pre-wrap">{detail}</p>}
							</details>
						</li>
					);
				})}
			</ul>
		</div>
	);
}

function Fact({ label, value, className }: { label: string; value: string; className?: string }) {
	return (
		<div className="flex flex-col gap-0.5">
			<dt className="text-muted-foreground">{label}</dt>
			<dd className={cn("break-words", className)}>{value}</dd>
		</div>
	);
}