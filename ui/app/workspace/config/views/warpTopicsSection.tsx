import { Button } from "@/components/ui/button";
import { DateTimePickerWithRange } from "@/components/ui/datePickerWithRange";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { getErrorMessage } from "@/lib/store";
import {
	useCancelWarpTopicsMutation,
	useGetWarpLogIndexStatusQuery,
	useGetWarpTopicsStatusQuery,
	useStartWarpTopicsMutation,
} from "@/lib/store/apis/warpApi";
import type { WarpBackfillJob } from "@/lib/types/warp";
import { getRangeForPeriod, TIME_PERIODS } from "@/lib/utils/timeRange";
import { Info, Loader2 } from "lucide-react";
import { type ReactNode, useEffect, useMemo, useState } from "react";
import { toast } from "sonner";

const DEFAULT_TOPICS_PERIOD = "30d";

interface WarpTopicsSectionProps {
	hasSettingsUpdateAccess: boolean;
	configured: boolean;
	vectorStoreConnected: boolean;
	/** Unsaved form edits. A run against settings that are about to change is wasted. */
	hasChanges: boolean;
	/** The form's field for how many unmatched requests start a discovery. */
	discoveryField?: ReactNode;
}

/**
 * Seeds topic clusters over the indexed requests, once per embedding model.
 *
 * "What do people ask about?" has no aggregate in the log store; the answer is
 * in the meaning of the requests, which the log vectors already encode.
 * The seed run clusters them, names each cluster through Warp's model, and
 * stores one centroid per topic so the list_topics tool can answer in one
 * call. After that the topics keep themselves current, so once the embedding
 * model has active topics the run controls give way to a note saying so; the
 * server refuses a second seed run either way.
 *
 * Laid out as the backfill beside it is, because it is the same kind of thing:
 * a window, a background job, cancel, and a status that survives a reload.
 */
export default function WarpTopicsSection({
	hasSettingsUpdateAccess,
	configured,
	vectorStoreConnected,
	hasChanges,
	discoveryField,
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

	// Explicitly false only: absent means the vector store could not say, and
	// a guess must not lock the seed run away.
	const [needsBackfillPoll, setNeedsBackfillPoll] = useState(false);
	const { data: indexStatus } = useGetWarpLogIndexStatusQuery(undefined, {
		// Polled faster while waiting on a backfill, so the seed run unlocks soon
		// after it lands rather than up to a minute later.
		pollingInterval: needsBackfillPoll ? 10000 : 60000,
	});
	const health = indexStatus?.topics;
	const needsBackfill = indexStatus?.has_indexed_requests === false;
	useEffect(() => setNeedsBackfillPoll(needsBackfill), [needsBackfill]);
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
	const statusActive = status?.status === "pending" || status?.status === "running" || status?.status === "cancelling";
	// Releasing the pinned id switches to the id-less query, whose cache can
	// still hold this same run as running until its next poll. That copy is
	// stale: adopting it pinned the id again, the pinned query said finished,
	// the id was released again, and the two fed each other until the id-less
	// poll landed - a render loop that crashed the page once anything else in
	// the section added updates of its own.
	const staleRun = statusActive && !!finished && status?.id === finished.id;
	const isActive = statusActive && !staleRun;
	// Seeded: this embedding model has topics, which keep themselves current. A
	// run in flight still shows its controls, so it can be followed or cancelled.
	const seeded = !isActive && (health?.active_topics ?? 0) > 0;
	// A live job always wins; otherwise fall back to the run that just ended.
	// Releasing the pinned id switches back to the id-less query, whose cache
	// can still hold an older run, so the run just finished stays on screen
	// unless the status names that same run, a live one, or a newer one.
	const statusIsCurrent =
		!!status?.id &&
		!staleRun &&
		(!finished || status.id === finished.id || isActive || (status.created_at ?? "") > (finished.created_at ?? ""));
	const shown = statusIsCurrent ? status : finished;

	// Adopt a job discovered by the id-less request, so a reload during a run
	// keeps following that run to its end.
	useEffect(() => {
		if (activeID || !status?.id || staleRun) return;
		if (statusActive) {
			setActiveID(status.id);
		}
	}, [activeID, status?.id, statusActive, staleRun]);

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
			toast.success("Topic seeding started.");
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
				<div className="flex items-center gap-1.5">
					<h4 className="text-sm font-medium">Topic clusters</h4>
					<Tooltip>
						<TooltipTrigger asChild>
							<button
								type="button"
								className="text-muted-foreground hover:text-foreground inline-flex"
								aria-label="How topics work"
								data-testid="warp-topics-info"
							>
								<Info className="size-3.5" />
							</button>
						</TooltipTrigger>
						<TooltipContent className="max-w-sm space-y-1.5" data-testid="warp-topics-info-content">
							<p>Topics group requests by what was asked, so Warp can answer what people ask about.</p>
							<p>
								<span className="font-medium">1. Seed, once.</span> A one-time run clusters the indexed requests of a window and names each
								group. Seeding is once per embedding model.
							</p>
							<p>
								<span className="font-medium">2. Filed as they arrive.</span> Every request indexed after that is filed under its nearest
								topic.
							</p>
							<p>
								<span className="font-medium">3. New topics found.</span> Requests that match no topic wait. Once the new topic threshold is
								reached, an hourly pass groups them into new topics or folds them into existing ones.
							</p>
							<p>
								<span className="font-medium">4. Kept tidy.</span> The same pass retires topics with no requests for 30 days and merges
								topics that come to mean the same thing.
							</p>
							<p>Changing the embedding model starts over: its topics are seeded again.</p>
						</TooltipContent>
					</Tooltip>
				</div>
				<p className="text-muted-foreground text-xs">
					Seed topics once from a window of indexed requests. After that they keep themselves up to date as new requests are indexed, with
					no further runs.
				</p>
			</div>

			{seeded ? (
				<p className="text-muted-foreground text-xs" data-testid="warp-topics-seeded">
					Topics are built for this embedding model and keep themselves up to date: new requests are filed as they are indexed, and new
					topics are found among the ones that match none.
				</p>
			) : (
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
									hasChanges ||
									needsBackfill
								}
								data-testid="warp-topics-start-btn"
							>
								{isStarting && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
								Seed topics
							</Button>
						)}
					</div>
				</div>
			)}

			{hasChanges && !seeded && <p className="text-muted-foreground text-xs">Save configuration changes before seeding topics.</p>}
			{needsBackfill && !seeded && !isActive && (
				<p className="text-muted-foreground text-xs" data-testid="warp-topics-needs-backfill">
					Nothing is indexed for this embedding model yet. Backfill a window above first, then seed topics from the same window.
				</p>
			)}

			{discoveryField}

			{health && (
				<p className="text-muted-foreground text-xs" data-testid="warp-topics-health">
					{health.active_topics} active topic{health.active_topics === 1 ? "" : "s"}
					{health.themes > 0 ? ` in ${health.themes} theme${health.themes === 1 ? "" : "s"}` : ""} · {health.pooled} request
					{health.pooled === 1 ? "" : "s"} waiting for a topic
					{health.requests_24h > 0
						? ` · ${Math.round(health.unmatched_share_24h * 100)}% of the last day's ${health.requests_24h} request${health.requests_24h === 1 ? "" : "s"} matched no topic`
						: " · nothing filed in the last day"}
				</p>
			)}
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
						{shown.themes ? ` · ${shown.themes} theme${shown.themes === 1 ? "" : "s"}` : ""}
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