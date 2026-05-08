import { Button } from "@/components/ui/button";
import { ArrowUpRight, SlidersHorizontal } from "lucide-react";
import { useTranslation } from "react-i18next";

const PRICING_OVERRIDES_DOCS_URL = "https://docs.getbifrost.ai/features/governance/custom-pricing";

interface PricingOverridesEmptyStateProps {
	onCreateClick: () => void;
}

export function PricingOverridesEmptyState({ onCreateClick }: PricingOverridesEmptyStateProps) {
	const { t } = useTranslation();
	return (
		<div
			className="flex min-h-[80vh] w-full flex-col items-center justify-center gap-4 py-16 text-center"
			data-testid="pricing-overrides-empty-state"
		>
			<div className="text-muted-foreground">
				<SlidersHorizontal className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />
			</div>
			<div className="flex flex-col gap-1">
				<h1 className="text-muted-foreground text-xl font-medium">{t("workspace.customPricing.overridesEmptyState.title")}</h1>
				<div className="text-muted-foreground mx-auto mt-2 max-w-[600px] text-sm font-normal">
					{t("workspace.customPricing.overridesEmptyState.description")}
				</div>
				<div className="mx-auto mt-6 flex flex-row flex-wrap items-center justify-center gap-2">
					<Button
						variant="outline"
						aria-label={t("common.readMore")}
						data-testid="pricing-overrides-button-read-more"
						onClick={() => {
							window.open(`${PRICING_OVERRIDES_DOCS_URL}?utm_source=bfd`, "_blank", "noopener,noreferrer");
						}}
					>
						{t("common.readMore")} <ArrowUpRight className="text-muted-foreground h-3 w-3" />
					</Button>
					<Button
						aria-label={t("workspace.customPricing.overridesEmptyState.createOverride")}
						data-testid="pricing-override-create-btn"
						onClick={onCreateClick}
					>
						{t("workspace.customPricing.overridesEmptyState.createOverride")}
					</Button>
				</div>
			</div>
		</div>
	);
}