import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { getLocale } from "@/lib/i18n";
import { getErrorMessage } from "@/lib/store";
import { useGetComplexitySuggestionRunEstimateQuery, useStartComplexitySuggestionRunMutation } from "@/lib/store/apis/governanceApi";
import { SuggestionRunWindow } from "@/lib/types/complexityRouter";
import { LoaderCircle, Play } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { availableWindows } from "./suggestions.utils";

interface Props {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	routerId: string;
	// Days of captured traffic kept; windows longer than this would read
	// vectors that are gone, so they are not offered.
	lookbackDays: number;
}

// RunNowDialog starts a suggestion run outside the schedule over a window the
// operator picks, after showing what that run would read and cost in model
// calls.
export default function RunNowDialog({ open, onOpenChange, routerId, lookbackDays }: Props) {
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
	const number = (value: number) => new Intl.NumberFormat(getLocale()).format(value);

	const start = () => {
		startRun({ window: runWindow, routerId })
			.unwrap()
			.then(() => {
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
					<DialogDescription>{t("runDialogDescription")}</DialogDescription>
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
					<div
						className="bg-muted/50 space-y-1 rounded-sm p-3 text-xs"
						data-testid="complexity-suggestions-run-estimate"
						aria-live="polite"
					>
						{error && !isFetching ? (
							<p className="text-destructive">{t("estimateFailed", { error: getErrorMessage(error) })}</p>
						) : !estimated ? (
							<p className="text-muted-foreground flex items-center gap-2">
								<LoaderCircle className="size-3.5 animate-spin" />
								{t("estimating")}
							</p>
						) : (
							<>
								<p className="text-sm font-medium">
									{t("estimateRequests", { count: estimate.requests, formatted: number(estimate.requests) })}
								</p>
								{estimate.requests > 0 && (
									<p className="text-muted-foreground">
										{t("estimateClustered", { count: estimate.clustered, formatted: number(estimate.clustered) })}
										{estimate.skipped > 0 && ` ${t("estimateSkipped", { count: estimate.skipped, formatted: number(estimate.skipped) })}`}
									</p>
								)}
								<p className="text-muted-foreground">
									{t("estimateModelCalls", { count: estimate.max_model_calls, formatted: number(estimate.max_model_calls) })}
								</p>
							</>
						)}
						<p className="text-muted-foreground pt-1">{t("runInBackground")}</p>
					</div>
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
						disabled={isStarting || !runWindow || !estimated || estimate.requests === 0}
						data-testid="complexity-suggestions-run-start-button"
					>
						{isStarting ? <LoaderCircle className="size-3.5 animate-spin" /> : <Play className="size-3.5" />}
						{t("runStart")}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}