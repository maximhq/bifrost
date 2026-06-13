import ContactUsView from "../views/contactUsView";

interface EdgeControlFallbackViewProps {
	icon: React.ReactNode;
	title: string;
	description: string;
	readmeLink: string;
	testIdPrefix?: string;
}

export default function EdgeControlFallbackView({ icon, title, description, readmeLink, testIdPrefix }: EdgeControlFallbackViewProps) {
	return (
		<div className="h-full w-full">
			<ContactUsView
				className="mx-auto min-h-[80vh]"
				icon={icon}
				title={title}
				description={description}
				readmeLink={readmeLink}
				testIdPrefix={testIdPrefix}
			/>
		</div>
	);
}
