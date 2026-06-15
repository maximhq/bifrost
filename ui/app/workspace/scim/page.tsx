import { ScrollArea } from "@/components/ui/scrollArea";
import SCIMView from "@enterprise/components/scim/scimView";

export default function SCIMPage() {
	return (
		<ScrollArea className="no-padding-parent w-full h-[calc(100dvh-1rem)]">
			<div className="mx-auto max-w-7xl px-4">
				<SCIMView />
			</div>
		</ScrollArea>
	);
}