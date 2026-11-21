import { Label } from "@/components/ui/label";
import { ModelSelector } from "@/components/ui/modelSelector";
import { ProviderSelector } from "@/components/ui/providerSelector";
import type { EvalKey } from "@/lib/types/evals";
import { Link } from "@tanstack/react-router";
import { KeyRound, TriangleAlert } from "lucide-react";
import { useTranslation } from "react-i18next";
import { evalKeyFor } from "./evaluatorUtils";

interface EvalModelFieldsProps {
	// "judge" grades with a chat model, "embedding" embeds the texts it compares.
	kind: "judge" | "embedding";
	provider: string;
	model: string;
	onChange: (next: { provider: string; model: string }) => void;
	evalKeys: EvalKey[];
	// False until the eval keys have loaded, so no warning shows on a guess.
	evalKeysKnown: boolean;
	disabled?: boolean;
	// Renders the dropdowns in place, for use inside a sheet.
	noPortal?: boolean;
	testIdPrefix: string;
}

/**
 * Provider and model for an evaluator that calls a model, with a line that says which
 * key the calls run on. Evaluations only ever use keys marked for them.
 */
export function EvalModelFields({
	kind,
	provider,
	model,
	onChange,
	evalKeys,
	evalKeysKnown,
	disabled,
	noPortal,
	testIdPrefix,
}: EvalModelFieldsProps) {
	const { t } = useTranslation("config");
	const key = evalKeyFor(evalKeys, provider);
	const providerLabelId = `${testIdPrefix}-provider-label`;
	const modelLabelId = `${testIdPrefix}-model-label`;

	return (
		<div className="flex flex-col gap-2">
			<div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
				<div className="flex flex-col gap-1.5">
					<Label id={providerLabelId}>{t(kind === "judge" ? "evals.model.judgeProvider" : "evals.model.embeddingProvider")}</Label>
					<ProviderSelector
						source="configured"
						value={provider}
						onChange={(next: string) => onChange({ provider: next, model: next === provider ? model : "" })}
						disabled={disabled}
						noPortal={noPortal}
						ariaLabelledBy={providerLabelId}
						data-testid={`${testIdPrefix}-provider-select`}
					/>
				</div>
				<div className="flex flex-col gap-1.5">
					<Label id={modelLabelId}>{t(kind === "judge" ? "evals.model.judgeModel" : "evals.model.embeddingModel")}</Label>
					<ModelSelector
						provider={provider || undefined}
						value={model}
						onChange={(next) => onChange({ provider, model: next })}
						disabled={disabled || !provider}
						allowCustomModel
						ariaLabelledBy={modelLabelId}
						data-testid={`${testIdPrefix}-model-select`}
					/>
				</div>
			</div>
			{provider && key && (
				<p className="text-muted-foreground flex items-center gap-1.5 text-xs" data-testid={`${testIdPrefix}-eval-key-note`}>
					<KeyRound className="size-3 shrink-0" />
					{t("evals.model.runsOnKey", { provider, name: key.name, limit: key.concurrency })}
				</p>
			)}
			{provider && !key && evalKeysKnown && (
				<p
					className="flex flex-wrap items-center gap-1.5 text-xs text-amber-700 dark:text-amber-300"
					data-testid={`${testIdPrefix}-no-eval-key-note`}
				>
					<TriangleAlert className="size-3 shrink-0" />
					{t("evals.model.noEvalKey", { provider })}
					<Link to="/workspace/providers" search={{ provider }} className="underline underline-offset-2">
						{t("evals.list.markKey")}
					</Link>
				</p>
			)}
			{!provider && <p className="text-muted-foreground text-xs">{t("evals.model.onlyEvalKeys")}</p>}
		</div>
	);
}