// Types for the evaluators API (/api/evals). They mirror framework/evals in the gateway.

export type EvaluatorType = "ai" | "programmatic" | "statistical" | "api" | "human";

export type GradingStyle = "yes_no" | "scale" | "multiselect" | "number";

export type CriterionOperator = "=" | "!=" | ">" | "<" | ">=" | "<=" | "is one of";

export type RunCriterionName = "mean_score" | "median_score" | "minimum_score" | "maximum_score" | "queries_passed";

export interface Criterion {
	name: string;
	operator: CriterionOperator;
	value: number | string | boolean | string[];
	options?: string[];
}

export interface PassCriteria {
	entry_level: Criterion;
	run_level: Criterion;
}

/** Fields shared by every evaluator config. */
export interface EvaluatorCommonConfig {
	pass_criteria?: PassCriteria;
	reversed?: boolean;
	variables?: Record<string, string>;
	optional_variables?: Record<string, string>;
}

export interface AIEvaluatorConfig extends EvaluatorCommonConfig {
	instructions: string;
	grading_style: GradingStyle;
	scale?: { min: number; max: number };
	provider: string;
	model: string;
	model_params?: Record<string, unknown>;
}

export interface ProgrammaticEvaluatorConfig extends EvaluatorCommonConfig {
	code: string;
	response_type: GradingStyle;
}

export interface APIEvaluatorHeader {
	key: string;
	value: string;
}

export interface APIEvaluatorConfig extends EvaluatorCommonConfig {
	method: string;
	url: string;
	headers?: APIEvaluatorHeader[];
	params?: { key: string; value: string }[];
	payload?: string;
	output_mapping: { score: string; reasoning?: string };
	timeout_ms?: number;
	concurrency?: number;
}

export interface StatisticalEvaluatorConfig extends EvaluatorCommonConfig {
	embedding_provider?: string;
	embedding_model?: string;
}

export interface HumanEvaluatorConfig extends EvaluatorCommonConfig {
	instructions?: string;
	evaluation_type: GradingStyle;
	scale?: { min: number; max: number };
}

export type EvaluatorConfig =
	| AIEvaluatorConfig
	| ProgrammaticEvaluatorConfig
	| APIEvaluatorConfig
	| StatisticalEvaluatorConfig
	| HumanEvaluatorConfig;

/** An evaluator as a listing returns it, without its config. */
export interface Evaluator {
	id: string;
	name: string;
	description: string;
	type: EvaluatorType;
	builtin_key?: string;
	current_version: number;
	tags?: string[];
	created_by?: string;
	created_at: string;
	updated_at: string;
}

/** An evaluator as a listing returns it: no config, only the facts a list shows. */
export interface EvaluatorSummary extends Evaluator {
	/** Names of the inputs the evaluator requires. */
	variables: string[];
	/** The entry-level rule. Absent when the stored config no longer parses. */
	pass_criterion?: Criterion;
	lower_is_better: boolean;
	/** Judge or embedding provider, for an evaluator that calls a model. */
	provider?: string;
	model?: string;
	/** Method and host of an API evaluator. */
	endpoint?: string;
}

/** A provider key marked "use for evaluations". */
export interface EvalKey {
	key_id: string;
	name: string;
	provider: string;
	/** The most evaluations that run on this key at once. */
	concurrency: number;
}

export interface ListEvalKeysResponse {
	keys: EvalKey[];
}

export type EvaluatorSource = "custom" | "builtin";

/** Whether an evaluator can run: "no_key" calls a model whose provider has no eval key. */
export type EvaluatorKeyStatus = "ready" | "no_key";

/** An evaluator with the config of one version. Secret header values arrive redacted. */
export interface EvaluatorWithConfig extends Evaluator {
	version: number;
	config: EvaluatorConfig;
	variables: string[];
	optional_variables?: string[];
	executable: boolean;
}

export interface EvaluatorVersion {
	id: string;
	evaluator_id: string;
	version: number;
	config: EvaluatorConfig;
	commit_message?: string;
	created_by?: string;
	created_at: string;
}

export interface EvaluatorInput {
	name: string;
	description?: string;
	type?: EvaluatorType;
	tags?: string[];
	config?: EvaluatorConfig;
	commit_message?: string;
}

export interface ListEvaluatorsParams {
	/** Any of these types. Empty means every type. */
	types?: string[];
	source?: EvaluatorSource;
	status?: EvaluatorKeyStatus;
	search?: string;
	tag?: string;
	limit?: number;
	offset?: number;
}

export interface ListEvaluatorsResponse {
	evaluators: EvaluatorSummary[];
	total: number;
	limit: number;
	offset: number;
}

export interface EvaluatorResponse {
	evaluator: EvaluatorWithConfig;
}

export interface EvaluatorVersionsResponse {
	versions: EvaluatorVersion[];
}

export interface CatalogEntry {
	key: string;
	name: string;
	description: string;
	category: string;
	type: "ai" | "statistical";
	variables: string[];
	optional_variables?: string[];
	lower_is_better: boolean;
	needs_judge_model: boolean;
	needs_embedding_model: boolean;
	pass_criteria: PassCriteria;
	scale: string;
}

export interface CatalogResponse {
	catalog: CatalogEntry[];
}

export interface InstallBuiltinRequest {
	name?: string;
	description?: string;
	tags?: string[];
	provider?: string;
	model?: string;
	model_params?: Record<string, unknown>;
	pass_criteria?: PassCriteria;
}

export interface ValidateEvaluatorRequest {
	type: EvaluatorType;
	builtin_key?: string;
	config: EvaluatorConfig | Record<string, unknown>;
}

export interface ValidateEvaluatorResponse {
	valid: boolean;
	error?: string;
	variables: string[];
	optional_variables?: string[];
	config?: EvaluatorConfig;
}

export type ScoreStatus = "ok" | "na" | "error";

/** An evaluator's verdict. Exactly one of number, bool and string is set when status is "ok". */
export interface EvaluationScore {
	status: ScoreStatus;
	number?: number;
	bool?: boolean;
	string?: string;
}

export interface EvaluationResult {
	evaluator_id?: string;
	evaluator_version?: number;
	score: EvaluationScore;
	reasoning?: string;
	pass?: boolean;
	usage?: { prompt_tokens: number; completion_tokens: number; total_tokens: number };
	cost?: number;
	latency_ms: number;
	logs?: string[];
}

export type ExecutionStatus = "queued" | "running" | "completed";

export interface EvaluationExecution {
	id: string;
	status: ExecutionStatus;
	evaluator_id?: string;
	evaluator_version?: number;
	key_id?: string;
	result?: EvaluationResult;
	created_at: string;
	started_at?: string;
	completed_at?: string;
}

export interface ExecutionResponse {
	execution: EvaluationExecution;
}

export interface DraftEvaluator {
	type: EvaluatorType;
	builtin_key?: string;
	config: EvaluatorConfig | Record<string, unknown>;
	/** The evaluator being edited, so a draft that carries redacted secrets runs with the stored ones. */
	evaluator_id?: string;
}

export interface ExecuteDraftRequest {
	draft: DraftEvaluator;
	variables: Record<string, string>;
	wait_ms?: number;
}

export interface ExecuteEvaluatorRequest {
	variables: Record<string, string>;
	version?: number;
	wait_ms?: number;
}