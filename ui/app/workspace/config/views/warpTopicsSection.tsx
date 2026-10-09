import { Button } from "@/components/ui/button";
import { DateTimePickerWithRange } from "@/components/ui/datePickerWithRange";
import { getErrorMessage } from "@/lib/store";
import { useCancelWarpTopicsMutation, useGetWarpTopicsStatusQuery, useStartWarpTopicsMutation } from "@/lib/store/apis/warpApi";
import type { WarpBackfillJob } from "@/lib/types/warp";
import { getRangeForPeriod, TIME_PERIODS } from "@/lib/utils/timeRange";
import { Loader2 } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";

const DEFAULT_TOPICS_PERIOD = "30d";

interface WarpTopicsSectionProps {
	hasSettingsUpdateAccess: boolean;
	configured: boolean;
	vectorStoreConnected: boolean;
	/** Unsaved form edits. A run against settings that are about to change is wasted. */
	hasChanges: boolean;
}

/**
 * Computes topic clusters over the indexed requests.
 *
 * "What do people ask about?" has no aggregate in the log store; the answer is
 * in the meaning of the requests, which the log vectors already encode.
 * This job clusters them, names each cluster through Warp's model, and stores
 * one centroid per topic so the list_topics tool can answer in one call.
 *
 * Laid out as the backfill beside it is, because it is the same kind of thing:
 * a window, a background job, cancel, and a status that survives a reload.
 */
