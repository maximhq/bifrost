import { Palette } from "lucide-react";
import { useTranslation } from "react-i18next";
import ContactUsView from "../views/contactUsView";

// OSS stub. Custom branding is an enterprise capability — the OSS backend
// exposes no endpoint to store a logo, so this build always renders the
// Bifrost default and this view only explains the upgrade path.
export default function BrandingView() {
	const { t } = useTranslation("config");
	return (
		<div className="h-full w-full">
			<ContactUsView
				className="mx-auto min-h-[80vh]"
				icon={<Palette className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
				title={t("enterprise.brandingTitle")}
				description={t("enterprise.brandingDescription")}
				readmeLink="https://docs.getbifrost.ai/enterprise/overview"
				testIdPrefix="branding"
			/>
		</div>
	);
}
