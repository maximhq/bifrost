import { Button } from "@/components/ui/button";
import { Progress } from "@/components/ui/progress";
import { getLocale } from "@/lib/i18n";
import { getErrorMessage } from "@/lib/store";
import { useCancelComplexitySuggestionRunMutation } from "@/lib/store/apis/governanceApi";
import { SuggestionRun } from "@/lib/types/complexityRouter";
import { cn } from "@/lib/utils";
import { formatDistanceToNow } from "date-fns";
import { LoaderCircle, Play, Settings2 } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { dateFnsLocale } from "@/lib/i18n/dateLocale";
import { runBreakdown, runInProgress, runProgress, runWindowDays } from "./suggestions.utils";

interface Props {
	routerId: string;
	run?: SuggestionRun;
	// Whether any suggestion has ever been stored for the router: with none
	// and no run, the card invites the first run.
	hasSuggestions: boolean;
	nextRunAt?: string;
	enabled: boolean;
	// Whether a run can be started here: suggestions are on, capture works and
	// the caller may change the router.
	canRun: boolean;
	canUpdate: boolean;
	// A run asked for here that has not recorded itself yet.
	awaiting: boolean;
	onRun: () => void;
	onOpenSettings: () => void;
}

// SuggestionRunCard says where the router's suggestions stand: no run yet,
// a run in progress with how far it got, or the last run and how its window
// became the groups listed below.
export default function SuggestionRunCard({
	routerId,
	run,
	hasSuggestions,
	nextRunAt,
	enabled,
	canRun,
	canUpdate,
	awaiting,
	onRun,
	onOpenSettings,
}: Props) {
	const { t } = useTranslation("models", { keyPrefix: "routing.complexityUi.suggestions" });
	const [breakdownOpen, setBreakdownOpen] = useState(false);
	const [cancelRun, { isLoading: isCancelling }] = useCancelComplexitySuggestionRunMutation();
	const number = (value: number) => new Intl.NumberFormat(getLocale()).format(value);
	const ago = (value: string) => formatDistanceToNow(new Date(value), { addSuffix: true, locale: dateFnsLocale(getLocale()) });
	const nextRun =
		enabled && nextRunAt ? new Date(nextRunAt).toLocaleString(getLocale(), { dateStyle: "medium", timeStyle: "short" }) : null;

	const settingsButton = (
		<Button
			type="button"
			variant="ghost"
			size="sm"
			onClick={onOpenSettings}
			aria-label={t("settings")}
			title={t("settings")}
			data-testid="complexity-suggestions-settings-button"
		>
			<Settings2 className="size-3.5" />
		</Button>
	);

	if (runInProgress(run) || awaiting) {
		const progress = run && runInProgress(run) ? runProgress(run) : 0;
		const stage = run && runInProgress(run) ? (run.status === "queued" ? "queued" : (run.stage ?? "queued")) : "queued";
		const cancel = () => {
			if (!run) return;
			cancelRun({ runId: run.id, routerId })
				.unwrap()
				.then(() => toast.success(t("runCancelled"), { position: "top-right" }))
				.catch((err) => toast.error(t("runCancelFailed", { error: getErrorMessage(err) }), { position: "top-right" }));
		};
		return (
			<section className="space-y-3 rounded-sm border p-4" data-testid="complexity-suggestions-run-progress">
				<div className="flex flex-wrap items-center gap-x-3 gap-y-1">
					<h3 className="text-sm font-semibold">
						{run && runInProgress(run) ? t("analyzingWindow", { count: runWindowDays(run) }) : t("analyzingQueued")}
					</h3>
					<span className="text-muted-foreground text-xs">{t("runInBackground")}</span>
					{canUpdate && run && runInProgress(run) && (
						<Button
							type="button"
							variant="outline"
							size="sm"
							className="ml-auto h-7"
							onClick={cancel}
							disabled={isCancelling}
							data-testid="complexity-suggestions-cancel-run-button"
						>
							{isCancelling && <LoaderCircle className="size-3.5 animate-spin" />}
							{t("cancelRun")}
						</Button>
					)}
				</div>
				<Progress value={progress * 100} className="h-1.5" aria-label={t("runProgressAria")} />
				<p className="text-muted-foreground text-xs" data-testid="complexity-suggestions-run-stage">
					{stage === "suggesting" && run
						? t("stage.suggestingCount", { done: number(run.clusters_done), total: number(run.clusters_asked) })
						: t(`stage.${stage}`)}
				</p>
			</section>
		);
	}

	if (!run && !hasSuggestions) {
		return (
			<section className="flex flex-col items-start gap-3 rounded-sm border p-6" data-testid="complexity-suggestions-empty-state">
				<div className="flex w-full items-start gap-2">
					<p className="text-muted-foreground text-base font-medium">{t("emptyTitle")}</p>
					<span className="ml-auto">{settingsButton}</span>
				</div>
				<p className="text-muted-foreground max-w-2xl text-sm leading-relaxed">{t("emptyBody")}</p>
				{canRun && (
					<Button type="button" size="sm" onClick={onRun} data-testid="complexity-suggestions-analyze-button">
						<Play className="size-3.5" />
						{t("analyzeTraffic")}
					</Button>
				)}
				{nextRun && <p className="text-muted-foreground text-xs">{t("nextRun", { time: nextRun })}</p>}
			</section>
		);
	}

	const rows = run ? runBreakdown(run) : [];
	return (
		<section className="rounded-sm border" data-testid="complexity-suggestions-run-card">
			<div className="flex flex-wrap items-center gap-x-3 gap-y-1.5 px-4 py-3">
				{run ? (
					<>
						<span className="text-sm font-semibold">{t("runTitle", { count: runWindowDays(run) })}</span>
						<span className="text-muted-foreground text-xs" data-testid="complexity-suggestions-last-run">
							{run.status === "failed"
								? t("runFailedAgo", { ago: ago(run.finished_at ?? run.started_at) })
								: run.status === "cancelled"
									? t("runCancelledAgo", { ago: ago(run.finished_at ?? run.started_at) })
									: run.status === "skipped"
										? t("runSkippedAgo", { ago: ago(run.started_at) })
										: t("runSummary", {
												ago: ago(run.finished_at ?? run.started_at),
												requests: number(run.requests_read),
												groups: number(run.created + run.refreshed),
												count: run.created + run.refreshed,
											})}
						</span>
					</>
				) : (
					<span className="text-sm font-semibold">{t("title")}</span>
				)}
				<div className="ml-auto flex items-center gap-2">
					{run && run.status !== "skipped" && rows.length > 1 && (
						<button
							type="button"
							onClick={() => setBreakdownOpen((open) => !open)}
							aria-expanded={breakdownOpen}
							className="text-muted-foreground hover:text-foreground text-xs underline-offset-2 hover:underline"
							data-testid="complexity-suggestions-breakdown-toggle"
						>
							{breakdownOpen
								? t("breakdownHide")
								: t("breakdownShow", { requests: number(run.requests_read), groups: number(run.created + run.refreshed) })}
						</button>
					)}
					{settingsButton}
					{canRun && (
						<Button
							type="button"
							variant="outline"
							size="sm"
							className="h-7"
							onClick={onRun}
							data-testid="complexity-suggestions-run-now-button"
						>
							<Play className="size-3.5" />
							{run ? t("rerun") : t("analyzeTraffic")}
						</Button>
					)}
				</div>
			</div>
			{nextRun && (
				<p className="text-muted-foreground -mt-1.5 px-4 pb-3 text-xs" data-testid="complexity-suggestions-next-run">
					{t("nextRun", { time: nextRun })}
				</p>
			)}
			{breakdownOpen && run && (
				<div
					className="grid max-w-3xl grid-cols-[minmax(0,1fr)_auto] gap-x-6 gap-y-1.5 border-t px-4 py-3 text-xs"
					data-testid="complexity-suggestions-breakdown"
				>
					{rows.map((row) => (
						<div key={row.step} className="contents" data-testid={`complexity-suggestions-breakdown-${row.step}`}>
							<span className={cn(row.kind === "minus" ? "text-muted-foreground pl-3" : "font-medium")}>
								{row.kind === "minus" ? "− " : row.kind === "result" ? "= " : ""}
								{t(`breakdown.${row.step}`)}
							</span>
							<span className={cn("text-right font-mono tabular-nums", row.kind === "minus" && "text-muted-foreground")}>
								{row.unit === "groups" ? t("breakdownGroups", { count: row.value, n: number(row.value) }) : number(row.value)}
							</span>
						</div>
					))}
				</div>
			)}
		</section>
	);
}