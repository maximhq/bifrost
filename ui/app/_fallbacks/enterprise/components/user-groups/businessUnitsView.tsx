import { Users } from "lucide-react";
import ContactUsView from "../views/contactUsView";

export function BusinessUnitsView() {
	return (
		<div className="w-full">
			<ContactUsView
				className="mx-auto min-h-[80vh]"
				testIdPrefix="business-units-governance"
				icon={<Users className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
				title="Unlock advanced governance"
				description="Manage users, business units with our enterprise-grade governance. This feature is part of the Bifrost enterprise license."
				readmeLink="https://docs.getbifrost.ai/enterprise/advanced-governance"
			/>
		</div>
	);
}
