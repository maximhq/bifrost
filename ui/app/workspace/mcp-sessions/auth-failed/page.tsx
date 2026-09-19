import { AlertCircle } from "lucide-react";
import { useQueryState } from "nuqs";
import { useTranslation } from "react-i18next";

export default function MCPSessionsAuthFailedPage() {
	const { t } = useTranslation("mcp");
	const [error] = useQueryState("error");
	return (
		<div className="mx-auto flex min-h-[60vh] w-full max-w-xl items-center justify-center p-6">
			<div className="bg-card w-full rounded-sm border p-8 text-center shadow-sm">
				<div className="bg-destructive/10 mx-auto mb-5 flex size-12 items-center justify-center rounded-full">
					<AlertCircle className="text-destructive size-6" />
				</div>
				<h1 className="text-xl font-semibold tracking-tight">{t("sessions.auth.failedTitle")}</h1>
				<p className="text-muted-foreground mt-2 text-sm">{error ?? t("sessions.auth.failedFallback")}</p>
				<p className="text-muted-foreground mt-4 text-sm">{t("sessions.auth.failedHint")}</p>
			</div>
		</div>
	);
}