export default function WarpTopicsSection({
	hasSettingsUpdateAccess,
	configured,
	vectorStoreConnected,
	hasChanges,
}: WarpTopicsSectionProps) {
	const [startTopics, { isLoading: isStarting }] = useStartWarpTopicsMutation();
	const [cancelTopics, { isLoading: isCancelling }] = useCancelWarpTopicsMutation();
	const [activeID, setActiveID] = useState<string | null>(null);
	const [finished, setFinished] = useState<WarpBackfillJob | null>(null);
	const [period, setPeriod] = useState<string | undefined>(DEFAULT_TOPICS_PERIOD);
	const [start, setStart] = useState<Date | undefined>(() => getRangeForPeriod(DEFAULT_TOPICS_PERIOD).from);
	const [end, setEnd] = useState<Date | undefined>(() => getRangeForPeriod(DEFAULT_TOPICS_PERIOD).to);
	// Memoized so the picker only resyncs on a real edit: it resets its internal
	// state whenever this object's identity changes, and the status poll
	// rerenders this view every couple of seconds.
	const range = useMemo(() => ({ from: start, to: end }), [start, end]);

	const {
		data: status,
		// Until the status request lands, "no job is running" is a guess, and
		// starting on that guess is a guaranteed 409.
		isLoading: isStatusLoading,
		isFetching: isStatusFetching,
		isError: isStatusError,
		refetch: refetchStatus,
	} = useGetWarpTopicsStatusQuery(activeID ? { id: activeID } : undefined, {
		// Not gated on configured: disabling Warp mid-run must not hide the
		// running job or its cancel action after a reload.
		skip: !hasSettingsUpdateAccess,
		pollingInterval: activeID ? 2000 : 10000,
	});
	const isActive = status?.status === "pending" || status?.status === "running" || status?.status === "cancelling";
	// A live job always wins; otherwise fall back to the run that just ended.
	const shown = status?.id ? status : finished;

	// Adopt a job discovered by the id-less request, so a reload during a run
	// keeps following that run to its end.
	useEffect(() => {
		if (activeID || !status?.id) return;
		if (status.status === "pending" || status.status === "running" || status.status === "cancelling") {
			setActiveID(status.id);
		}
	}, [activeID, status?.id, status?.status]);

	// Release the pinned id once the job has finished, keeping its outcome on
	// screen: that is exactly when someone wants to read it.
	useEffect(() => {
		if (!status?.id) return;
		if (status.status === "completed" || status.status === "failed" || status.status === "cancelled") {
			setFinished(status);
			setActiveID(null);
		}
	}, [status]);

	const onStart = async () => {
		const window = period ? getRangeForPeriod(period) : { from: start, to: end };
		if (!window.from || !window.to || window.from >= window.to) {
			toast.error("Choose a valid time range for topics.");
			return;
		}
		try {
			const started = await startTopics({ start_time: window.from.toISOString(), end_time: window.to.toISOString() }).unwrap();
			setFinished(null);
			setActiveID(started.id ?? null);
			toast.success("Topic clustering started.");
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const onCancel = async () => {
		try {
			await cancelTopics(activeID ? { id: activeID } : undefined).unwrap();
			toast.success("Topic clustering cancellation requested.");
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	return (
		<div className="-mx-4 space-y-3 border-t px-4 pt-4" data-testid="warp-topics-section">
			<div className="space-y-0.5">
				<h4 className="text-sm font-medium">Topic clusters</h4>
				<p className="text-muted-foreground text-xs">
					Group indexed requests by meaning and name each group, so Warp can answer what people ask about. Runs in the background, can be
					cancelled, and replaces the previous topics when it finishes.
				</p>
			</div>

			<div className="flex flex-col gap-2 md:flex-row md:items-center">
				<div className="min-w-0 flex-1">
					<DateTimePickerWithRange
						buttonClassName="w-full"
						popupAlignment="start"
						triggerTestId="warp-topics-range"
						dateTime={range}
						disabledAfter={new Date()}
						disabled={isActive || !hasSettingsUpdateAccess}
						predefinedPeriod={period}
						preDefinedPeriods={TIME_PERIODS}
						onDateTimeUpdate={(updated) => {
							setPeriod(undefined);
							setStart(updated.from);
							setEnd(updated.to);
						}}
						onPredefinedPeriodChange={(selected) => {
							if (!selected) return;
							const { from, to } = getRangeForPeriod(selected);
							setPeriod(selected);
							setStart(from);
							setEnd(to);
						}}
					/>
				</div>

				<div className="flex shrink-0 items-center justify-end gap-2">
					{isActive ? (
						<Button
							type="button"
							variant="outline"
							onClick={onCancel}
							disabled={isCancelling || !hasSettingsUpdateAccess}
							data-testid="warp-topics-cancel-btn"
						>
							{isCancelling && <Loader2 className="mr-2 h-4 w-4 animate-spin" />} Cancel clustering
						</Button>
					) : (
						<Button
							type="button"
							variant="outline"
							onClick={onStart}
							disabled={
								isStarting ||
								isStatusLoading ||
								isStatusFetching ||
								isStatusError ||
								!configured ||
								!vectorStoreConnected ||
								!hasSettingsUpdateAccess ||
								hasChanges
							}
							data-testid="warp-topics-start-btn"
						>
							{isStarting && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
							Compute topics
						</Button>
					)}
				</div>
			</div>

			{hasChanges && <p className="text-muted-foreground text-xs">Save configuration changes before computing topics.</p>}

			{shown?.id && (
				<div className="bg-muted/40 space-y-2 rounded-sm p-3 text-sm" data-testid="warp-topics-status">
					<div className="flex items-center justify-between gap-3">
						<span className="font-medium capitalize">{shown.status}</span>
						<span className="text-muted-foreground">
							{shown.scanned} request{shown.scanned === 1 ? "" : "s"} read
						</span>
					</div>
					<div className="text-muted-foreground text-xs">
						{shown.indexed} topic{shown.indexed === 1 ? "" : "s"} written
						{shown.failed > 0 ? ` · ${shown.failed} label${shown.failed === 1 ? "" : "s"} fell back` : ""}
					</div>
					{shown.message && <p className="text-muted-foreground text-xs">{shown.message}</p>}
					{shown.last_error && <p className="text-destructive text-xs">Latest error: {shown.last_error}</p>}
				</div>
			)}

			{isStatusError && (
				<p className="text-destructive flex items-center gap-2 text-xs" role="alert" data-testid="warp-topics-status-error">
					Could not read topic clustering status, so a running job may not be shown.
					<button type="button" onClick={() => refetchStatus()} className="underline" data-testid="warp-topics-status-retry">
						Retry
					</button>
				</p>
			)}
		</div>
	);
}