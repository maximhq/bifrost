import { Button } from "@/components/ui/button";
import { Wallet } from "lucide-react";
import { ArrowUpRight } from "lucide-react";
import { useTranslation } from "react-i18next";

const MODEL_LIMITS_DOCS_URL = "https://docs.getbifrost.ai/features/governance";

interface ModelLimitsEmptyStateProps {
	onAddClick: () => void;
	canCreate?: boolean;
}

export function ModelLimitsEmptyState({ onAddClick, canCreate = true }: ModelLimitsEmptyStateProps) {
	const { t } = useTranslation("models");
	return (
		<div className="flex min-h-[80vh] w-full flex-col items-center justify-center gap-4 py-16 text-center">
			<div className="text-muted-foreground">
				<Wallet className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />
			</div>
			<div className="flex flex-col gap-1">
				<h1 className="text-muted-foreground text-xl font-medium">{t("modelLimits.emptyTitle")}</h1>
				<div className="text-muted-foreground mx-auto mt-2 w-full max-w-[600px] text-sm font-normal">
					{t("modelLimits.emptyDescription")}
				</div>
				<div className="mx-auto mt-6 flex flex-row flex-wrap items-center justify-center gap-2">
					<Button
						variant="outline"
						aria-label={t("modelLimits.readMoreAria")}
						data-testid="model-limits-button-read-more"
						onClick={() => {
							window.open(`${MODEL_LIMITS_DOCS_URL}?utm_source=bfd`, "_blank", "noopener,noreferrer");
						}}
					>
						{t("modelLimits.readMore")} <ArrowUpRight className="text-muted-foreground h-3 w-3" />
					</Button>
					<Button aria-label={t("modelLimits.addFirstAria")} onClick={onAddClick} disabled={!canCreate} data-testid="model-limits-button-create">
						{t("modelLimits.addLimit")}
					</Button>
				</div>
			</div>
		</div>
	);
}
