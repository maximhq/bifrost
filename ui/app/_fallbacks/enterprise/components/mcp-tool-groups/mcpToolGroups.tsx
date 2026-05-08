import { ToolCase } from "lucide-react";
import { useTranslation } from "react-i18next";
import ContactUsView from "../views/contactUsView";

export default function MCPToolGroups() {
	const { t } = useTranslation();
	return (
		<>
			<div className="mb-4 flex items-center justify-between gap-4">
				<div>
					<h2 className="text-lg font-semibold tracking-tight">{t("workspace.mcpToolGroups.title")}</h2>
					<p className="text-muted-foreground text-sm">{t("workspace.mcpToolGroups.description")}</p>
				</div>
			</div>
			<div className="rounded-sm border">
				<div className="flex w-full flex-col items-center justify-center py-16">
					<ContactUsView
						className="mx-auto w-full max-w-lg"
						icon={<ToolCase className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
						title={t("workspace.mcpToolGroups.unlockTitle")}
						description={t("workspace.mcpToolGroups.unlockDescription")}
						readmeLink="https://docs.getbifrost.ai/mcp/overview"
					/>
				</div>
			</div>
		</>
	);
}