import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { TagInput } from "@/components/ui/tagInput";
import type { Criterion, CriterionOperator, GradingStyle, PassCriteria } from "@/lib/types/evals";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { NUMERIC_OPERATORS, operatorSymbol, RUN_CRITERION_NAMES, SCORE_NO, SCORE_YES } from "./evaluatorUtils";

/**
 * A number field that lets the text be empty or half typed. It reports a number only
 * when the text is one, so the criterion never holds NaN.
 */
export function NumberField({
	value,
	onChange,
	disabled,
	className,
	ariaLabel,
	testId,
}: {
	value: number | undefined;
	onChange: (value: number) => void;
	disabled?: boolean;
	className?: string;
	ariaLabel?: string;
	testId?: string;
}) {
	const [text, setText] = useState(value === undefined ? "" : String(value));

	// Follow a value set from outside, such as a reset, without fighting the user's typing.
	useEffect(() => {
		setText((current) => (current.trim() !== "" && Number(current) === value ? current : value === undefined ? "" : String(value)));
	}, [value]);

	return (
		<Input
			type="number"
			inputMode="decimal"
			step="any"
			value={text}
			disabled={disabled}
			aria-label={ariaLabel}
			className={className}
			data-testid={testId}
			onChange={(e) => {
				setText(e.target.value);
				const parsed = Number(e.target.value);
				if (e.target.value.trim() !== "" && Number.isFinite(parsed)) onChange(parsed);
			}}
		/>
	);
}

function OperatorSelect({
	value,
	onChange,
	disabled,
	ariaLabel,
	testId,
}: {
	value: CriterionOperator;
	onChange: (operator: CriterionOperator) => void;
	disabled?: boolean;
	ariaLabel: string;
	testId: string;
}) {
	return (
		<Select value={value} onValueChange={(next) => onChange(next as CriterionOperator)} disabled={disabled}>
			<SelectTrigger className="w-20" aria-label={ariaLabel} data-testid={testId}>
				<SelectValue />
			</SelectTrigger>
			<SelectContent>
				{NUMERIC_OPERATORS.map((operator) => (
					<SelectItem key={operator} value={operator}>
						{operatorSymbol(operator)}
					</SelectItem>
				))}
			</SelectContent>
		</Select>
	);
}

interface EntryCriterionFieldProps {
	style: GradingStyle;
	criterion: Criterion;
	onChange: (criterion: Criterion) => void;
	disabled?: boolean;
	testIdPrefix: string;
}

/** The rule one entry passes on. Its shape follows the score type. */
export function EntryCriterionField({ style, criterion, onChange, disabled, testIdPrefix }: EntryCriterionFieldProps) {
	const { t } = useTranslation("config");

	if (style === "yes_no") {
		const operator = criterion.operator === "!=" ? "!=" : "=";
		const value = criterion.value === SCORE_NO || criterion.value === false ? SCORE_NO : SCORE_YES;
		return (
			<div className="flex flex-wrap items-center gap-2">
				<Select
					value={operator}
					onValueChange={(next) => onChange({ ...criterion, operator: next as CriterionOperator })}
					disabled={disabled}
				>
					<SelectTrigger className="w-28" aria-label={t("evals.criteria.operator")} data-testid={`${testIdPrefix}-entry-operator`}>
						<SelectValue />
					</SelectTrigger>
					<SelectContent>
						<SelectItem value="=">{t("evals.criteria.is")}</SelectItem>
						<SelectItem value="!=">{t("evals.criteria.isNot")}</SelectItem>
					</SelectContent>
				</Select>
				<Select value={value} onValueChange={(next) => onChange({ ...criterion, value: next })} disabled={disabled}>
					<SelectTrigger className="w-24" aria-label={t("evals.criteria.value")} data-testid={`${testIdPrefix}-entry-value`}>
						<SelectValue />
					</SelectTrigger>
					<SelectContent>
						<SelectItem value={SCORE_YES}>{t("evals.criteria.yes")}</SelectItem>
						<SelectItem value={SCORE_NO}>{t("evals.criteria.no")}</SelectItem>
					</SelectContent>
				</Select>
			</div>
		);
	}

	if (style === "multiselect") {
		const options = criterion.options ?? [];
		const passing = Array.isArray(criterion.value) ? criterion.value : [];
		return (
			<div className="flex flex-col gap-3">
				<div className="flex flex-col gap-1.5">
					<Label htmlFor={`${testIdPrefix}-entry-options`}>{t("evals.criteria.labels")}</Label>
					<TagInput
						id={`${testIdPrefix}-entry-options`}
						value={options}
						readOnly={disabled}
						placeholder={t("evals.criteria.labelsPlaceholder")}
						onValueChange={(next) =>
							// A label that is no longer offered cannot stay a passing one.
							onChange({ ...criterion, operator: "is one of", options: next, value: passing.filter((label) => next.includes(label)) })
						}
						data-testid={`${testIdPrefix}-entry-options`}
					/>
				</div>
				{options.length > 0 && (
					<div className="flex flex-col gap-1.5">
						<Label>{t("evals.criteria.passingLabels")}</Label>
						<div className="flex flex-wrap gap-x-4 gap-y-2">
							{options.map((label) => (
								<label key={label} className="flex cursor-pointer items-center gap-2 text-sm">
									<Checkbox
										checked={passing.includes(label)}
										disabled={disabled}
										onCheckedChange={(checked) =>
											onChange({
												...criterion,
												operator: "is one of",
												value: checked ? [...passing, label] : passing.filter((other) => other !== label),
											})
										}
										data-testid={`${testIdPrefix}-entry-passing-${label}`}
									/>
									{label}
								</label>
							))}
						</div>
					</div>
				)}
			</div>
		);
	}

	const operator = NUMERIC_OPERATORS.includes(criterion.operator) ? criterion.operator : ">=";
	return (
		<div className="flex flex-wrap items-center gap-2">
			<OperatorSelect
				value={operator}
				onChange={(next) => onChange({ ...criterion, operator: next })}
				disabled={disabled}
				ariaLabel={t("evals.criteria.operator")}
				testId={`${testIdPrefix}-entry-operator`}
			/>
			<NumberField
				value={typeof criterion.value === "number" ? criterion.value : undefined}
				onChange={(value) => onChange({ ...criterion, operator, value })}
				disabled={disabled}
				className="w-28"
				ariaLabel={t("evals.criteria.value")}
				testId={`${testIdPrefix}-entry-value`}
			/>
		</div>
	);
}

