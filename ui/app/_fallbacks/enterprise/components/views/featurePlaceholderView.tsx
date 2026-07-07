import { EmptyStateView } from "@/components/emptyStateView";
import { Button } from "@/components/ui/button";
import type { LucideIcon } from "lucide-react";

interface Props {
	icon: LucideIcon;
	title: string;
	description: string;
	readmeLink: string;
	testIdPrefix?: string;
}

export default function FeaturePlaceholderView({ icon, title, description, readmeLink, testIdPrefix }: Props) {
	return (
		<EmptyStateView
			icon={icon}
			title={title}
			description={description}
			readmeLink={readmeLink}
			readMoreAriaLabel="Read more about this feature (opens in new tab)"
			readMoreTestId={testIdPrefix ? `${testIdPrefix}-read-more` : undefined}
			actions={
				<Button
					aria-label="Book a demo (opens Calendly in new tab)"
					data-testid={testIdPrefix ? `${testIdPrefix}-book-demo` : undefined}
					onClick={() => {
						window.open("https://calendly.com/maximai/bifrost-demo?utm_source=bfd_ent", "_blank", "noopener,noreferrer");
					}}
				>
					Book a demo
				</Button>
			}
		/>
	);
}
