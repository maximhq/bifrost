import { Button } from "@/components/ui/button";
import { useT } from "@/lib/i18n";
import { WalletCards } from "lucide-react";
import { ArrowUpRight } from "lucide-react";

const CUSTOMERS_DOCS_URL = "https://docs.getbifrost.ai/features/governance/virtual-keys#customers";

interface CustomersEmptyStateProps {
	onAddClick: () => void;
	canCreate?: boolean;
}

export function CustomersEmptyState({ onAddClick, canCreate = true }: CustomersEmptyStateProps) {
	const t = useT();
	return (
		<div className="flex min-h-[80vh] w-full flex-col items-center justify-center gap-4 py-16 text-center">
			<div className="text-muted-foreground">
				<WalletCards className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />
			</div>
			<div className="flex flex-col gap-1">
				<h1 className="text-muted-foreground text-xl font-medium">{t("governance.customers.emptyHeadline")}</h1>
				<div className="text-muted-foreground mx-auto mt-2 w-full max-w-[600px] text-sm font-normal">
					{t("governance.customers.emptyBody")}
				</div>
				<div className="mx-auto mt-6 flex flex-row flex-wrap items-center justify-center gap-2">
					<Button
						variant="outline"
						aria-label={t("governance.customers.readMoreAria")}
						data-testid="customer-button-read-more"
						onClick={() => {
							window.open(`${CUSTOMERS_DOCS_URL}?utm_source=bfd`, "_blank", "noopener,noreferrer");
						}}
					>
						{t("governance.shared.readMore")} <ArrowUpRight className="text-muted-foreground h-3 w-3" />
					</Button>
					<Button
						aria-label={t("governance.customers.addAria")}
						onClick={onAddClick}
						disabled={!canCreate}
						data-testid="customer-button-create"
					>
						{t("governance.customers.add")}
					</Button>
				</div>
			</div>
		</div>
	);
}