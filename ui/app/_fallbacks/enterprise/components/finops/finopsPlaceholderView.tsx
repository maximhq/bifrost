import { Receipt } from "lucide-react";
import ContactUsView from "../views/contactUsView";

type FinOpsPlaceholderViewProps = {
	title: string;
	description: string;
	testIdPrefix: string;
};

export default function FinOpsPlaceholderView({ title, description, testIdPrefix }: FinOpsPlaceholderViewProps) {
	return (
		<div className="h-full w-full">
			<ContactUsView
				className="mx-auto min-h-[80vh]"
				icon={<Receipt className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
				title={title}
				description={description}
				readmeLink="https://docs.getbifrost.ai/enterprise"
				testIdPrefix={testIdPrefix}
			/>
		</div>
	);
}