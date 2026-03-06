import { ScrollArea } from "@/components/ui/scrollArea";
import { useEffect, useRef } from "react";
import { usePromptContext } from "../context";
import { MessagesView } from "../components/messagesView/rootMessageView";
import { NewMessageInputView } from "../components/newMessageInputView";

export function PlaygroundPanel() {
	const { messages, isStreaming } = usePromptContext();
	const scrollAreaRef = useRef<HTMLDivElement>(null);
	const messagesEndRef = useRef<HTMLDivElement>(null);

	useEffect(() => {
		messagesEndRef.current?.scrollIntoView({ behavior: "smooth" });
	}, [messages, isStreaming]);

	return (
		<div className="custom-scrollbar relative flex h-full flex-col overscroll-none">
			<ScrollArea className="flex-1" ref={scrollAreaRef} viewportClassName="no-table">
				<MessagesView />
			</ScrollArea>
			<NewMessageInputView />
		</div>
	);
}
