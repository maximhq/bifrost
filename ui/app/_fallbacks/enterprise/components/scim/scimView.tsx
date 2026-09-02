import { BookUser } from "lucide-react";
import { useTranslation } from "react-i18next";
import ContactUsView from "../views/contactUsView";

export default function SCIMView() {
	const { t } = useTranslation("config");
	return (
		<div className="rounded-sm border">
			<div className="flex w-full flex-col items-center justify-center py-16">
				<ContactUsView
					className="mx-auto w-full max-w-lg"
					icon={<BookUser className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
					title={t("enterprise.scimTitle")}
					description={t("enterprise.sharedDescription")}
					readmeLink="https://docs.getbifrost.ai/enterprise/advanced-governance"
				/>
			</div>
		</div>
	);
}
