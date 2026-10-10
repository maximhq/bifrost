import { sidekiqJobLabel, sidekiqJobPercent } from "@/components/notificationCenter.utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Progress } from "@/components/ui/progress";
import type { SidekiqJob, SidekiqJobStatus } from "@/lib/types/sidekiq";
import { formatDistanceToNow } from "date-fns";
import { Check, X } from "lucide-react";
import { useState } from "react";

const statusPresentation: Record<
	SidekiqJobStatus,
	{ label: string; variant: "secondary" | "default" | "success" | "destructive" | "outline" }
> = {
	pending: { label: "Queued", variant: "secondary" },
	running: { label: "Running", variant: "default" },
	completed: { label: "Completed", variant: "success" },
	failed: { label: "Failed", variant: "destructive" },
	cancelled: { label: "Cancelled", variant: "outline" },
};

interface SidekiqJobsSectionProps {
	jobs: SidekiqJob[];
	cancellingId?: string;
	onCancel: (id: string) => void;
	onDismiss: (ids: string[]) => void;
}

export default function SidekiqJobsSection({ jobs, cancellingId, onCancel, onDismiss }: SidekiqJobsSectionProps) {
	// Cancelling is final (a cancelled job is not resumable), so it takes a second click.
	const [confirmId, setConfirmId] = useState<string>();

	if (jobs.length === 0) {
		return (
			<section
				data-testid="sidekiq-jobs-section"
				aria-label="Background jobs"
				className="flex h-44 flex-col items-center justify-center gap-2 px-6 text-center"
			>
				<span className="bg-muted text-muted-foreground flex size-9 items-center justify-center rounded-full">
					<Check className="size-4" />
				</span>
				<p className="text-sm font-medium">No background jobs</p>
				<p className="text-muted-foreground text-xs">Long-running tasks will show their progress here.</p>
			</section>
		);
	}

	return (
		<section data-testid="sidekiq-jobs-section" aria-label="Background jobs">
			<ul className="max-h-[min(26rem,calc(100vh-8rem))] divide-y overflow-y-auto">
				{jobs.map((job) => {
					const presentation = statusPresentation[job.status] ?? statusPresentation.pending;
					const percent = sidekiqJobPercent(job);
					const finishedAt = job.completed_at ?? job.updated_at;
					const detail = job.status === "failed" ? job.last_error || job.message : job.message;
					return (
						<li key={job.id} data-testid={`sidekiq-job-${job.id}`} className="group relative flex flex-col gap-1.5 px-4 py-2.5">
							<div className="flex items-center gap-2">
								<span className="min-w-0 flex-1 truncate text-sm font-medium">{sidekiqJobLabel(job.kind)}</span>
								<Badge variant={presentation.variant} data-testid={`sidekiq-job-status-${job.id}`}>
									{presentation.label}
								</Badge>
								{!job.cancellable && (
									<button
										type="button"
										aria-label={`Dismiss ${sidekiqJobLabel(job.kind)}`}
										onClick={() => onDismiss([job.id])}
										data-testid={`sidekiq-job-dismiss-${job.id}`}
										className="text-muted-foreground hover:bg-muted hover:text-foreground -ml-2 flex h-5 w-0 shrink-0 cursor-pointer items-center justify-center overflow-hidden rounded opacity-0 transition-[width,margin,opacity] group-hover:ml-0 group-hover:w-5 group-hover:opacity-100 focus-visible:ml-0 focus-visible:w-5 focus-visible:opacity-100"
									>
										<X className="size-3" />
									</button>
								)}
							</div>
							{percent !== undefined && job.progress && (
								<div className="flex items-center gap-2" data-testid={`sidekiq-job-progress-${job.id}`}>
									<Progress value={percent} aria-label={`${sidekiqJobLabel(job.kind)} progress`} className="h-1.5 flex-1" />
									<span className="text-muted-foreground w-24 text-right text-[11px] tabular-nums">
										{job.status === "completed" ? "Done" : `${job.progress.done.toLocaleString()} / ${job.progress.total.toLocaleString()}`}
									</span>
								</div>
							)}
							{detail && <p className="text-muted-foreground line-clamp-2 text-xs">{detail}</p>}
							<div className="text-muted-foreground flex items-center justify-between text-[11px]">
								<span>
									{job.cancellable ? "Started" : "Finished"}{" "}
									{formatDistanceToNow(new Date(job.cancellable ? job.created_at : finishedAt), { addSuffix: true })}
								</span>
								{job.cancellable &&
									(confirmId === job.id ? (
										<span className="flex items-center gap-1">
											<Button
												variant="destructive"
												className="h-6 px-2 text-xs"
												isLoading={cancellingId === job.id}
												disabled={cancellingId === job.id}
												onClick={() => {
													setConfirmId(undefined);
													onCancel(job.id);
												}}
												dataTestId={`sidekiq-job-cancel-confirm-${job.id}`}
											>
												Stop job
											</Button>
											<Button variant="ghost" className="h-6 px-2 text-xs" onClick={() => setConfirmId(undefined)}>
												Keep
											</Button>
										</span>
									) : (
										<Button
											variant="ghost"
											className="hover:text-destructive h-6 px-2 text-xs"
											disabled={cancellingId === job.id}
											onClick={() => setConfirmId(job.id)}
											dataTestId={`sidekiq-job-cancel-${job.id}`}
										>
											Cancel
										</Button>
									))}
							</div>
						</li>
					);
				})}
			</ul>
		</section>
	);
}