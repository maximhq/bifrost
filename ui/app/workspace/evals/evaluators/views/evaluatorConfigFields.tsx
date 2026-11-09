import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { CodeEditor } from "@/components/ui/codeEditor";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import type {
	AIEvaluatorConfig,
	APIEvaluatorConfig,
	APIEvaluatorHeader,
	EvalKey,
	EvaluatorConfig,
	EvaluatorType,
	GradingStyle,
	HumanEvaluatorConfig,
	PassCriteria,
	ProgrammaticEvaluatorConfig,
	StatisticalEvaluatorConfig,
} from "@/lib/types/evals";
import { cn } from "@/lib/utils";
import { Info, Plus, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { EvalModelFields } from "./evalModelFields";
import {
	API_METHODS,
	DEFAULT_SCALE,
	defaultEntryCriterion,
	defaultRunCriterion,
	GRADING_STYLES,
	gradingStyleOf,
	withGradingStyle,
} from "./evaluatorUtils";
import { InstructionsEditor } from "./instructionsEditor";
import { NumberField, PassCriteriaFields } from "./passCriteriaFields";

// Variable references as a user types them. They are passed into the translated text as
// values, because the catalog's own double braces are its interpolation syntax.
const BRACE_EXAMPLES = { input: "{{input}}", output: "{{output}}" };

interface EvaluatorConfigFieldsProps {
	type: EvaluatorType;
	// Set for an evaluator installed from the catalog: its prompt or metric is fixed.
	builtinKey?: string;
	config: EvaluatorConfig;
	onChange: (config: EvaluatorConfig) => void;
	evalKeys: EvalKey[];
	evalKeysKnown: boolean;
	disabled?: boolean;
}

function Section({ title, hint, children }: { title: string; hint?: string; children: React.ReactNode }) {
	return (
		<section className="flex flex-col gap-3">
			<div className="flex flex-col gap-0.5">
				<h2 className="text-sm font-semibold">{title}</h2>
				{hint && <p className="text-muted-foreground text-xs">{hint}</p>}
			</div>
			{children}
		</section>
	);
}

/** The cards that pick what kind of score an evaluator gives. */
function ScoreTypePicker({
	value,
	onChange,
	disabled,
}: {
	value: GradingStyle;
	onChange: (style: GradingStyle) => void;
	disabled?: boolean;
}) {
	const { t } = useTranslation("config");
	return (
		<div className="grid grid-cols-2 gap-2 xl:grid-cols-4" role="radiogroup" aria-label={t("evals.fields.scoreType")}>
			{GRADING_STYLES.map((style) => (
				<button
					key={style}
					type="button"
					role="radio"
					aria-checked={value === style}
					disabled={disabled}
					onClick={() => value !== style && onChange(style)}
					className={cn(
						"flex cursor-pointer flex-col gap-0.5 rounded-sm border p-2.5 text-left transition-colors disabled:cursor-not-allowed disabled:opacity-60",
						value === style ? "border-primary bg-primary/5" : "hover:bg-muted/50",
					)}
					data-testid={`evaluator-score-type-${style}`}
				>
					<span className="text-sm font-medium">{t(`evals.scoreTypes.${style}.label`)}</span>
					<span className="text-muted-foreground text-xs">{t(`evals.scoreTypes.${style}.hint`)}</span>
				</button>
			))}
		</div>
	);
}

function ScaleFields({
	scale,
	onChange,
	disabled,
}: {
	scale: { min: number; max: number } | undefined;
	onChange: (scale: { min: number; max: number }) => void;
	disabled?: boolean;
}) {
	const { t } = useTranslation("config");
	const range = scale ?? DEFAULT_SCALE;
	return (
		<div className="flex flex-wrap items-center gap-2 text-sm">
			<span>{t("evals.fields.scaleFrom")}</span>
			<NumberField
				value={range.min}
				onChange={(min) => onChange({ ...range, min })}
				disabled={disabled}
				className="w-20"
				ariaLabel={t("evals.fields.scaleMin")}
				testId="evaluator-scale-min-input"
			/>
			<span>{t("evals.fields.scaleTo")}</span>
			<NumberField
				value={range.max}
				onChange={(max) => onChange({ ...range, max })}
				disabled={disabled}
				className="w-20"
				ariaLabel={t("evals.fields.scaleMax")}
				testId="evaluator-scale-max-input"
			/>
		</div>
	);
}

/** The pass rules of a config, filled in when a stored config has none. */
function criteriaOf(type: EvaluatorType, config: EvaluatorConfig): PassCriteria {
	return config.pass_criteria ?? { entry_level: defaultEntryCriterion(gradingStyleOf(type, config)), run_level: defaultRunCriterion() };
}

/** The form for one evaluator's config. Which fields show depends on the type. */
export function EvaluatorConfigFields({
	type,
	builtinKey,
	config,
	onChange,
	evalKeys,
	evalKeysKnown,
	disabled,
}: EvaluatorConfigFieldsProps) {
	const { t } = useTranslation("config");
	const style = gradingStyleOf(type, config);
	const isBuiltin = Boolean(builtinKey);

	const passCriteria = (
		<Section title={t("evals.sections.passCriteria")} hint={t("evals.sections.passCriteriaHint")}>
			<PassCriteriaFields
				style={style}
				criteria={criteriaOf(type, config)}
				onChange={(pass_criteria) => onChange({ ...config, pass_criteria })}
				disabled={disabled}
				testIdPrefix="evaluator"
			/>
		</Section>
	);

	if (type === "ai") {
		const ai = config as AIEvaluatorConfig;
		return (
			<>
				{isBuiltin ? (
					<Alert variant="info" data-testid="evaluator-builtin-notice">
						<Info className="size-4" />
						<AlertDescription>{t("evals.editor.builtinPromptFixed")}</AlertDescription>
					</Alert>
				) : (
					<>
						<Section title={t("evals.sections.instructions")} hint={t("evals.sections.instructionsHint", BRACE_EXAMPLES)}>
							<InstructionsEditor
								value={ai.instructions}
								onChange={(instructions) => onChange({ ...ai, instructions })}
								disabled={disabled}
								ariaLabel={t("evals.sections.instructions")}
								placeholder={t("evals.fields.instructionsPlaceholder", BRACE_EXAMPLES)}
								testId="evaluator-instructions-input"
							/>
						</Section>
						<Section title={t("evals.fields.scoreType")}>
							<ScoreTypePicker value={style} onChange={(next) => onChange(withGradingStyle(type, ai, next))} disabled={disabled} />
							{style === "scale" && <ScaleFields scale={ai.scale} onChange={(scale) => onChange({ ...ai, scale })} disabled={disabled} />}
						</Section>
					</>
				)}
				<Section title={t("evals.sections.judge")}>
					<EvalModelFields
						kind="judge"
						provider={ai.provider}
						model={ai.model}
						onChange={(next) => onChange({ ...ai, ...next })}
						evalKeys={evalKeys}
						evalKeysKnown={evalKeysKnown}
						disabled={disabled}
						testIdPrefix="evaluator-judge"
					/>
				</Section>
				{passCriteria}
			</>
		);
	}

	if (type === "programmatic") {
		const programmatic = config as ProgrammaticEvaluatorConfig;
		return (
			<>
				<Section title={t("evals.sections.code")} hint={t("evals.sections.codeHint")}>
					<div className="overflow-hidden rounded-sm border" data-testid="evaluator-code-editor">
						<CodeEditor
							className="z-0 w-full"
							minHeight={220}
							maxHeight={480}
							lang="python"
							code={programmatic.code}
							readonly={disabled}
							onChange={(code) => onChange({ ...programmatic, code })}
							options={{ scrollBeyondLastLine: false, lineNumbers: "on", alwaysConsumeMouseWheel: false }}
						/>
					</div>
				</Section>
				<Section title={t("evals.fields.returnType")} hint={t("evals.fields.returnTypeHint")}>
					<ScoreTypePicker value={style} onChange={(next) => onChange(withGradingStyle(type, programmatic, next))} disabled={disabled} />
				</Section>
				{passCriteria}
			</>
		);
	}

	if (type === "api") {
		const api = config as APIEvaluatorConfig;
		const headers = api.headers ?? [];
		const setHeaders = (next: APIEvaluatorHeader[]) => onChange({ ...api, headers: next });
		return (
			<>
				<Section title={t("evals.sections.request")} hint={t("evals.sections.requestHint")}>
					<div className="flex gap-2">
						<Select value={api.method} onValueChange={(method) => onChange({ ...api, method })} disabled={disabled}>
							<SelectTrigger className="w-28 shrink-0" aria-label={t("evals.fields.method")} data-testid="evaluator-api-method-select">
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								{API_METHODS.map((method) => (
									<SelectItem key={method} value={method}>
										{method}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
						<Input
							value={api.url}
							onChange={(e) => onChange({ ...api, url: e.target.value })}
							disabled={disabled}
							aria-label={t("evals.fields.url")}
							placeholder="https://scorer.example.com/v1/score" // i18n-ignore
							className="font-mono"
							data-testid="evaluator-api-url-input"
						/>
					</div>
				</Section>

				<Section title={t("evals.sections.headers")} hint={t("evals.sections.headersHint")}>
					{headers.map((header, index) => (
						<div key={index} className="flex gap-2">
							<Input
								value={header.key}
								onChange={(e) => setHeaders(headers.map((h, i) => (i === index ? { ...h, key: e.target.value } : h)))}
								disabled={disabled}
								aria-label={t("evals.fields.headerName")}
								placeholder="Authorization" // i18n-ignore
								className="w-1/3 font-mono"
								data-testid={`evaluator-api-header-key-${index}`}
							/>
							<Input
								value={header.value}
								onChange={(e) => setHeaders(headers.map((h, i) => (i === index ? { ...h, value: e.target.value } : h)))}
								disabled={disabled}
								aria-label={t("evals.fields.headerValue")}
								className="flex-1 font-mono"
								autoComplete="off"
								data-testid={`evaluator-api-header-value-${index}`}
							/>
							<Button
								type="button"
								variant="ghost"
								size="icon"
								className="size-9 shrink-0"
								disabled={disabled}
								onClick={() => setHeaders(headers.filter((_, i) => i !== index))}
								aria-label={t("evals.fields.removeHeader")}
								data-testid={`evaluator-api-header-remove-${index}`}
							>
								<Trash2 className="size-4" />
							</Button>
						</div>
					))}
					<div>
						<Button
							type="button"
							variant="outline"
							size="sm"
							disabled={disabled}
							onClick={() => setHeaders([...headers, { key: "", value: "" }])}
							data-testid="evaluator-api-header-add"
						>
							<Plus className="size-3" />
							{t("evals.fields.addHeader")}
						</Button>
					</div>
				</Section>

				{api.method !== "GET" && (
					<Section title={t("evals.sections.payload")} hint={t("evals.sections.payloadHint", BRACE_EXAMPLES)}>
						<div className="overflow-hidden rounded-sm border" data-testid="evaluator-api-payload-editor">
							<CodeEditor
								className="z-0 w-full"
								minHeight={140}
								maxHeight={360}
								lang="json"
								code={api.payload ?? ""}
								readonly={disabled}
								onChange={(payload) => onChange({ ...api, payload })}
								options={{ scrollBeyondLastLine: false, lineNumbers: "on", alwaysConsumeMouseWheel: false }}
							/>
						</div>
					</Section>
				)}

				<Section title={t("evals.sections.response")} hint={t("evals.sections.responseHint")}>
					<div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
						<div className="flex flex-col gap-1.5">
							<Label htmlFor="evaluator-api-score-path">{t("evals.fields.scorePath")}</Label>
							<Input
								id="evaluator-api-score-path"
								value={api.output_mapping?.score ?? ""}
								onChange={(e) => onChange({ ...api, output_mapping: { ...api.output_mapping, score: e.target.value } })}
								disabled={disabled}
								placeholder="result.score" // i18n-ignore
								className="font-mono"
								data-testid="evaluator-api-score-path-input"
							/>
						</div>
						<div className="flex flex-col gap-1.5">
							<Label htmlFor="evaluator-api-reasoning-path">{t("evals.fields.reasoningPath")}</Label>
							<Input
								id="evaluator-api-reasoning-path"
								value={api.output_mapping?.reasoning ?? ""}
								onChange={(e) => onChange({ ...api, output_mapping: { ...api.output_mapping, reasoning: e.target.value } })}
								disabled={disabled}
								placeholder="result.reason" // i18n-ignore
								className="font-mono"
								data-testid="evaluator-api-reasoning-path-input"
							/>
						</div>
						<div className="flex flex-col gap-1.5">
							<Label>{t("evals.fields.timeoutMs")}</Label>
							<NumberField
								value={api.timeout_ms || undefined}
								onChange={(timeout_ms) => onChange({ ...api, timeout_ms })}
								disabled={disabled}
								ariaLabel={t("evals.fields.timeoutMs")}
								testId="evaluator-api-timeout-input"
							/>
						</div>
						<div className="flex flex-col gap-1.5">
							<Label>{t("evals.fields.concurrency")}</Label>
							<NumberField
								value={api.concurrency || undefined}
								onChange={(concurrency) => onChange({ ...api, concurrency })}
								disabled={disabled}
								ariaLabel={t("evals.fields.concurrency")}
								testId="evaluator-api-concurrency-input"
							/>
							<p className="text-muted-foreground text-xs">{t("evals.fields.concurrencyHint")}</p>
						</div>
					</div>
				</Section>
				{passCriteria}
			</>
		);
	}

	if (type === "human") {
		const human = config as HumanEvaluatorConfig;
		return (
			<>
				<Alert variant="info" data-testid="evaluator-human-notice">
					<Info className="size-4" />
					<AlertDescription>{t("evals.editor.humanNotice")}</AlertDescription>
				</Alert>
				<Section title={t("evals.sections.reviewerInstructions")} hint={t("evals.sections.reviewerInstructionsHint")}>
					<Textarea
						value={human.instructions ?? ""}
						onChange={(e) => onChange({ ...human, instructions: e.target.value })}
						disabled={disabled}
						aria-label={t("evals.sections.reviewerInstructions")}
						className="min-h-28"
						data-testid="evaluator-instructions-input"
					/>
				</Section>
				<Section title={t("evals.fields.scoreType")}>
					<ScoreTypePicker value={style} onChange={(next) => onChange(withGradingStyle(type, human, next))} disabled={disabled} />
					{style === "scale" && <ScaleFields scale={human.scale} onChange={(scale) => onChange({ ...human, scale })} disabled={disabled} />}
				</Section>
				{passCriteria}
			</>
		);
	}

	// Statistical: always installed from the catalog. Only the embedding model, when the
	// metric needs one, and the pass rules are the user's to set.
	const statistical = config as StatisticalEvaluatorConfig;
	return (
		<>
			<Alert variant="info" data-testid="evaluator-builtin-notice">
				<Info className="size-4" />
				<AlertDescription>{t("evals.editor.builtinMetricFixed")}</AlertDescription>
			</Alert>
			{statistical.embedding_provider !== undefined && (
				<Section title={t("evals.sections.embedding")}>
					<EvalModelFields
						kind="embedding"
						provider={statistical.embedding_provider ?? ""}
						model={statistical.embedding_model ?? ""}
						onChange={(next) => onChange({ ...statistical, embedding_provider: next.provider, embedding_model: next.model })}
						evalKeys={evalKeys}
						evalKeysKnown={evalKeysKnown}
						disabled={disabled}
						testIdPrefix="evaluator-embedding"
					/>
				</Section>
			)}
			{passCriteria}
		</>
	);
}