import { CheckCircle2 } from "lucide-react";
import { useTranslation } from "react-i18next";

export default function MCPSessionsAuthSuccessPage() {
	const { t } = useTranslation("mcp");
	return (
		<div className="mx-auto flex min-h-[60vh] w-full max-w-xl items-center justify-center p-6">
			<div className="bg-card w-full rounded-sm border p-8 text-center shadow-sm">
				<div className="bg-primary/10 mx-auto mb-5 flex size-12 items-center justify-center rounded-full">
					<CheckCircle2 className="text-primary size-6" />
				</div>
				<h1 className="text-xl font-semibold tracking-tight">{t("sessions.auth.successTitle")}</h1>
				<p className="text-muted-foreground mt-2 text-sm">{t("sessions.auth.successBody")}</p>
			</div>
		</div>
	);
}
