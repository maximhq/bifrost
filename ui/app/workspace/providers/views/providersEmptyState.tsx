import { Button } from "@/components/ui/button";
import { ArrowUpRight, Server } from "lucide-react";
import { useTranslation } from "react-i18next";

const PROVIDERS_DOCS_URL = "https://docs.getbifrost.ai/providers/supported-providers/overview";

interface ProvidersEmptyStateProps {
	/** Dropdown (or button) for adding a provider; never greyed out */
	addProviderDropdown: React.ReactNode;
}

export function ProvidersEmptyState({ addProviderDropdown }: ProvidersEmptyStateProps) {
	const { t } = useTranslation("models");
	return (
		<div className="flex min-h-[80vh] w-full flex-col items-center justify-center gap-4 py-16 text-center">
			<div className="text-muted-foreground">
				<Server className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />
			</div>
			<div className="flex flex-col gap-1">
				<h1 className="text-muted-foreground text-xl font-medium">{t("providers.emptyTitle")}</h1>
				<div className="text-muted-foreground mx-auto mt-2 w-full max-w-[600px] text-sm font-normal">
					{t("providers.emptyDescription")}
				</div>
				<div className="mx-auto mt-6 flex flex-row flex-wrap items-center justify-center gap-2">
					<Button
						variant="outline"
						aria-label={t("providers.readMoreAria")}
						data-testid="providers-button-read-more"
						onClick={() => {
							window.open(`${PROVIDERS_DOCS_URL}?utm_source=bfd`, "_blank", "noopener,noreferrer");
						}}
					>
						{t("providers.readMore")} <ArrowUpRight className="text-muted-foreground h-3 w-3" />
					</Button>
					{addProviderDropdown}
				</div>
			</div>
		</div>
	);
}