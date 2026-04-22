import { ScrollArea } from "@/components/ui/scrollArea";
import { MessagesView } from "../components/messagesView/rootMessageView";
import { NewMessageInputView } from "../components/newMessageInputView";

export function PlaygroundPanel() {
	return (
		<div className="custom-scrollbar relative flex h-full flex-col overscroll-none">
			<ScrollArea className="flex-1 scroll-mb-12 overflow-y-auto" viewportClassName="no-table">
				<MessagesView />
			</ScrollArea>
			<NewMessageInputView />
		</div>
	);
}