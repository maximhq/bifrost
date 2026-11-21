import type { Criterion } from "@/lib/types/evals";
import { useTranslation } from "react-i18next";
import { criterionParts } from "./evaluatorUtils";

/** A pass rule in words and symbols, such as "score ≥ 0.8". */
export function PassCriterionText({ criterion }: { criterion: Criterion | undefined }) {
	const { t } = useTranslation("config");
	if (!criterion) return <span className="text-muted-foreground">-</span>;
	const parts = criterionParts(criterion);
	return (
		<span className="font-mono text-xs whitespace-nowrap">
			{parts.isOneOf ? t("evals.criteria.scoreIsOneOf", { values: parts.value }) : t("evals.criteria.scoreCompare", parts)}
		</span>
	);
}