interface PassCriteriaFieldsProps {
	style: GradingStyle;
	criteria: PassCriteria;
	onChange: (criteria: PassCriteria) => void;
	disabled?: boolean;
	testIdPrefix: string;
}

/** Both pass rules: the one for a single entry and the one for a whole run. */
export function PassCriteriaFields({ style, criteria, onChange, disabled, testIdPrefix }: PassCriteriaFieldsProps) {
	const { t } = useTranslation("config");
	const run = criteria.run_level;
	const runOperator = NUMERIC_OPERATORS.includes(run.operator) ? run.operator : ">=";

	return (
		<div className="grid grid-cols-1 gap-5 lg:grid-cols-2">
			<div className="flex flex-col gap-2">
				<Label>{t("evals.criteria.entryPassesWhen")}</Label>
				<EntryCriterionField
					style={style}
					criterion={criteria.entry_level}
					onChange={(entry_level) => onChange({ ...criteria, entry_level })}
					disabled={disabled}
					testIdPrefix={testIdPrefix}
				/>
			</div>
			<div className="flex flex-col gap-2">
				<Label>{t("evals.criteria.runPassesWhen")}</Label>
				<div className="flex flex-wrap items-center gap-2">
					<Select value={run.name} onValueChange={(name) => onChange({ ...criteria, run_level: { ...run, name } })} disabled={disabled}>
						<SelectTrigger className="w-48" aria-label={t("evals.criteria.runMeasure")} data-testid={`${testIdPrefix}-run-name`}>
							<SelectValue />
						</SelectTrigger>
						<SelectContent>
							{RUN_CRITERION_NAMES.map((name) => (
								<SelectItem key={name} value={name}>
									{t(`evals.criteria.runNames.${name}`)}
								</SelectItem>
							))}
						</SelectContent>
					</Select>
					<OperatorSelect
						value={runOperator}
						onChange={(operator) => onChange({ ...criteria, run_level: { ...run, operator } })}
						disabled={disabled}
						ariaLabel={t("evals.criteria.operator")}
						testId={`${testIdPrefix}-run-operator`}
					/>
					<NumberField
						value={typeof run.value === "number" ? run.value : undefined}
						onChange={(value) => onChange({ ...criteria, run_level: { ...run, operator: runOperator, value } })}
						disabled={disabled}
						className="w-24"
						ariaLabel={t("evals.criteria.value")}
						testId={`${testIdPrefix}-run-value`}
					/>
				</div>
			</div>
		</div>
	);
}