import { Users } from "lucide-react";
import { useTranslation } from "react-i18next";
import ContactUsView from "../views/contactUsView";

export default function UserRankingsTab() {
	const { t } = useTranslation("governance");

	return (
		<div className="h-full w-full">
			<ContactUsView
				className="mx-auto min-h-[80vh]"
				icon={<Users className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
				title={t("userRankings.unlockTitle")}
				readmeLink="https://docs.getbifrost.ai/enterprise/user-rankings"
				testIdPrefix="user-rankings"
			/>
		</div>
	);
}
