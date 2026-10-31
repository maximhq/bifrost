export type SidekiqJobStatus = "pending" | "running" | "completed" | "failed" | "cancelled";

// Progress is absent for job kinds that do not report any (status only).
export interface SidekiqJobProgress {
	done: number;
	total: number;
}

// A background job as returned by GET /api/sidekiq/jobs.
export interface SidekiqJob {
	id: string;
	kind: string;
	status: SidekiqJobStatus;
	progress?: SidekiqJobProgress;
	message?: string;
	last_error?: string;
	cancellable: boolean;
	created_at: string;
	started_at?: string;
	updated_at: string;
	completed_at?: string;
}

export interface SidekiqJobsResponse {
	jobs: SidekiqJob[];
}

export interface CancelSidekiqJobResponse {
	cancelled: boolean;
}