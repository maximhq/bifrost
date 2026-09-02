import { ShieldUser } from "lucide-react";
import { useTranslation } from "react-i18next";
import ContactUsView from "../views/contactUsView";

export default function MCPAuthConfigView() {
	const { t } = useTranslation("mcp");

	return (
		<div className="h-full w-full">
			<ContactUsView
				className="mx-auto min-h-[80vh]"
				icon={<ShieldUser className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
				title={t("authConfig.unlockTitle")}
				description={t("authConfig.unlockDescription")}
				readmeLink="https://docs.getbifrost.ai/mcp/overview"
			/>
		</div>
	);
}
