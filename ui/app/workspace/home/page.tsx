import { ScrollArea } from "@/components/ui/scrollArea";
import HomeView from "@enterprise/components/home/homeView";

export default function HomePage() {
	return (
		<div
			className="no-padding-parent no-border-parent bg-background flex h-[calc(var(--app-content-viewport)_-_var(--app-bottom-padding))] w-full"
			data-testid="home-root"
		>
			<ScrollArea className="bg-card flex min-w-0 flex-1 flex-col rounded-md border" viewportClassName="no-table">
				<div className="p-4">
					<HomeView />
				</div>
			</ScrollArea>
		</div>
	);
}