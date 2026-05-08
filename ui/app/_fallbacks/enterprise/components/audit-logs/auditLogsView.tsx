import { ScrollText } from "lucide-react";
import { useTranslation } from "react-i18next";
import ContactUsView from "../views/contactUsView";

export default function AuditLogsView() {
	const { t } = useTranslation();

	return (
		<div className="h-full w-full">
			<ContactUsView
				className="mx-auto min-h-[80vh]"
				icon={<ScrollText className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
				title={t("workspace.auditLogs.unlockTitle")}
				description={t("workspace.auditLogs.unlockDescription")}
				readmeLink="https://docs.getbifrost.ai/enterprise/audit-logs"
			/>
		</div>
	);
}