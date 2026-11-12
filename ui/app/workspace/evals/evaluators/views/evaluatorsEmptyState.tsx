import { Button } from "@/components/ui/button";
import { Link } from "@tanstack/react-router";
import { FlaskConical, LibraryBig, Plus } from "lucide-react";
import { useTranslation } from "react-i18next";

/** Shown in place of the table while no evaluator exists and nothing is filtered. */
export function EvaluatorsEmptyState({ canCreate }: { canCreate: boolean }) {
	const { t } = useTranslation("config");

	return (
		<div
			className="flex min-h-[80vh] w-full flex-col items-center justify-center gap-4 py-16 text-center"
			data-testid="evaluators-empty-state"
		>
			<div className="text-muted-foreground">
				<FlaskConical className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />
			</div>
			<div className="flex flex-col gap-1">
				<h1 className="text-muted-foreground text-xl font-medium">{t("evals.list.emptyTitle")}</h1>
				<div className="text-muted-foreground mx-auto mt-2 w-full max-w-[600px] text-sm font-normal">
					{t("evals.list.emptyDescription")}
				</div>
				<div className="mx-auto mt-6 flex flex-row flex-wrap items-center justify-center gap-2">
					<Button asChild variant="outline" data-testid="evaluators-empty-catalog-link">
						<Link to="/workspace/evals/evaluators/catalog">
							<LibraryBig className="h-4 w-4" />
							{t("evals.list.browseCatalog")}
						</Link>
					</Button>
					{canCreate && (
						<Button asChild data-testid="evaluators-empty-create-link">
							<Link to="/workspace/evals/evaluators/editor" search={{ type: "ai" }}>
								<Plus className="h-4 w-4" />
								{t("evals.list.newEvaluator")}
							</Link>
						</Button>
					)}
				</div>
			</div>
		</div>
	);
}