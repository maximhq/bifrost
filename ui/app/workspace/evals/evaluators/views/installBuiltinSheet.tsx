import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { useToast } from "@/hooks/use-toast";
import { getErrorMessage, useInstallBuiltinEvaluatorMutation } from "@/lib/store";
import type { CatalogEntry, Criterion, EvalKey } from "@/lib/types/evals";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { EvalModelFields } from "./evalModelFields";
import { catalogModelNeed } from "./evaluatorUtils";
import { EntryCriterionField } from "./passCriteriaFields";

interface InstallBuiltinSheetProps {
	// The built-in being added, or null while the sheet is closed.
	entry: CatalogEntry | null;
	evalKeys: EvalKey[];
	evalKeysKnown: boolean;
	onClose: () => void;
	onInstalled: (evaluatorId: string) => void;
}

/** Adds a built-in to the user's evaluators. Its prompt or metric is fixed; the model and pass rule are theirs. */
export function InstallBuiltinSheet({ entry, evalKeys, evalKeysKnown, onClose, onInstalled }: InstallBuiltinSheetProps) {
	const { t } = useTranslation("config");
	const { t: tCommon } = useTranslation("common");
	const { toast } = useToast();
	const [install, { isLoading }] = useInstallBuiltinEvaluatorMutation();
	const [name, setName] = useState("");
	const [provider, setProvider] = useState("");
	const [model, setModel] = useState("");
	const [criterion, setCriterion] = useState<Criterion | null>(null);

	// Start each built-in from its own defaults.
	useEffect(() => {
		if (!entry) return;
		setName(entry.name);
		setProvider("");
		setModel("");
		setCriterion(entry.pass_criteria.entry_level);
	}, [entry]);

	if (!entry || !criterion) {
		return <Sheet open={false} />;
	}

	const need = catalogModelNeed(entry);
	const missingModel = need !== "none" && (!provider || !model);
	const canSubmit = name.trim() !== "" && !missingModel && !isLoading;

	const handleSubmit = async (e: React.FormEvent) => {
		e.preventDefault();
		if (!canSubmit) return;
		try {
			const result = await install({
				key: entry.key,
				data: {
					name: name.trim(),
					provider: need === "none" ? undefined : provider,
					model: need === "none" ? undefined : model,
					pass_criteria: { entry_level: criterion, run_level: entry.pass_criteria.run_level },
				},
			}).unwrap();
			toast({ title: t("evals.catalog.added", { name: result.evaluator.name }) });
			onInstalled(result.evaluator.id);
		} catch (error) {
			toast({ title: tCommon("error"), description: getErrorMessage(error), variant: "destructive" });
		}
	};

	return (
		<Sheet open onOpenChange={(open) => !open && !isLoading && onClose()}>
			<SheetContent className="flex w-full flex-col gap-0 overflow-y-auto p-8 sm:max-w-xl" data-testid="evaluator-install-sheet">
				<SheetHeader className="flex-col items-start gap-1">
					<SheetTitle>{t("evals.catalog.addTitle", { name: entry.name })}</SheetTitle>
					<SheetDescription>
						{t(need === "none" ? "evals.catalog.addDescriptionMetric" : "evals.catalog.addDescriptionModel")}
					</SheetDescription>
				</SheetHeader>
				<form onSubmit={(e) => void handleSubmit(e)} className="flex grow flex-col gap-5">
					<div className="flex flex-col gap-1.5">
						<Label htmlFor="evaluator-install-name">{t("evals.fields.name")}</Label>
						<Input
							id="evaluator-install-name"
							value={name}
							onChange={(e) => setName(e.target.value)}
							maxLength={255}
							data-testid="evaluator-install-name-input"
						/>
					</div>

					{need !== "none" && (
						<EvalModelFields
							kind={need}
							provider={provider}
							model={model}
							onChange={(next) => {
								setProvider(next.provider);
								setModel(next.model);
							}}
							evalKeys={evalKeys}
							evalKeysKnown={evalKeysKnown}
							noPortal
							testIdPrefix="evaluator-install"
						/>
					)}

					<div className="flex flex-col gap-2">
						<Label>{t(entry.lower_is_better ? "evals.catalog.passesWhenLower" : "evals.criteria.entryPassesWhen")}</Label>
						<EntryCriterionField style="number" criterion={criterion} onChange={setCriterion} testIdPrefix="evaluator-install" />
						<p className="text-muted-foreground text-xs">{t("evals.catalog.scoreRange", { scale: entry.scale })}</p>
					</div>

					<div className="flex flex-col gap-1.5">
						<Label>{t("evals.catalog.whatItReads")}</Label>
						<p className="font-mono text-xs" data-testid="evaluator-install-variables">
							{entry.variables.join(", ")}
						</p>
						{entry.optional_variables && entry.optional_variables.length > 0 && (
							<p className="text-muted-foreground font-mono text-xs">
								{t("evals.catalog.optionalVariables", { variables: entry.optional_variables.join(", ") })}
							</p>
						)}
					</div>

					<div className="mt-auto flex justify-end gap-2 pt-4">
						<Button type="button" variant="outline" onClick={onClose} disabled={isLoading} data-testid="evaluator-install-cancel">
							{tCommon("cancel")}
						</Button>
						<Button type="submit" disabled={!canSubmit} data-testid="evaluator-install-submit">
							{isLoading ? t("evals.catalog.adding") : t("evals.catalog.addEvaluator")}
						</Button>
					</div>
				</form>
			</SheetContent>
		</Sheet>
	);
}