export default function BlockHeader({ title, icon }: { title: string; icon?: React.ReactNode }) {
	return (
		<div className="flex items-center gap-2">
			{icon && <span className="shrink-0">{icon}</span>}
			<div className="text-sm font-medium">{title}</div>
		</div>
	);
}
