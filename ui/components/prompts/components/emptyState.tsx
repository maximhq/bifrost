import { Button } from "@/components/ui/button";
import { usePromptContext } from "../context";

export function EmptyState() {
	const { setPromptSheet } = usePromptContext();

	return (
		<div className="text-muted-foreground flex h-full items-center justify-center">
			<div className="text-center">
				<p className="text-lg font-medium">No prompt selected</p>
				<p className="text-sm">
					Select a prompt from the sidebar or{" "}
					<Button variant="link" className="h-auto p-0 text-sm" onClick={() => setPromptSheet({ open: true })}>
						create a new one
					</Button>
				</p>
			</div>
		</div>
	);
}
