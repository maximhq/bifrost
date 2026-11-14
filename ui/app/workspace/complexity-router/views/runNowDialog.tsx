import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { getErrorMessage } from "@/lib/store";
import { useGetComplexitySuggestionRunEstimateQuery, useStartComplexitySuggestionRunMutation } from "@/lib/store/apis/governanceApi";
import { SuggestionRunWindow } from "@/lib/types/complexityRouter";
import { LoaderCircle, Play } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { availableWindows, canStartRun } from "./suggestions.utils";

interface Props {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	routerId: string;
	// Days of captured traffic kept; windows longer than this would read
	// vectors that are gone, so they are not offered.
	lookbackDays: number;
	// Called with the run's ID once the gateway accepts it.
	onStarted?: (runId: string) => void;
}

// RunNowDialog starts a suggestion run outside the schedule over a window the
// operator picks, after showing what that run would read and cost in model
// calls.
export default function RunNowDialog({ open, onOpenChange, routerId, lookbackDays, onStarted }: Props) {
	const { t } = useTranslation("models", { keyPrefix: "routing.complexityUi.suggestions" });
	const { t: tCommon } = useTranslation("common");
	const windows = useMemo(() => availableWindows(lookbackDays), [lookbackDays]);
	const [runWindow, setRunWindow] = useState<SuggestionRunWindow | undefined>(undefined);
	useEffect(() => {
		if (!open) return;
		// The longest window that fits, up to a week: the default lookback.
		setRunWindow(windows.includes("7d") ? "7d" : windows[windows.length - 1]);
	}, [open, windows]);
	// currentData, not data: data keeps the last window's estimate while a new
	// one loads or after it fails, which would show (and start on) counts for a
	// window that is no longer selected.
	const {
		currentData: estimate,
		isFetching,
		isSuccess,
		error,
	} = useGetComplexitySuggestionRunEstimateQuery({ window: runWindow ?? "24h", routerId }, { skip: !open || !runWindow });
	const estimated = isSuccess && !isFetching && estimate !== undefined;
	const [startRun, { isLoading: isStarting }] = useStartComplexitySuggestionRunMutation();

	const start = () => {
		startRun({ window: runWindow, routerId })
			.unwrap()
			.then(({ run_id }) => {
				onStarted?.(run_id);
				toast.success(t("runStarted"), { position: "top-right" });
				onOpenChange(false);
			})
			.catch((err) => toast.error(t("runFailedToStart", { error: getErrorMessage(err) }), { position: "top-right" }));
	};

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent className="max-w-md" data-testid="complexity-suggestions-run-dialog">
				<DialogHeader>
					<DialogTitle>{t("runDialogTitle")}</DialogTitle>
					<DialogDescription>{t("runInBackground")}</DialogDescription>
				</DialogHeader>
				<div className="space-y-4">
					<div className="space-y-1.5">
						<Label htmlFor="suggestions-run-window">{t("runWindowLabel")}</Label>
						<Select value={runWindow} onValueChange={(value) => setRunWindow(value as SuggestionRunWindow)}>
							<SelectTrigger id="suggestions-run-window" className="w-full" data-testid="complexity-suggestions-run-window-select">
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								{windows.map((value) => (
									<SelectItem key={value} value={value} data-testid={`complexity-suggestions-run-window-${value}`}>
										{t(`runWindow.${value}`)}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
					</div>
					{/* The estimate only decides whether a run can start; it is shown
					    only when it says why one cannot. */}
					{error && !isFetching ? (
						<p className="text-destructive text-xs" data-testid="complexity-suggestions-run-estimate" aria-live="polite">
							{t("estimateFailed", { error: getErrorMessage(error) })}
						</p>
					) : estimated && !canStartRun(estimate) ? (
						<p className="text-muted-foreground text-xs" data-testid="complexity-suggestions-run-estimate" aria-live="polite">
							{t("estimateEmpty")}
						</p>
					) : estimated && estimate.backfill_unknown ? (
						<p className="text-muted-foreground text-xs" data-testid="complexity-suggestions-run-backfill" aria-live="polite">
							{t("estimateBackfillUnknown")}
						</p>
					) : estimated && (estimate.backfill ?? 0) > 0 ? (
						// A first run over a window from before suggestions were on
						// embeds those requests first, so say so before it starts.
						<p className="text-muted-foreground text-xs" data-testid="complexity-suggestions-run-backfill" aria-live="polite">
							{t("estimateBackfill", { count: estimate.backfill })}
						</p>
					) : null}
				</div>
				<DialogFooter className="gap-2">
					<Button
						type="button"
						variant="outline"
						size="sm"
						onClick={() => onOpenChange(false)}
						data-testid="complexity-suggestions-run-cancel-button"
					>
						{tCommon("cancel")}
					</Button>
					<Button
						type="button"
						size="sm"
						onClick={start}
						// Only on a successful estimate of the selected window.
						disabled={isStarting || !runWindow || !estimated || !canStartRun(estimate)}
						data-testid="complexity-suggestions-run-start-button"
					>
						{isStarting || (isFetching && !error) ? <LoaderCircle className="size-3.5 animate-spin" /> : <Play className="size-3.5" />}
						{t("runStart")}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}