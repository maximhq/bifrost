import { CodeEditor } from "@/components/ui/codeEditor";
import CollapsibleBox from "./collapsibleBox";

// Pretty-prints a JSON body and falls back to the raw text (SSE streams, truncated previews).
function formatBody(body: string): string {
	try {
		return JSON.stringify(JSON.parse(body), null, 2);
	} catch {
		return body;
	}
}

interface PassthroughBodyBoxProps {
	title: string;
	body: string;
	testId: string;
}

/** A passthrough request or response body exactly as it crossed the wire, read-only. */
export default function PassthroughBodyBox({ title, body, testId }: PassthroughBodyBoxProps) {
	const formatted = formatBody(body);
	return (
		<div data-testid={testId}>
			<CollapsibleBox title={title} onCopy={() => formatted}>
				<CodeEditor
					className="z-0 w-full"
					shouldAdjustInitialHeight={true}
					maxHeight={450}
					wrap={true}
					code={formatted}
					lang="json"
					readonly={true}
					options={{
						collapsibleBlocks: true,
						showVerticalScrollbar: true,
						scrollBeyondLastLine: false,
						lineNumbers: "off",
						alwaysConsumeMouseWheel: false,
					}}
				/>
			</CollapsibleBox>
		</div>
	);
}