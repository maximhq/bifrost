import { BookUser } from "lucide-react";
import ContactUsView from "../views/contactUsView";

export default function SCIMView() {
	// Same bordered panel the other enterprise placeholders use, so the upsell
	// reads as page content rather than as the page background.
	return (
		<div className="rounded-sm border">
			<div className="flex w-full flex-col items-center justify-center py-16">
				<ContactUsView
					className="mx-auto w-full max-w-lg"
					icon={<BookUser className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
					title="Unlock SCIM based access management for user provisioning"
					description="This feature is a part of the Bifrost enterprise license. We would love to know more about your use case and how we can help you."
					readmeLink="https://docs.getbifrost.ai/enterprise/advanced-governance"
				/>
			</div>
		</div>
	);
}