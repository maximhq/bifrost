import { Button } from "@/components/ui/button";
import { ArrowUpRight, Waypoints } from "lucide-react";
import { useTranslation } from "react-i18next";

const RELAYS_DOCS_URL = "https://docs.getbifrost.ai/relays/overview";

export function EndpointsEmptyState({ onCreate, canCreate }: { onCreate: () => void; canCreate: boolean }) {
	const { t } = useTranslation("config");

	return (
		<div className="flex min-h-[80vh] w-full flex-col items-center justify-center gap-4 py-16 text-center" data-testid="relays-empty">
			<div className="text-muted-foreground">
				<Waypoints className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />
			</div>
			<div className="flex flex-col gap-1">
				<h1 className="text-muted-foreground text-xl font-medium">{t("relays.emptyState.title")}</h1>
				<div className="text-muted-foreground mx-auto mt-2 w-full max-w-[600px] text-sm font-normal">
					{t("relays.emptyState.description")}
				</div>
				<div className="mx-auto mt-6 flex flex-row flex-wrap items-center justify-center gap-2">
					<Button
						variant="outline"
						aria-label={t("relays.emptyState.readMoreAria")}
						data-testid="relays-button-read-more"
						onClick={() => {
							window.open(`${RELAYS_DOCS_URL}?utm_source=bfd`, "_blank", "noopener,noreferrer");
						}}
					>
						{t("relays.emptyState.readMore")} <ArrowUpRight className="text-muted-foreground h-3 w-3" />
					</Button>
					<Button onClick={onCreate} disabled={!canCreate} data-testid="relays-create-first">
						{t("relays.page.addRelay")}
					</Button>
				</div>
			</div>
		</div>
	);
}