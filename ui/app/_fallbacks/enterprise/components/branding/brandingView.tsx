import { Palette } from "lucide-react";
import ContactUsView from "../views/contactUsView";

// OSS stub. Custom branding is an enterprise capability — the OSS backend
// exposes no endpoint to store a logo, so this build always renders the
// Bifrost default and this view only explains the upgrade path.
export default function BrandingView() {
	return (
		<div className="h-full w-full">
			<ContactUsView
				className="mx-auto min-h-[80vh]"
				icon={<Palette className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
				title="Unlock custom branding"
				description="Replace the Bifrost logo with your own across the dashboard and login screen. This feature is a part of the Bifrost enterprise license. We would love to know more about your use case and how we can help you."
				readmeLink="https://docs.getbifrost.ai/enterprise/overview"
				testIdPrefix="branding"
			/>
		</div>
	);
}
