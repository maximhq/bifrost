import { CodeEditor } from "@/components/ui/codeEditor";
import { type CustomCell, type CustomRenderer, drawTextCell, GridCellKind, type ProvideEditorComponent } from "@maximai/glide-data-grid";
import { useState } from "react";

// A cell of a column that holds JSON text: tool calls, conversation history, tags.
export interface JsonCellData {
	readonly kind: "json-cell";
	readonly text: string;
}

export type JsonCell = CustomCell<JsonCellData>;

export function jsonCell(text: string, readonly: boolean): JsonCell {
	return { kind: GridCellKind.Custom, data: { kind: "json-cell", text }, copyData: text, allowOverlay: true, readonly };
}

// reformat rewrites JSON text with the given indent. Text that is not JSON comes back as it is.
function reformat(text: string, indent?: number): string {
	try {
		return JSON.stringify(JSON.parse(text), null, indent);
	} catch {
		return text;
	}
}

// The editor opens with the JSON laid out over lines, and saves whatever text it is left with.
const JsonCellEditor: ProvideEditorComponent<JsonCell> = ({ value, onChange }) => {
	const [text, setText] = useState(() => reformat(value.data.text, 2));
	return (
		<div className="bg-card min-w-[420px]" data-testid="dataset-json-cell-editor">
			<CodeEditor
				lang="json"
				code={text}
				readonly={value.readonly}
				variant="ghost"
				autoResize
				autoFocus
				shouldAdjustInitialHeight
				minHeight={120}
				maxHeight={420}
				options={{ lineNumbers: "off", scrollBeyondLastLine: false, collapsibleBlocks: true, autoSuggest: false }}
				onChange={(next) => {
					setText(next);
					onChange({ ...value, data: { kind: "json-cell", text: next }, copyData: next });
				}}
			/>
		</div>
	);
};

// jsonCellRenderer draws a JSON cell on one line and edits it in a code editor, the
// way Maxim's datasheet does.
export const jsonCellRenderer: CustomRenderer<JsonCell> = {
	kind: GridCellKind.Custom,
	isMatch: (cell): cell is JsonCell => (cell.data as { kind?: string }).kind === "json-cell",
	draw: (args, cell) => {
		drawTextCell(args, reformat(cell.data.text), cell.contentAlign);
		return true;
	},
	provideEditor: () => ({ editor: JsonCellEditor, disablePadding: true }),
	onPaste: (value, data) => ({ ...data, text: value }),
	onDelete: (cell) => ({ ...cell, data: { ...cell.data, text: "" }, copyData: "" }),
};