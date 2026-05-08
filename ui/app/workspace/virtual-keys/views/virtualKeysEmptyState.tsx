import { Button } from "@/components/ui/button";
import i18n from "@/lib/i18n";
import { useTranslation } from "react-i18next";
import { KeyRound } from "lucide-react";
import { ArrowUpRight } from "lucide-react";

const VIRTUAL_KEYS_DOCS_URL = "https://docs.getbifrost.ai/features/governance/virtual-keys";

interface VirtualKeysEmptyStateProps {
	onAddClick: () => void;
	canCreate?: boolean;
}

export function VirtualKeysEmptyState({ onAddClick, canCreate = true }: VirtualKeysEmptyStateProps) {
	const { t } = useTranslation();
	return (
		<div
			className="flex min-h-[80vh] w-full flex-col items-center justify-center gap-4 py-16 text-center"
			data-testid="virtual-keys-empty-state"
		>
			<div className="text-muted-foreground">
				<KeyRound className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />
			</div>
			<div className="flex flex-col gap-1">
				<h1 className="text-muted-foreground text-xl font-medium">{t("workspace.virtualKeys.emptyStateTitle")}</h1>
				<div className="text-muted-foreground mx-auto mt-2 max-w-[600px] text-sm font-normal">
					{t("workspace.virtualKeys.emptyStateDescription")}
				</div>
				<div className="mx-auto mt-6 flex flex-row flex-wrap items-center justify-center gap-2">
					<Button
						variant="outline"
						aria-label={t("workspace.virtualKeys.readMore") + " (opens in new tab)"}
						data-testid="virtual-keys-button-read-more"
						onClick={() => {
							window.open(`${VIRTUAL_KEYS_DOCS_URL}?utm_source=bfd`, "_blank", "noopener,noreferrer");
						}}
					>
						{t("workspace.virtualKeys.readMore")} <ArrowUpRight className="text-muted-foreground h-3 w-3" />
					</Button>
					<Button
						aria-label={t("workspace.virtualKeys.emptyStateAddButton")}
						onClick={onAddClick}
						disabled={!canCreate}
						data-testid="create-vk-btn"
					>
						{t("workspace.virtualKeys.emptyStateAddButton")}
					</Button>
				</div>
			</div>
		</div>
	);
}