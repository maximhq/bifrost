import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { getErrorMessage, useExecuteDraftEvaluatorMutation, useLazyGetEvaluatorExecutionQuery } from "@/lib/store";
import type { DraftEvaluator, EvalKey, EvaluationExecution } from "@/lib/types/evals";
import { cn } from "@/lib/utils";
import { LoaderCircle, Play } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { type ExecutionOutcome, executionOutcome, scoreText } from "./evaluatorUtils";

// How long the gateway holds the execute call before answering "still running".
const EXECUTE_WAIT_MS = 15000;
const POLL_INTERVAL_MS = 1000;
// A run that has not finished by then is reported, not waited on forever.
const POLL_LIMIT_MS = 180000;

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
}

/** Runs the evaluator as it is in the editor, saved or not, on values the user types. */
export function EvaluatorTestPanel({ draft, variables, optionalVariables, blockedReason, evalKeys }: EvaluatorTestPanelProps) {
	const { t } = useTranslation("config");
	const [values, setValues] = useState<Record<string, string>>({});
	const [execution, setExecution] = useState<EvaluationExecution | null>(null);
	const [running, setRunning] = useState(false);
	const [failure, setFailure] = useState<string | null>(null);
	const [executeDraft] = useExecuteDraftEvaluatorMutation();
	const [fetchExecution] = useLazyGetEvaluatorExecutionQuery();
	// Bumped on every run and on unmount, so a superseded run stops polling.
	const runToken = useRef(0);

	useEffect(
		() => () => {
			runToken.current += 1;
		},
		[],
	);

	const handleRun = async () => {
		const token = ++runToken.current;
		setRunning(true);
		setFailure(null);
		setExecution(null);
		try {
			const sent: Record<string, string> = {};
			for (const name of [...variables, ...optionalVariables]) {
				if (values[name]) sent[name] = values[name];
			}
			let current = (await executeDraft({ draft, variables: sent, wait_ms: EXECUTE_WAIT_MS }).unwrap()).execution;
			const deadline = Date.now() + POLL_LIMIT_MS;
			while (current.status !== "completed" && token === runToken.current) {
				if (Date.now() > deadline) throw new Error(t("evals.test.timedOut"));
				setExecution(current);
				await new Promise((resolve) => setTimeout(resolve, POLL_INTERVAL_MS));
				if (token !== runToken.current) return;
				current = (await fetchExecution(current.id).unwrap()).execution;
			}
			if (token === runToken.current) setExecution(current);
		} catch (error) {
			if (token === runToken.current) setFailure(getErrorMessage(error));
		} finally {
			if (token === runToken.current) setRunning(false);
		}
	};

	const result = execution?.status === "completed" ? execution.result : undefined;
	const outcome = result ? executionOutcome(result) : undefined;
	const ranOn = execution?.key_id ? evalKeys.find((key) => key.key_id === execution.key_id) : undefined;

	return (
		<aside className="flex min-h-0 grow flex-col" data-testid="evaluator-test-panel">
			<div className="flex shrink-0 flex-col gap-0.5 border-b px-4 py-3">
				<h2 className="text-base font-semibold">{t("evals.test.title")}</h2>
				<p className="text-muted-foreground text-xs">{t("evals.test.description")}</p>
			</div>
			<div className="relative flex min-h-0 grow flex-col gap-4 p-4 lg:overflow-y-auto">
				{variables.length === 0 && optionalVariables.length === 0 && (
					<p className="text-muted-foreground text-xs" data-testid="evaluator-test-no-variables">
						{t("evals.test.noVariables")}
					</p>
				)}

				{[...variables.map((name) => ({ name, optional: false })), ...optionalVariables.map((name) => ({ name, optional: true }))].map(
					({ name, optional }) => (
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
					),
				)}

				<div className="flex flex-col gap-1.5">
					<Button
						type="button"
						onClick={() => void handleRun()}
						disabled={running || Boolean(blockedReason)}
						data-testid="evaluator-test-run-button"
					>
						{running ? <LoaderCircle className="size-4 animate-spin" /> : <Play className="size-4" />}
						{running ? t("evals.test.running") : t("evals.test.run")}
					</Button>
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

function Fact({ label, value, className }: { label: string; value: string; className?: string }) {
	return (
		<div className="flex flex-col gap-0.5">
			<dt className="text-muted-foreground">{label}</dt>
			<dd className={cn("break-words", className)}>{value}</dd>
		</div>
	);
}