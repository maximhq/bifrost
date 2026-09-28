import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Database } from "lucide-react";
import { useTranslation } from "react-i18next";

const NotAvailableBanner = () => {
	const { t } = useTranslation("common");
	return (
		<div className="h-base flex items-center justify-center p-4">
			<div className="w-full max-w-md">
				<Alert className="border-destructive/50 text-destructive/50 dark:text-destructive/70 dark:border-destructive/70 [&>svg]:text-destructive dark:bg-card bg-red-50">
					<AlertTitle className="flex items-center gap-2">
						<Database className="dark:text-destructive/70 text-destructive/50 h-4 w-4" />
						{t("notAvailableBanner.title")}
					</AlertTitle>
					<AlertDescription className="mt-2 space-y-2 text-xs">
						<div>{t("notAvailableBanner.description")}</div>
						<div className="text-muted-foreground">
							{t("notAvailableBanner.enableHintPrefix")}{" "}
							<a
								href="https://www.getmaxim.ai/bifrost/docs/quickstart/gateway/setting-up#two-configuration-modes"
								target="_blank"
								rel="noopener noreferrer"
								className="font-medium underline underline-offset-2"
								data-testid="config-store-documentation-link"
							>
								{t("notAvailableBanner.documentation")}
							</a>
							{t("notAvailableBanner.enableHintSuffix")}
						</div>
					</AlertDescription>
				</Alert>
			</div>
		</div>
	);
};

export default NotAvailableBanner;