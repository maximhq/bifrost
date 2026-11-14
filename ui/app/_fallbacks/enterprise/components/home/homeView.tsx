import { House } from "lucide-react";
import { useTranslation } from "react-i18next";
import ContactUsView from "../views/contactUsView";

export default function HomeView() {
	const { t } = useTranslation("shell");
	return (
		<div className="h-full w-full">
			<ContactUsView
				className="mx-auto min-h-[80vh]"
				icon={<House className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
				title={t("home.unlockTitle")}
				description={t("home.unlockDescription")}
				readmeLink="https://docs.getbifrost.ai/enterprise"
				testIdPrefix="home"
			/>
		</div>
	);
}