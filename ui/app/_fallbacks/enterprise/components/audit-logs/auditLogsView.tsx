import { ScrollText } from "lucide-react";
import FeaturePlaceholderView from "../views/featurePlaceholderView";

export default function AuditLogsView() {
	return (
		<FeaturePlaceholderView
			icon={ScrollText}
			title="Unlock audit logs for better compliance"
			description="This feature is a part of the Bifrost enterprise license. We would love to know more about your use case and how we can help you."
			readmeLink="https://docs.getbifrost.ai/enterprise/audit-logs"
		/>
	);
}
