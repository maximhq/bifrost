import { KeyRound } from "lucide-react";
import ContactUsView from "../views/contactUsView";

export default function LicenseSettingsView() {
	return (
		<div className="h-full w-full">
			<ContactUsView
				className="mx-auto min-h-[80vh]"
				icon={<KeyRound className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
				title="Unlock license management"
				description="This feature is a part of the Bifrost enterprise license. We would love to know more about your use case and how we can help you."
				readmeLink="https://docs.getbifrost.ai/enterprise/overview"
				testIdPrefix="license"
			/>
		</div>
	);
}
