import { ScrollArea } from "@/components/ui/scrollArea";
import { useRef } from "react";
import { MessagesView } from "../components/messagesView/rootMessageView";
import { NewMessageInputView } from "../components/newMessageInputView";

export function PlaygroundPanel() {
	const scrollAreaRef = useRef<HTMLDivElement>(null);

	return (
		<div className="custom-scrollbar relative flex h-full flex-col overscroll-none">
			<ScrollArea className="flex-1 overflow-y-auto" ref={scrollAreaRef} viewportClassName="no-table">
				<MessagesView />
			</ScrollArea>
			<NewMessageInputView />
		</div>
	);
}
