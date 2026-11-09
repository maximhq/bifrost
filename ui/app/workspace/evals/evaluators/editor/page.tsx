import FullPageLoader from "@/components/fullPageLoader";
import { LoadErrorView } from "@/components/loadErrorView";
import { useGetEvaluatorQuery } from "@/lib/store";
import type { EvaluatorType } from "@/lib/types/evals";
import { parseAsString, useQueryStates } from "nuqs";
import { useTranslation } from "react-i18next";
import { EvaluatorEditor } from "../views/evaluatorEditor";
import { CREATABLE_EVALUATOR_TYPES } from "../views/evaluatorUtils";

/**
 * The evaluator editor. `?id=` opens an existing evaluator; `?type=` starts a new one
 * of that type. The repo's routes carry no path parameters, so both live in the query.
 */
export default function EvaluatorEditorPage() {
	const { t } = useTranslation("config");
	const [{ id, type }] = useQueryStates({ id: parseAsString.withDefault(""), type: parseAsString.withDefault("") });
	const { data, error, isLoading, refetch } = useGetEvaluatorQuery(id, { skip: !id });
	const newType: EvaluatorType = CREATABLE_EVALUATOR_TYPES.includes(type as EvaluatorType) ? (type as EvaluatorType) : "ai";

	if (id && isLoading) {
		return <FullPageLoader />;
	}

	if (id && (error || !data)) {
		return (
			<LoadErrorView entity={t("evals.editor.entity")} error={error} onRetry={() => void refetch()} testId="evaluator-editor-load-error" />
		);
	}

	// Keyed so that switching evaluator or type starts from a clean form.
	return (
		<div className="dark:bg-card no-padding-parent no-border-parent h-[calc(var(--app-content-viewport)_-_var(--app-bottom-padding))]">
			<EvaluatorEditor key={id || `new-${newType}`} evaluator={id ? data?.evaluator : undefined} newType={newType} />
		</div>
	);
}