interface Props {
	icon: React.ReactNode;
	title: string;
	description: string;
}

export default function ContactUsView({ icon, title, description }: Props) {
	return (
		<div className="flex flex-row gap-4">
			<div className="w-[24px]">{icon}</div>
			<div className="flex flex-col gap-1">
				<div className="text-muted-foreground text-xl font-medium">{title}</div>
				<div className="text-muted-foreground text-sm font-normal">{description}</div>
			</div>
		</div>
	);
}
