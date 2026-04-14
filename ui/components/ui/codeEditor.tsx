import { cn } from "@/lib/utils";
import { Loader2 } from "lucide-react";
import { editor } from "monaco-editor";
import { useTheme } from "next-themes";
import { Suspense, lazy, useEffect, useRef, useState } from "react";

// Lazy-loaded Monaco Editor (SSR isn't a concern in SPA mode).
const MonacoEditorLazy = lazy(() => import("@monaco-editor/react").then((mod) => ({ default: mod.default })));

const MonacoEditor = (props: React.ComponentProps<typeof MonacoEditorLazy>) => (
	<Suspense fallback={<Loader2 className="h-4 w-4 animate-spin p-4" />}>
		<MonacoEditorLazy {...props} />
	</Suspense>
);

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
	const { className, lang, code, onChange } = props;
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
	const handleEditorDidMount = (editor: editor.IStandaloneCodeEditor) => {
		if (props.autoFocus) {
			editor.focus();
		}

		// Auto-resize logic
		if (props.shouldAdjustInitialHeight || props.autoResize) {
			const clampHeight = (h: number) => {
				if (props.minHeight && h < props.minHeight) h = props.minHeight;
				if (props.maxHeight && h > props.maxHeight) h = props.maxHeight;
				return h;
			};

			editor.onDidContentSizeChange((e: editor.IContentSizeChangedEvent) => {
				if (!e.contentHeightChanged) return;
				const height = clampHeight(e.contentHeight);
				setEditorHeight(height);
				editor.layout();
			});

			// Initial height adjustment
			const height = clampHeight(editor.getContentHeight());
			setEditorHeight(height);
			editor.layout();
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
					// This is a hack to disable web workers when using the editor in a static export, do not change this.
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