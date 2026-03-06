export function EmptyState() {
	return (
		<div className="text-muted-foreground flex h-full items-center justify-center">
			<div className="text-center">
				<p className="text-lg font-medium">No prompt selected</p>
				<p className="text-sm">Select a prompt from the sidebar or create a new one</p>
			</div>
		</div>
	);
}
