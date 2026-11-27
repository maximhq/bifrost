import { CheckCircle2 } from "lucide-react";
import { useQueryState } from "nuqs";
import { useTranslation } from "react-i18next";
import { ToolsPendingNotice } from "../views/toolsPendingNotice";

export default function MCPSessionsAuthSuccessPage() {
	const { t } = useTranslation("mcp");
	// Set by the OAuth callback when sign-in stored the token but could not load
	// the client's tools with it.
	const [tools] = useQueryState("tools");
	const [client] = useQueryState("client");
	return (
		<div className="mx-auto flex min-h-[60vh] w-full max-w-xl items-center justify-center p-6">
			<div className="bg-card w-full rounded-sm border p-8 text-center shadow-sm">
				<div className="bg-primary/10 mx-auto mb-5 flex size-12 items-center justify-center rounded-full">
					<CheckCircle2 className="text-primary size-6" />
				</div>
				<h1 className="text-xl font-semibold tracking-tight">{t("sessions.auth.successTitle")}</h1>
				<p className="text-muted-foreground mt-2 text-sm">{t("sessions.auth.successBody")}</p>
				{tools === "pending" && client && <ToolsPendingNotice clientName={client} className="mt-6" />}
			</div>
		</div>
	);
}