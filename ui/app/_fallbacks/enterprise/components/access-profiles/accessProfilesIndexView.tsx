import { ShieldCheck } from "lucide-react";
import FeaturePlaceholderView from "../views/featurePlaceholderView";

export default function AccessProfilesIndexView() {
	return (
		<FeaturePlaceholderView
			icon={ShieldCheck}
			title="Unlock access profiles for better performance"
			description="This feature is a part of the Bifrost enterprise license. Create access profiles to control access to your resources."
			readmeLink="https://docs.getbifrost.ai/enterprise/access-profiles"
			testIdPrefix="access-profiles"
		/>
	);
}
