"use client";

import { cn } from "@/lib/utils";
import { Loader2 } from "lucide-react";
import { editor } from "monaco-editor";
import { useTheme } from "next-themes";
import dynamic from "next/dynamic";
import { useEffect, useRef, useState } from "react";

// Dynamically import Monaco Editor with SSR disabled
const MonacoEditor = dynamic(() => import("@monaco-editor/react").then((mod) => mod.default), {
	ssr: false,
	loading: () => <Loader2 className="h-4 w-4 animate-spin p-4" />,
});

export type CompletionItem = {
	label: string;
	insertText: string;
	documentation?: string;
	description?: string;
	type: "variable" | "method" | "object";
};

export interface CodeEditorProps {
	id?: string;
	className?: string;
	lang?: string;
	code?: string;
	readonly?: boolean;
	maxHeight?: number;
	height?: string | number;
	minHeight?: number;
	width?: string | number;
	onChange?: (value: string) => void;
	wrap?: boolean;
	onBlur?: () => void;
	onSave?: () => void;
	onFocus?: () => void;
	customCompletions?: (CompletionItem & {
		methods?: (CompletionItem & {
			signature?: {
				parameters: string[];
				returnType?: string;
			};
		})[];
		description?: string;
		signature?: {
			parameters: string[];
			returnType?: string;
		};
	})[];
	variant?: "ghost" | "default";
	customLanguage?: CustomLanguage;
	shouldAdjustInitialHeight?: boolean;
	autoResize?: boolean;
	autoFocus?: boolean;
	autoFormat?: boolean;
	fontSize?: number;
	options?: {
		autoSizeOnContentChange?: boolean;
		lineNumbers?: "on" | "off";
		collapsibleBlocks?: boolean;
		alwaysConsumeMouseWheel?: boolean;
		autoSuggest?: boolean;
		overviewRulerLanes?: number;
		scrollBeyondLastLine?: boolean;
		showIndentLines?: boolean;
		quickSuggestions?: boolean;
		disableHover?: boolean;
		lineNumbersMinChars?: number;
		showVerticalScrollbar?: boolean;
		showHorizontalScrollbar?: boolean;
	};
	containerClassName?: string;
}

export interface CustomLanguage {
	id: string;
	register: (monaco: any) => void;
	validate: (monaco: any, model: any) => any[];
}

