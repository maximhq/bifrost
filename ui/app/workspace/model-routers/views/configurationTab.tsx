import { Button } from "@/components/ui/button";
import { LoaderCircle, Save } from "lucide-react";
import { useTranslation } from "react-i18next";
import { toFormValues } from "@/app/workspace/complexity-router/formSchema";
import {
	CLASSIFIER_NAME_KEYS,
	RouterConfigurationActions,
	RouterConfigurationEditor,
	RouterConfigurationRestoreButton,
} from "./routerConfigurationEditor";
import type { RouterConfigForm } from "./useRouterConfigForm";

const DESCRIPTION_KEYS = {
	semantic: "stepSemanticDesc",
	decision: "stepDecisionDesc",
	llm: "stepLlmDesc",
} as const;

// ConfigurationTab edits how the router classifies: which classifier, and
// what it works from. Saving re-serves the router; its models live on the
// Models tab and are saved there.
export function ConfigurationTab({ config }: { config: RouterConfigForm }) {
	const { t } = useTranslation("models");
	const { t: tcpx } = useTranslation("models", { keyPrefix: "routing.complexityUi" });
	const { live, derived, saved, staging, canUpdate, isSaving, canSave } = config;
	const classifier = live.classifier;
	const savedClassifier = saved ? toFormValues(saved).classifier : classifier;

	// Says why Save is off when the decision provider blocks it; otherwise
	// calls out what the save will do that is easy to miss.
	const note = derived.blockedOnDecisionProvider
		? classifier === "semantic"
			? tcpx("footerDecisionIsFallback")
			: tcpx("footerDecisionNeedsProvider")
		: staging.count > 0
			? tcpx("suggestions.footerStaged", { count: staging.count })
			: classifier !== savedClassifier
				? // Suggestions only run on the semantic classifier, so leaving it
					// turns saved-on suggestions off; say so before the save does it.
					classifier !== "semantic" && saved?.suggestions?.enabled
					? tcpx("footerClassifierChangedSuggestionsOff", { classifier: tcpx(CLASSIFIER_NAME_KEYS[classifier]) })
					: tcpx("footerClassifierChanged", { classifier: tcpx(CLASSIFIER_NAME_KEYS[classifier]) })
				: "";

	return (
		<form
			className="flex min-h-0 grow flex-col"
			onSubmit={(event) => {
				event.preventDefault();
				void config.submit();
			}}
			noValidate
		>
			<div className="custom-scrollbar min-h-0 flex-1 space-y-6 overflow-y-auto pb-6">
				<div className="flex w-full flex-col gap-3 md:flex-row md:items-start md:justify-between">
					<div className="min-w-0">
						<h2 className="text-lg font-semibold">{tcpx("stepSetup")}</h2>
						<p className="text-muted-foreground text-sm">{tcpx(DESCRIPTION_KEYS[classifier])}</p>
					</div>
					<div className="flex shrink-0 flex-wrap items-center gap-2">
						<RouterConfigurationActions config={config} />
					</div>
				</div>
				<RouterConfigurationEditor config={config} showClassifierChoice />
			</div>

			<div className="flex flex-wrap items-center justify-end gap-2.5 border-t pt-4">
				<p className="mr-auto text-xs text-amber-700 dark:text-amber-400" role="status" data-testid="complexity-router-footer-note">
					{note}
				</p>
				<RouterConfigurationRestoreButton config={config} />
				<Button
					data-testid="complexity-router-discard-changes-button"
					type="button"
					variant="outline"
					size="sm"
					onClick={config.discard}
					disabled={!derived.isDirty || isSaving || config.restore.isResetting}
				>
					{tcpx("discardChanges")}
				</Button>
				<Button data-testid="complexity-router-save-changes-button" type="submit" size="sm" disabled={!canSave || isSaving}>
					{isSaving ? <LoaderCircle className="h-3.5 w-3.5 animate-spin" /> : <Save className="h-3.5 w-3.5" />}
					{isSaving ? t("routing.saving") : tcpx("saveChanges")}
				</Button>
			</div>
		</form>
	);
}