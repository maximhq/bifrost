"use client";

import Editor, { Monaco, loader } from "@monaco-editor/react";
import * as monaco from "monaco-editor";
import { editor } from "monaco-editor";
import { useEffect, useRef, useState } from "react";
import "./code-editor.css";
import { cn } from "@/lib/utils";
import { Loader2 } from "lucide-react";

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
	theme?: string;
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
	} & editor.IStandaloneEditorConstructionOptions;
	containerClassName?: string;
}

export interface CustomLanguage {
	id: string;
	register: (monaco: Monaco) => void;
	validate: (monaco: Monaco, model: editor.ITextModel) => editor.IMarkerData[];
}

export function CodeEditor(props: CodeEditorProps) {
	const { className, lang, code, onChange, height, minHeight } = props;
	const editorContainer = useRef<HTMLDivElement>(null);
	const editorRef = useRef<editor.IStandaloneCodeEditor>(null);
	const [disposables, setDisposables] = useState<monaco.IDisposable[]>([]);

	const [isMounted, setIsMounted] = useState({
		status: false,
		height: props.height,
	});
	loader.config({
		monaco: require("monaco-editor"),
	});
	/**
	 * This block is added to handle a specific case where the editor is readonly, auto-resize on load is enabled  and the code is changed
	 */
	useEffect(() => {
		if (!props.readonly || !props.shouldAdjustInitialHeight) {
			return;
		}
		if (editorRef.current) {
			let contentHeight = editorRef.current.getContentHeight();
			if (props.minHeight && contentHeight && contentHeight < props.minHeight) {
				contentHeight = props.minHeight;
			}
			if (props.maxHeight && contentHeight && contentHeight > props.maxHeight) {
				contentHeight = props.maxHeight;
			}
			setIsMounted({ status: true, height: contentHeight ? contentHeight + 15 : props.height });
			if (contentHeight && editorRef.current.getDomNode())
				editorRef.current.layout({
					height: contentHeight + 15,
					width: editorRef.current.getDomNode()!.clientWidth,
				});
		}
	}, [props.readonly, props.code, props.height, props.minHeight, props.maxHeight, props.shouldAdjustInitialHeight]);

	useEffect(() => {
		return () => {
			disposables.forEach((disposable) => {
				if (disposable?.dispose && typeof disposable.dispose === "function") {
					disposable.dispose();
				}
			});
		};
	}, [disposables]);

	const handleEditorDidMount = (editor: editor.IStandaloneCodeEditor, monaco: Monaco) => {
		if (editorRef.current) {
			editorRef.current.dispose();
		}
		editorRef.current = editor;

		if (props.onFocus) {
			editor.onDidFocusEditorWidget(() => {
				props.onFocus!();
			});
		}
		if (props.autoFocus) {
			editor.focus();
		}

		// Register custom completions including objects and methods
		if (props.customCompletions !== undefined) {
			// Adding new
			const newDisposables: monaco.IDisposable[] = [];
			// First, register signature help providers for custom methods
			const objectItems = props.customCompletions.filter((item) => item.type === "object" && item.methods);

			// Register signature help for all methods with signatures
			objectItems.forEach((objectItem) => {
				// For each object with methods that have signatures, register a signature help provider
				newDisposables.push(
					monaco.languages.registerSignatureHelpProvider(lang || "javascript", {
						signatureHelpTriggerCharacters: ["(", ","],
						signatureHelpRetriggerCharacters: [","],
						provideSignatureHelp: (model, position, token, context): monaco.languages.SignatureHelpResult | null => {
							// Get the content before the cursor to determine if we're in a method call
							const textUntilPosition = model.getValueInRange({
								startLineNumber: position.lineNumber,
								startColumn: 1,
								endLineNumber: position.lineNumber,
								endColumn: position.column,
							});

							// Check for method calls like "objectName.methodName("
							const methodCallMatch = textUntilPosition.match(new RegExp(`${objectItem.label}\\.(\\w+)\\(`));
							if (!methodCallMatch) return null;

							const methodName = methodCallMatch[1];
							const method = objectItem.methods?.find((m) => m.label === methodName);

							if (!method || !method.signature) return null;

							// Count commas to determine which parameter we're on
							const commaCount = textUntilPosition
								.substring(textUntilPosition.lastIndexOf("("))
								.split("")
								.filter((char) => char === ",").length;

							// Create signature information
							return {
								dispose: () => {},
								value: {
									signatures: [
										{
											label: `${methodName}(${method.signature.parameters.join(", ")})${method.signature.returnType ? `: ${method.signature.returnType}` : ""}`,
											documentation: method.description || "",
											parameters: method.signature.parameters.map((param) => ({
												label: param,
												documentation: "", // Could add per-parameter documentation if available
											})),
										},
									],
									activeSignature: 0,
									activeParameter: Math.min(commaCount, method.signature.parameters.length - 1),
								},
							};
						},
					}),
				);
			});

			// Register completion provider for non-object items
			newDisposables.push(
				monaco.languages.registerCompletionItemProvider(lang || "javascript", {
					provideCompletionItems: (model, position) => {
						const wordUntilPosition = model.getWordUntilPosition(position);
						return {
							suggestions: props
								.customCompletions!.filter((item) => item.type !== "object")
								.map((item) => {
									// Format the signature for display in detail
									let detail = item.label;

									// Add signature details to methods if available
									if (item.signature) {
										const params = item.signature.parameters.join(", ");
										const returnType = item.signature.returnType ? `: ${item.signature.returnType}` : "";
										detail = `${item.label}(${params})${returnType}`;
									}

									return {
										label: item.label,
										kind:
											item.type === "variable" ? monaco.languages.CompletionItemKind.Variable : monaco.languages.CompletionItemKind.Method,
										insertText: item.insertText,
										documentation: { value: item.documentation ?? "", isTrusted: true },
										detail: item.description || "",
										// For methods with signatures, add these to show parameter hints
										...(item.type === "method" && item.signature
											? {
													insertTextRules: monaco.languages.CompletionItemInsertTextRule.KeepWhitespace,
													command: { id: "editor.action.triggerParameterHints", title: "Trigger Parameter Hints" },
												}
											: {}),
										range: {
											startLineNumber: position.lineNumber,
											startColumn: wordUntilPosition.startColumn,
											endLineNumber: position.lineNumber,
											endColumn: position.column,
										},
									} satisfies monaco.languages.CompletionItem;
								}),
						};
					},
				}),
			);

			// Register objects and their methods for dot completion
			objectItems.forEach((objectItem) => {
				// Register object's methods completion when typing after object name and dot
				monaco.languages.registerCompletionItemProvider(lang || "javascript", {
					triggerCharacters: ["."],
					provideCompletionItems: (model, position) => {
						const textUntilPosition = model.getValueInRange({
							startLineNumber: position.lineNumber,
							startColumn: 1,
							endLineNumber: position.lineNumber,
							endColumn: position.column,
						});

						// Check if we're typing after the object name and a dot
						const objectAccessPattern = new RegExp(`${objectItem.label}\\.\\w*$`);
						if (objectAccessPattern.test(textUntilPosition)) {
							const wordUntilPosition = model.getWordUntilPosition(position);

							return {
								suggestions: objectItem.methods!.map((method) => {
									// Format method signature for display
									let detail = method.label;

									// Add signature details if available
									if (method.signature) {
										const params = method.signature.parameters.join(", ");
										const returnType = method.signature.returnType ? `: ${method.signature.returnType}` : "";
										detail = `${method.label}(${params})${returnType}`;
									}

									// Create completion item for method
									return {
										label: {
											label: method.label,
											description: method.signature
												? `(${method.signature.parameters.join(", ")})${method.signature.returnType ? `: ${method.signature.returnType}` : ""}`
												: undefined,
										},
										kind: monaco.languages.CompletionItemKind.Method,
										insertText: method.insertText,
										documentation: { value: method.description || "" },
										detail: detail,
										// Important: These properties help trigger parameter hints
										...(method.signature
											? {
													insertTextRules: monaco.languages.CompletionItemInsertTextRule.KeepWhitespace,
													command: { id: "editor.action.triggerParameterHints", title: "Trigger Parameter Hints" },
												}
											: {}),
										range: {
											startLineNumber: position.lineNumber,
											startColumn: wordUntilPosition.startColumn,
											endLineNumber: position.lineNumber,
											endColumn: position.column,
										},
									};
								}),
							};
						}

						return { suggestions: [] };
					},
				});
				newDisposables.push(
					// Register the object itself for completion
					monaco.languages.registerCompletionItemProvider(lang || "javascript", {
						provideCompletionItems: (model, position) => {
							const wordUntilPosition = model.getWordUntilPosition(position);

							// Only suggest the object name if we're typing something that matches the start
							if (objectItem.label.startsWith(wordUntilPosition.word)) {
								return {
									suggestions: [
										{
											label: objectItem.label,
											kind: monaco.languages.CompletionItemKind.Class,
											insertText: objectItem.label,
											documentation: { value: objectItem.description || `Object containing various methods` },
											detail: objectItem.description || `Object with methods`,
											range: {
												startLineNumber: position.lineNumber,
												startColumn: wordUntilPosition.startColumn,
												endLineNumber: position.lineNumber,
												endColumn: position.column,
											},
										},
									],
								};
							}

							return { suggestions: [] };
						},
					}),
				);
			});
			setDisposables(newDisposables);
		}

		// if the editor is mounted and the initial height is not adjusted, adjust it
		if (!isMounted.status && props.shouldAdjustInitialHeight === true) {
			setTimeout(() => {
				let contentHeight = editorRef.current?.getContentHeight();
				if (props.minHeight && contentHeight && contentHeight < props.minHeight) {
					contentHeight = props.minHeight;
				}
				if (props.maxHeight && contentHeight && contentHeight > props.maxHeight) {
					contentHeight = props.maxHeight;
				}
				setIsMounted({ status: true, height: contentHeight ? contentHeight + 15 : props.height });
				if (contentHeight && editor.getDomNode()) {
					editor.layout({
						height: contentHeight + 15,
						width: editor.getDomNode()!.clientWidth,
					});
				}
			}, 20);
		}
		if (props.autoResize === true) {
			const model = editor.getModel();
			if (model) {
				model.onDidChangeContent(() => {
					let contentHeight = editorRef.current?.getContentHeight();
					if (props.minHeight && contentHeight && contentHeight < props.minHeight) {
						contentHeight = props.minHeight;
					}
					// Checking for maxHeight
					if (props.maxHeight && contentHeight && contentHeight > props.maxHeight) {
						contentHeight = props.maxHeight;
					}
					if (!contentHeight || !editor.getDomNode()) return;
					editor.layout({ height: contentHeight, width: editor.getDomNode()!.clientWidth });
					setIsMounted({ status: true, height: contentHeight });
					if (editorContainer.current) {
						editorContainer.current.style.overflowY = "auto";
						editorContainer.current.style.maxHeight = `${props.maxHeight || contentHeight}px`;
					}
				});
			}
		}
		if (props.customLanguage) {
			props.customLanguage.register(monaco);
			editor.onDidChangeModelContent(() => {
				const model = editor.getModel();
				if (!model) return;
				const markers = props.customLanguage!.validate(monaco, model);
				monaco.editor.setModelMarkers(model, props.customLanguage!.id, markers);
			});
		}

		if (props.autoFormat) {
			editor.getAction("editor.action.formatDocument")?.run();
		}

		if (props.options?.autoSizeOnContentChange) {
			editor.onDidContentSizeChange((e) => {
				const contentHeight = e.contentHeight;
				if (contentHeight && editor.getDomNode()) {
					editor.layout({
						height: contentHeight + 15,
						width: editor.getDomNode()!.clientWidth,
					});
				}
				setIsMounted({ status: true, height: contentHeight + 15 });
			});
		}
	};

	let scrollBeyondLastLine = props.options?.scrollBeyondLastLine;
	if (scrollBeyondLastLine === undefined) {
		scrollBeyondLastLine = props.autoResize === true ? false : undefined;
	}

	return (
		<div
			id={props.id}
			ref={editorContainer}
			className={cn("group relative h-full w-full", props.containerClassName)}
			onBlur={() => {
				if (props.onBlur) props.onBlur();
			}}
		>
			<Editor
				key={props.id}
				onMount={handleEditorDidMount}
				className={cn(`code text-md w-full overscroll-auto bg-transparent ring-offset-transparent outline-hidden`, {
					[`${className}`]: className !== undefined,
				})}
				width={props.width}
				height={isMounted.height || height || minHeight || "auto"}
				theme={props.theme}
				loading={<Loader2 className="h-4 w-4 animate-spin p-4" />}
				options={
					{
						stickyScroll: {
							enabled: false,
						},
						lineNumbersMinChars: props.options?.lineNumbersMinChars ?? 4,
						overviewRulerLanes: props.options?.overviewRulerLanes,
						readOnly: props.readonly,
						scrollBeyondLastLine: scrollBeyondLastLine,
						autoClosingBrackets: "always",
						suggestOnTriggerCharacters: props.options?.autoSuggest !== undefined ? props.options?.autoSuggest : true,
						codeLens: true,
						fontFamily: "var(--font-geist-mono)",
						padding: { top: 2, bottom: 2 },
						fontSize: props.fontSize || 12.5,
						lineNumbers: props.options?.lineNumbers || "off",
						glyphMargin: false,
						scrollbar: {
							vertical: props.options?.showVerticalScrollbar === true ? "auto" : "hidden",
							horizontal: props.options?.showHorizontalScrollbar === true ? "auto" : "hidden",
							alwaysConsumeMouseWheel: props.options?.alwaysConsumeMouseWheel || scrollBeyondLastLine === false ? false : true,
						},
						contextmenu: false,
						minimap: { enabled: false },
						renderLineHighlight: "none",
						wordWrap: props.wrap ? "on" : "off",
						folding: props.options?.collapsibleBlocks ? true : false,
						cursorStyle: "line",
						cursorBlinking: "smooth",
						fixedOverflowWidgets: true,
						guides: {
							indentation: props.options?.showIndentLines ?? true,
						},
						hover: {
							enabled: !props.options?.disableHover,
						},
						...props.options,
					} satisfies editor.IStandaloneEditorConstructionOptions
				}
				defaultLanguage={lang || "javascript"}
				language={lang}
				defaultValue={""}
				value={code}
				onChange={(value) => {
					if (onChange) {
						onChange(value || "");
					}
				}}
			/>
		</div>
	);
}
