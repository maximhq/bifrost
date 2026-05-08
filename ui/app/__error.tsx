import { Button } from "@/components/ui/button";
import { useTranslation } from "react-i18next";

export function ErrorComponent() {
	const { t } = useTranslation();
	return (
		<main className="h-base flex items-center justify-center p-6">
			<div className="mx-auto w-full max-w-md text-center">
				<p className="text-foreground text-7xl font-bold tracking-tight">500</p>
				<h1 className="text-foreground mt-4 text-2xl font-semibold">{t("error.title")}</h1>
				<p className="text-muted-foreground mt-2 text-sm">{t("error.description")}</p>
				<div className="mt-6 flex items-center justify-center gap-3">
					<Button size={"sm"} data-testid="error-reload-btn" onClick={() => window.location.reload()}>
						{t("common.reload")}
					</Button>
				</div>
			</div>
		</main>
	);
}