export function CodeEditor(props: CodeEditorProps) {
	const { className, lang, code, onChange, height, minHeight } = props;
	const editorContainer = useRef<HTMLDivElement>(null);
	const [isClient, setIsClient] = useState(false);
	const [editorHeight, setEditorHeight] = useState<number | string>(props.height || props.minHeight || 200);

	// Ensure we only render on client
	useEffect(() => {
		setIsClient(true);
	}, []);

	const { theme, systemTheme } = useTheme();

	// Calculate theme
	const getTheme = () => {
		if (theme === "dark") return "custom-dark";
		if (theme === "system" && systemTheme === "dark") return "custom-dark";
		return "light";
	};

	// Handle editor mount
	const handleEditorDidMount = (editor: any, monaco: any) => {
		if (props.autoFocus) {
			editor.focus();
		}

		// Auto-resize logic
		if (props.shouldAdjustInitialHeight || props.autoResize) {
			const updateHeight = () => {
				try {
					let contentHeight = editor.getContentHeight();
					if (props.minHeight && contentHeight < props.minHeight) {
						contentHeight = props.minHeight;
					}
					if (props.maxHeight && contentHeight > props.maxHeight) {
						contentHeight = props.maxHeight;
					}
					setEditorHeight(contentHeight + 15);
					editor.layout();
				} catch (error) {
					console.warn("Error updating editor height:", error);
				}
			};

			// Initial height adjustment
			setTimeout(updateHeight, 100);

			// Auto-resize on content change
			if (props.autoResize) {
				const model = editor.getModel();
				if (model) {
					model.onDidChangeContent(() => {
						requestAnimationFrame(updateHeight);
					});
				}
			}
		}

		// Auto-format
		if (props.autoFormat) {
			try {
				editor.getAction("editor.action.formatDocument")?.run();
			} catch (error) {
				console.warn("Auto-format failed:", error);
			}
		}
	};

	const isFoldingEnabled = props.options?.collapsibleBlocks ?? false;

	const editorOptions = {
		lineNumbers: (props.options?.lineNumbers || "off") as "on" | "off",
		readOnly: props.readonly,
		scrollBeyondLastLine: props.options?.scrollBeyondLastLine ?? false,
		minimap: { enabled: false },
		contextmenu: false,
		fontFamily: "var(--font-geist-mono)",
		fontSize: props.fontSize || 12.5,
		padding: { top: 2, bottom: 2 },
		wordWrap: props.wrap ? ("on" as const) : ("off" as const),
		folding: isFoldingEnabled,
		glyphMargin: false,
		lineNumbersMinChars: props.options?.lineNumbersMinChars ?? 4,
		lineDecorationsWidth: 8,
		showFoldingControls: isFoldingEnabled ? ("always" as const) : ("mouseover" as const),
		overviewRulerLanes: props.options?.overviewRulerLanes ?? 0,
		renderLineHighlight: "none" as const,
		cursorStyle: "line" as const,
		cursorBlinking: "smooth" as const,
		scrollbar: {
			vertical: (props.options?.showVerticalScrollbar ? "auto" : "hidden") as "auto" | "hidden",
			horizontal: (props.options?.showHorizontalScrollbar ? "auto" : "hidden") as "auto" | "hidden",
			alwaysConsumeMouseWheel: props.options?.alwaysConsumeMouseWheel ?? false,
		},
		guides: {
			indentation: props.options?.showIndentLines ?? true,
		},
		hover: {
			enabled: !props.options?.disableHover,
		},
		wordBasedSuggestions: "off" as const,
		...props.options,
	} as editor.IStandaloneEditorConstructionOptions;

	if (!isClient) {
		return (
			<div className={cn("group relative flex h-24 w-full items-center justify-center", props.containerClassName)}>
				<Loader2 className="h-4 w-4 animate-spin" />
			</div>
		);
	}

	return (
		<div id={props.id} ref={editorContainer} className={cn("group relative h-full w-full", props.containerClassName)} onBlur={props.onBlur}>
			<MonacoEditor
				height={editorHeight}
				width={props.width}
				language={lang || "javascript"}
				value={code || ""}
				theme={getTheme()}
				options={editorOptions}
				loading={<Loader2 className="h-4 w-4 animate-spin" />}
				onChange={(value) => {
					if (onChange) {
						onChange(value || "");
					}
				}}
				onMount={handleEditorDidMount}
				className={cn("code text-md w-full bg-transparent ring-offset-transparent outline-none", className)}
				beforeMount={(monaco) => {
					// Configure Monaco for static exports
					if (typeof window !== "undefined") {
						// Disable web workers
						(window as any).MonacoEnvironment = {
							getWorker: () => {
								return {
									postMessage: () => {},
									terminate: () => {},
									addEventListener: () => {},
									removeEventListener: () => {},
									dispatchEvent: () => false,
									onerror: null,
									onmessage: null,
									onmessageerror: null,
								};
							},
						};

						// Define custom dark theme with transparent background
						monaco.editor.defineTheme("custom-dark", {
							base: "vs-dark",
							inherit: true,
							rules: [],
							colors: {
								"editor.background": "#00000000",
								focusBorder: "#00000000",
								"editor.lineHighlightBorder": "#00000000",
								"editor.selectionHighlightBorder": "#00000000",
								"editorWidget.border": "#00000000",
								"editorOverviewRuler.border": "#00000000",
							},
						});
					}
				}}
			/>
		</div>
	);
}
