// Types for test runs: a target run on every row of a dataset version and scored by evaluators.

import type { EvaluatorType } from "@/lib/types/evals";

export type RunStatus = "queued" | "running" | "completed" | "failed" | "stopped";
export type RunEntryStatus = "queued" | "running" | "evaluating" | "completed" | "failed" | "stopped";
export type RunResultStatus = "queued" | "running" | "completed" | "failed" | "not_applicable" | "awaiting_review";
export type RunTargetType = "dataset" | "prompt" | "model" | "http";

export const RUN_TARGET_TYPES: RunTargetType[] = ["prompt", "model", "http", "dataset"];

// The request an HTTP target makes, in the shape of an API evaluator's config. The
// value at output_mapping.score is the output.
export interface RunHTTPTarget {
	method: string;
	url: string;
	headers?: { key: string; value: string }[];
	payload?: string;
	output_mapping: { score: string };
	timeout_ms?: number;
}

export interface RunTarget {
	type: RunTargetType;
	prompt_id?: string;
	// 0 or absent in a request means the latest version. A stored run has the number.
	prompt_version?: number;
	provider?: string;
	model?: string;
	system?: string;
	params?: Record<string, unknown>;
	http?: RunHTTPTarget;
}

export interface RunEvaluatorRef {
	id: string;
	version?: number;
}

// An evaluator as a run stores it: a copy of the definition, secrets redacted.
export interface RunEvaluator {
	id: string;
	name: string;
	type: EvaluatorType;
	version: number;
}

export interface RunInput {
	name: string;
	tags?: string[];
	target: RunTarget;
	dataset_id: string;
	// 0 or absent means the dataset as it is now: unsaved edits are saved as a version first.
	dataset_version?: number;
	split_id?: string;
	evaluators: RunEvaluatorRef[];
	target_concurrency?: number;
}

export interface RunCheck {
	valid: boolean;
	problems: string[];
	// The variables the run provides to its evaluators.
	variables: string[];
}

export interface RunEvaluatorSummary {
	evaluator_id: string;
	name: string;
	type: EvaluatorType;
	version: number;
	total: number;
	scored: number;
	na: number;
	errors: number;
	passed: number;
	pass_rate: number;
	mean?: number;
	median?: number;
	min?: number;
	max?: number;
	// Results still queued, running or waiting for a reviewer.
	pending: number;
	// The run-level verdict. Absent while the run is active.
	run_pass?: boolean;
}

export interface RunLatency {
	count: number;
	min: number;
	max: number;
	mean: number;
	p50: number;
	p90: number;
	p95: number;
	p99: number;
}

export interface RunSummary {
	evaluators: RunEvaluatorSummary[];
	latency?: RunLatency;
	totals: { target_tokens: number; target_cost: number; evaluator_tokens: number; evaluator_cost: number };
}

export interface Run {
	id: string;
	name: string;
	tags?: string[];
	status: RunStatus;
	target_type: RunTargetType;
	target: RunTarget;
	dataset_id: string;
	dataset_version: number;
	split_id?: string;
	evaluators: RunEvaluator[];
	total: number;
	// Entries by status.
	counts: Partial<Record<RunEntryStatus, number>>;
	// Completed entries that passed every evaluator with a pass rule.
	passed: number;
	summary?: RunSummary;
	error?: string;
	created_by?: string;
	created_at: string;
	updated_at: string;
	started_at?: string;
	ended_at?: string;
}

export interface RunScore {
	status: "ok" | "na" | "error";
	number?: number;
	bool?: boolean;
	string?: string;
}

export interface RunResult {
	id: string;
	entry_id: string;
	evaluator_id: string;
	status: RunResultStatus;
	score?: RunScore;
	pass?: boolean;
	reasoning?: string;
	latency_ms: number;
	total_tokens: number;
	cost?: number;
	error?: string;
	reviewed_by?: string;
}

export interface RunEntry {
	id: string;
	run_id: string;
	seq: number;
	dataset_row_id: string;
	status: RunEntryStatus;
	attempt: number;
	variables: Record<string, string>;
	output: string;
	tool_calls?: string;
	context?: string;
	passed?: boolean;
	target_latency_ms: number;
	target_total_tokens: number;
	target_cost?: number;
	error?: string;
	results: RunResult[];
	// The baseline run's entry for the same dataset row, when the entries were read
	// against a baseline and that run has the row.
	baseline?: RunEntry;
}

export interface RunEntriesPage {
	entries: RunEntry[];
	total: number;
}

export interface ListRunsParams {
	search?: string;
	status?: string;
	dataset_id?: string;
	tag?: string;
	limit?: number;
	offset?: number;
}

export interface ListRunsResponse {
	runs: Run[];
	total: number;
	limit: number;
	offset: number;
}

export interface ListRunEntriesParams {
	id: string;
	status?: string;
	passed?: boolean;
	search?: string;
	// Another run on the same dataset to read the entries against.
	baseline?: string;
	// With a baseline: only rows that scored lower, or higher, than it.
	change?: "worse" | "better";
	offset?: number;
	limit?: number;
}

export interface RunComparedRow {
	dataset_row_id: string;
	variables: Record<string, string>;
	// One per run, in the order the runs were given. Null for a run without the row.
	entries: (RunEntry | null)[];
}

// How a run's scores moved against the baseline, for one evaluator, over every row.
export interface RunScoreChange {
	improved: number;
	regressed: number;
	compared: number;
}

export interface RunComparison {
	// The first run is the baseline the others are measured against.
	runs: Run[];
	rows: RunComparedRow[];
	total: number;
	// Keyed by run ID, then evaluator ID. The baseline has no entry.
	changes: Record<string, Record<string, RunScoreChange>>;
}