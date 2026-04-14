import { VariantProps, cva } from "class-variance-authority";
import React, { useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import TextareaAutosize from "react-textarea-autosize";
import { cn } from "../utils";
import { CustomDropdown, DropdownOption } from "./dropdown";

const textAreaVariants = cva(
	"flex w-full h-full text-transparent caret-black rounded-md resize-none bg-transparent px-3 py-2 text-md placeholder:text-content-disabled disabled:cursor-not-allowed disabled:opacity-50 relative font-[inherit] focus-visible:outline-none whitespace-pre-wrap overflow-y-auto",
	{
		variants: {
			variant: {
				default: "border border-border-default focus-visible:border-border-focus",
				ghost: "",
			},
		},
		defaultVariants: {
			variant: "default",
		},
	},
);

interface TextareaProps extends React.TextareaHTMLAttributes<HTMLTextAreaElement>, VariantProps<typeof textAreaVariants> {
	innerRef?: React.Ref<HTMLTextAreaElement>;
	autoFocus?: boolean;
	focusAtTheEndOfTheValue?: boolean;
	onFilePasted?: (files: File[]) => void;
	suggestions?: DropdownOption[];
	nestedSuggestions?: Record<string, any>;
	suggestionsTrigger?: string;
	highlightPatterns?: Array<{
		pattern: RegExp;
		className: string | ((part: string) => string);
		validate: (part: string) => boolean;
		enableVariableClickEdit?: boolean;
	}>;
	textAreaClassName?: string;
	noSuggestionText?: string;
	suggestionDropdownClassName?: string;
	/**
	 * Disables the functionality of clicking on a variable to change it.
	 */
	enableVariableClickEdit?: boolean;
	/**
	 * If true, selects all text by default.
	 */
	selectAllByDefault?: boolean;
	/**
	 * Inline suggestion text to show when cursor is inside empty {{}} braces.
	 */
	inlineSuggestionText?: string;
}

const RichTextarea = React.forwardRef<HTMLTextAreaElement, TextareaProps>(
	({
		className,
		variant,
		innerRef,
		enableVariableClickEdit,
		suggestions,
		nestedSuggestions,
		value = "",
		onChange,
		highlightPatterns,
		suggestionsTrigger: customCompletionsTrigger,
		textAreaClassName,
		noSuggestionText,
		suggestionDropdownClassName,
		selectAllByDefault,
		inlineSuggestionText,
		...props
	}) => {
		const textareaRef = useRef<HTMLTextAreaElement | null>(null);
		const preRef = useRef<HTMLPreElement>(null);
		const [showDropdown, setShowDropdown] = useState(false);
		const [dropdownPosition, setDropdownPosition] = useState({ top: 0, left: 0 });
		const [dropdownDirection, setDropdownDirection] = useState<"top" | "bottom">("bottom");
		const [cursorPosition, setCursorPosition] = useState<number | null>(null);
		const [clickedVariable, setClickedVariable] = useState<{ start: number; end: number; text: string } | null>(null);
		const [searchText, setSearchText] = useState("");
		const [currentPath, setCurrentPath] = useState<string[]>([]);
		const containerRef = useRef<HTMLDivElement>(null);

		// Combine all highlight patterns with default patterns
		const highlightPattern: RegExp | undefined = useMemo(() => {
			const patterns = highlightPatterns?.map((h) => `(${h.pattern.source})`);
			if (!patterns || patterns.length === 0) return undefined;
			const combinedPattern = patterns.join("|");
			return new RegExp(combinedPattern, "g");
		}, [highlightPatterns]);

		const getNestedSuggestions = (path: string[]): DropdownOption[] | undefined => {
			if (!nestedSuggestions || path.length === 0) return suggestions;

			let current: any = nestedSuggestions;
			// Navigate through the path
			for (let i = 0; i < path.length; i++) {
				if (!current[path[i]]) return undefined;
				current = current[path[i]];
			}

			// Convert the nested object to DropdownOption format
			if (typeof current === "object" && current !== null) {
				// Get all keys except "description" which is handled separately
				const keys = Object.keys(current).filter((key) => key !== "description");

				// Special case: if the only key is __description, don't show it as an option
				if (keys.length === 1 && keys[0] === "__description") {
					return [];
				}

				return keys.map((key) => {
					// Check if this object only has a __description key
					const hasOnlySpecialDescription =
						typeof current[key] === "object" &&
						current[key] !== null &&
						Object.keys(current[key]).length === 1 &&
						Object.keys(current[key])[0] === "__description";

					// Determine if it has children (but ignore the __description key when counting)
					const hasChildren =
						typeof current[key] === "object" &&
						current[key] !== null &&
						Object.keys(current[key]).filter((k) => k !== "description" && k !== "__description").length > 0;

					// Get description - prioritize regular description, fall back to __description
					let description;
					if (current[key]?.description) {
						description = current[key].description;
					} else if (hasOnlySpecialDescription) {
						description = current[key].__description;
					}

					return {
						type: "item",
						label: key,
						value: hasChildren ? `{{${[...path, key].join(".")}.` : `{{${[...path, key].join(".")}}}`,
						hasChildren,
						description,
					};
				});
			}

			return undefined;
		};

		const filteredSuggestions: DropdownOption[] | undefined = useMemo(() => {
			// Get suggestions based on the current path
			const pathSuggestions = getNestedSuggestions(currentPath);
			if (!pathSuggestions) return undefined;

			const searchQuery = searchText ? searchText.toLowerCase().trim() : "";

			return pathSuggestions
				.map((opt) => {
					if (opt.type === "group") {
						// Always filter out hidden options from groups
						const visibleOptions = opt.options?.filter((option: DropdownOption) => !option.hidden) ?? [];

						// If no search query, return all visible options
						if (!searchQuery.trim()) {
							if (visibleOptions.length === 0) return false;
							return {
								...opt,
								options: visibleOptions,
							};
						}

						// With search query, filter by search text
						const groupLabelMatches = opt.label?.toLowerCase().includes(searchQuery) ?? false;

						// Filtering hidden options from display but keep for matching
						const allMatchingOptions =
							opt.options?.filter((option: DropdownOption) => option.label?.toLowerCase().includes(searchQuery) ?? false) ?? [];

						const filteredSubOptions = allMatchingOptions.filter((option: DropdownOption) => !option.hidden);

						if (groupLabelMatches) {
							return {
								...opt,
								options: visibleOptions,
							};
						}

						if (!allMatchingOptions || allMatchingOptions.length === 0) return false;
						return {
							...opt,
							options: filteredSubOptions,
						};
					} else {
						// Always filter out hidden items
						if (opt.hidden) return false;
						// If no search query, return the option
						if (!searchQuery.trim()) return opt;
						// With search query, filter by search text
						return opt.label?.toLowerCase().includes(searchQuery) ? opt : false;
					}
				})
				.filter(Boolean) as DropdownOption[];
		}, [suggestions, nestedSuggestions, searchText, currentPath]);

		const handleChange = (e: React.ChangeEvent<HTMLTextAreaElement>) => {
			const newValue = e.target.value;
			const cursorPos = e.target.selectionStart;
			setCursorPosition(cursorPos);

			// Find the last '{{' before cursor
			const beforeCursor = newValue.slice(0, cursorPos);
			const lastOpenBraceIndex = beforeCursor.lastIndexOf("{{");

			if (lastOpenBraceIndex !== -1 && lastOpenBraceIndex < cursorPos) {
				const textAfterBraces = beforeCursor.slice(lastOpenBraceIndex + 2);
				// Only show dropdown if there's no closing braces in the text after '{{'
				if (!textAfterBraces.includes("}}")) {
					// Check for dot notation
					const parts = textAfterBraces.split(".");
					const searchPart = parts.pop() || "";
					const path = parts.filter(Boolean);

					setCurrentPath(path);
					setSearchText(searchPart);

					const rect = getCaretCoordinates();
					if (rect) {
						// Estimate dropdown height - can be adjusted based on actual content
						const estimatedDropdownHeight = 200; // Default estimate

						// Determine if there's enough space below
						const shouldShowBelow = rect.spaceBelow >= estimatedDropdownHeight || rect.spaceBelow > rect.spaceAbove;

						if (shouldShowBelow) {
							setDropdownPosition({
								top: rect.bottom,
								left: rect.left,
							});
							setDropdownDirection("bottom");
						} else {
							// Position above the cursor
							setDropdownPosition({
								top: rect.bottom - 20, // Adjust for the cursor height
								left: rect.left,
							});
							setDropdownDirection("top");
						}
						if (clickedVariable && enableVariableClickEdit) {
							setClickedVariable(null);
						}
						setShowDropdown(true);
					}
				} else {
					setShowDropdown(false);
					setSearchText("");
					setCurrentPath([]);
				}
			} else {
				setShowDropdown(false);
				setSearchText("");
				setCurrentPath([]);
			}

			onChange?.({ target: { value: newValue } } as React.ChangeEvent<HTMLTextAreaElement>);
		};

		const getCaretCoordinates = () => {
			const textarea = textareaRef.current;
			if (!textarea) return null;

			const position = textarea.selectionStart;
			const text = textarea.value.substring(0, position);
			const div = document.createElement("div");
			const styles = window.getComputedStyle(textarea);

			div.style.position = "absolute";
			div.style.top = "0";
			div.style.left = "0";
			div.style.whiteSpace = "pre-wrap";
			div.style.visibility = "hidden";
			div.style.font = styles.font;
			div.style.padding = styles.padding;
			div.style.width = styles.width;
			div.textContent = text;

			const span = document.createElement("span");
			div.appendChild(span);
			document.body.appendChild(div);

			const rect = span.getBoundingClientRect();
			const textareaRect = textarea.getBoundingClientRect();
			document.body.removeChild(div);

			// Calculate available space above and below the cursor
			const spaceBelow = window.innerHeight - (textareaRect.top + rect.top - textarea.scrollTop + 20);
			const spaceAbove = textareaRect.top + rect.top - textarea.scrollTop;

			return {
				left: textareaRect.left + rect.left - textarea.scrollLeft,
				bottom: textareaRect.top + rect.top - textarea.scrollTop + 20,
				spaceBelow,
				spaceAbove,
			};
		};

		const setSelectionIfStillFocused = (position: number) => {
			setTimeout(() => {
				const textarea = textareaRef.current;
				if (!textarea) return;
				if (document.activeElement !== textarea) return;
				textarea.selectionStart = position;
				textarea.selectionEnd = position;
			}, 0);
		};

		const handleSuggestionSelect = (option: DropdownOption) => {
			if (!textareaRef.current) return;

			const text = value as string;
			let newValue: string;
			const hasChildren = "hasChildren" in option && option.hasChildren;
			// Check if the option value ends with a dot for suggestions array
			const endsWithDot = option.value?.endsWith?.(".") ?? false;

			if (clickedVariable && enableVariableClickEdit) {
				// Replace clicked variable
				newValue = text.slice(0, clickedVariable.start) + option.value + text.slice(clickedVariable.end);
				setClickedVariable(null);

				// Set cursor position after the inserted variable
				setTimeout(() => {
					if (textareaRef.current) {
						if (!option.value) return;
						const newPosition = clickedVariable.start + option.value.length;
						textareaRef.current.selectionStart = newPosition;
						textareaRef.current.selectionEnd = newPosition;
						textareaRef.current.focus();

						// If the option value ends with a dot, update dropdown for nested suggestions
						if (endsWithDot) {
							updateDropdownForChildren(newPosition);
						}
					}
				}, 0);
			} else if (cursorPosition !== null) {
				// Handle normal typing suggestion
				const beforeCursor = text.slice(0, cursorPosition);
				const afterCursor = text.slice(cursorPosition);
				const lastOpenBraceIndex = beforeCursor.lastIndexOf("{{");

				// Check if there are closing braces immediately after the cursor
				// to avoid duplicating them
				let adjustedAfterCursor = afterCursor;
				if (!hasChildren && !endsWithDot && afterCursor.trimStart().startsWith("}}")) {
					// Find the position of the first "}}" after cursor
					const closingBracePos = afterCursor.indexOf("}}");
					// Remove the closing braces
					adjustedAfterCursor = afterCursor.slice(0, closingBracePos) + afterCursor.slice(closingBracePos + 2);
				}

				// Replace everything from '{{' to cursor with the new value
				newValue = text.slice(0, lastOpenBraceIndex) + option.value + adjustedAfterCursor;

				// Set cursor position after the inserted variable
				setTimeout(() => {
					if (textareaRef.current) {
						if (!option.value) return;
						const newPosition = lastOpenBraceIndex + option.value.length;
						textareaRef.current.selectionStart = newPosition;
						textareaRef.current.selectionEnd = newPosition;
						textareaRef.current.focus();

						// If the option has children or ends with a dot, continue showing dropdown
						if (hasChildren || endsWithDot) {
							updateDropdownForChildren(newPosition);
						}
					}
				}, 0);
			} else {
				return;
			}

			onChange?.({ target: { value: newValue } } as React.ChangeEvent<HTMLTextAreaElement>);

			// Only hide dropdown if the option doesn't have children and doesn't end with a dot
			if (!hasChildren && !endsWithDot) {
				setShowDropdown(false);
				setSearchText("");
				setCurrentPath([]);
			}
		};

		const handleVariableClick = (e: React.MouseEvent, start: number, end: number, text: string) => {
			e.preventDefault();
			e.stopPropagation();

			const coordinates = getCaretCoordinates();

			if (coordinates) {
				// Estimate dropdown height - can be adjusted based on actual content
				const estimatedDropdownHeight = 200; // Default estimate

				// Determine if there's enough space below
				const shouldShowBelow = coordinates.spaceBelow >= estimatedDropdownHeight || coordinates.spaceBelow > coordinates.spaceAbove;

				if (shouldShowBelow) {
					setDropdownDirection("bottom");
				}
			}

			const rect = (e.target as HTMLElement).getBoundingClientRect();
			setDropdownPosition({
				top: rect.bottom,
				left: rect.left,
			});

			setClickedVariable({ start, end, text });
			setShowDropdown(true);
		};

		// Helper function to update dropdown for options with children
		const updateDropdownForChildren = (cursorPosition: number) => {
			// Update the current path based on the selected option
			if (textareaRef.current) {
				const text = textareaRef.current.value;
				const beforeCursor = text.slice(0, cursorPosition);
				const lastOpenBraceIndex = beforeCursor.lastIndexOf("{{");
				const pathText = beforeCursor.slice(lastOpenBraceIndex + 2, cursorPosition - 1); // Remove '{{' and trailing dot
				const path = pathText.split(".").filter(Boolean);

				setCurrentPath(path);
				setSearchText("");
				setCursorPosition(cursorPosition);

				// Update dropdown position at the new cursor location
				setTimeout(() => {
					const rect = getCaretCoordinates();
					if (rect) {
						// Estimate dropdown height
						const estimatedDropdownHeight = 200; // Default estimate

						// Determine if there's enough space below
						const shouldShowBelow = rect.spaceBelow >= estimatedDropdownHeight || rect.spaceBelow > rect.spaceAbove;

						if (shouldShowBelow) {
							setDropdownPosition({
								top: rect.bottom,
								left: rect.left,
							});
							setDropdownDirection("bottom");
						} else {
							// Position above the cursor
							setDropdownPosition({
								top: rect.bottom - 20, // Adjust for the cursor height
								left: rect.left,
							});
							setDropdownDirection("top");
						}
						setShowDropdown(true);
					}
				}, 10);
			}
		};

		const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
			if (showDropdown && (e.key === "Escape" || e.key === "Tab")) {
				e.preventDefault();
				setShowDropdown(false);
			}
		};

		const handleSelect = (e: React.SyntheticEvent<HTMLTextAreaElement>) => {
			const textarea = e.target as HTMLTextAreaElement;
		};

		const handleBlur = () => {
			setShowDropdown(false);
			setSearchText("");
			setCurrentPath([]);
		};

		const handleKeyDownCapture = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
			if (e.key === "Space") {
				setSearchText("");
				setCurrentPath([]);
			}
		};

		const syncScroll = (e: React.UIEvent<HTMLTextAreaElement>) => {
			if (preRef.current) {
				preRef.current.scrollTop = e.currentTarget.scrollTop;
				preRef.current.scrollLeft = e.currentTarget.scrollLeft;
			}
		};

		const filePasteHandler = (e: React.ClipboardEvent<HTMLTextAreaElement>) => {
			const items = e.clipboardData.items;
			const files = Array.from(items).filter((item) => item.kind === "file");
			if (files.length === 0) {
				return;
			}
			e.preventDefault();
			e.stopPropagation();
			props.onFilePasted?.(files.map((file) => file.getAsFile() as File));
		};

		// Only run once when the prop toggles from false → true
		const didSelectRef = useRef(false);
		useEffect(() => {
			if (selectAllByDefault && textareaRef.current && !didSelectRef.current) {
				textareaRef.current.select();
				didSelectRef.current = true;
			}
			if (!selectAllByDefault) {
				didSelectRef.current = false;
			}
		}, [selectAllByDefault]);

		useEffect(() => {
			const handleWindowClick = (event: MouseEvent) => {
				const dropdownPortal = document.getElementById("rich-textarea-dropdown-portal");
				if (
					containerRef.current &&
					!containerRef.current.contains(event.target as Node) &&
					!dropdownPortal?.contains(event.target as Node)
				) {
					setShowDropdown(false);
					setSearchText("");
					setCurrentPath([]);
				}
			};
			window.addEventListener("click", handleWindowClick);
			return () => {
				window.removeEventListener("click", handleWindowClick);
			};
		}, []);

		return (
			<div className={cn("relative h-full", className)} ref={containerRef}>
				<TextareaAutosize
					onPaste={props.onFilePasted ? filePasteHandler : undefined}
					ref={textareaRef}
					value={value}
					onChange={handleChange}
					onScroll={syncScroll}
					onKeyDown={handleKeyDown}
					onSelect={handleSelect}
					onBlur={handleBlur}
					onKeyDownCapture={handleKeyDownCapture}
					className={cn(textAreaVariants({ variant, className: textAreaClassName }), "overscroll-auto break-words")}
					minRows={1}
					{...Object.fromEntries(Object.entries(props).filter(([key]) => !["onFilePasted", "style", "onPaste"].includes(key)))}
				/>

				<SuggestionDropdown
					dropdownDirection={dropdownDirection}
					dropdownPosition={dropdownPosition}
					filteredSuggestions={filteredSuggestions}
					handleSuggestionSelect={handleSuggestionSelect}
					noSuggestionText={noSuggestionText}
					suggestionDropdownClassName={suggestionDropdownClassName}
					showDropdown={showDropdown}
				/>

				<pre
					ref={preRef}
					aria-hidden="true"
					className={cn(
						textAreaVariants({ variant, className: textAreaClassName }),
						"no-scrollbar pointer-events-none absolute inset-0 m-0 block w-full bg-transparent font-[inherit] text-muted-foreground",
					)}
					style={{ wordBreak: "break-word" }}
				>
					{typeof value === "string" && (
						<>
							{highlightPattern ? (
								(() => {
									let cumulativePosition = 0;
									return value.split(highlightPattern).map((part, i) => {
										if (!part) return null;
										const currentPartPosition = cumulativePosition;
										cumulativePosition += part.length;

										const matchedPattern = highlightPatterns?.find((pattern) => {
											// Test if this part matches the pattern
											const matches = part.match(pattern.pattern);
											// Validate the match if a validation function is provided
											return matches && (!pattern.validate || pattern.validate(part));
										});
										if (!matchedPattern) {
											if (inlineSuggestionText && part === "{{}}" && textareaRef.current) {
												const currentCursorPos = textareaRef.current.selectionStart;
												const partStartPos = currentPartPosition;
												const partEndPos = currentPartPosition + part.length;
												if (currentCursorPos >= partStartPos && currentCursorPos <= partEndPos) {
													// Position cursor 2 characters from the start of {{}} (inside the braces)
													const updatedPosition = currentPartPosition + 2;
													setSelectionIfStillFocused(updatedPosition);
													return (
														<React.Fragment key={i}>
															{"{{"}
															<span className="text-content-secondary opacity-60">{inlineSuggestionText}</span>
															{"}}"}
														</React.Fragment>
													);
												}
											}
											return <React.Fragment key={i}>{part}</React.Fragment>;
										}
										const className =
											typeof matchedPattern.className === "function" ? matchedPattern.className(part) : matchedPattern.className;
										return (
											<mark key={i} className={cn("rounded-sm pb-0.5 italic outline", className)}>
												{part}
											</mark>
										);
									});
								})()
							) : (
								<React.Fragment>{value}</React.Fragment>
							)}
							{value.endsWith("\n") ? " " : ""}
						</>
					)}
				</pre>
			</div>
		);
	},
);

RichTextarea.displayName = "RichTextarea";

const SuggestionDropdown = ({
	dropdownDirection,
	dropdownPosition,
	filteredSuggestions,
	handleSuggestionSelect,
	noSuggestionText,
	suggestionDropdownClassName,
	portalContainer,
	showDropdown,
}: {
	dropdownDirection: "bottom" | "top";
	dropdownPosition: { top: number; left: number };
	filteredSuggestions?: DropdownOption[];
	handleSuggestionSelect: (option: DropdownOption) => void;
	noSuggestionText?: string;
	suggestionDropdownClassName?: string;
	portalContainer?: Element | null;
	showDropdown: boolean;
}) => {
	if (!showDropdown || !filteredSuggestions || !filteredSuggestions.length) return null;

	return createPortal(
		<div
			style={{
				position: "fixed",
				top: dropdownDirection === "bottom" ? `${dropdownPosition.top}px` : "auto",
				bottom: dropdownDirection === "top" ? `calc(100vh - ${dropdownPosition.top}px)` : "auto",
				left: `${dropdownPosition.left}px`,
				zIndex: 50,
			}}
			id="rich-textarea-dropdown-portal"
		>
			<CustomDropdown
				options={filteredSuggestions}
				onChange={(opt) => handleSuggestionSelect(opt as DropdownOption)}
				className={cn("custom-scrollbar max-h-full min-w-[200px] bg-white p-1 shadow-lg", suggestionDropdownClassName)}
				selectFirstOptionByDefault
				style={{
					maxHeight:
						dropdownDirection === "bottom" ? `calc(100vh - ${dropdownPosition.top}px - 8px)` : `calc(${dropdownPosition.top}px - 8px)`,
				}}
				emptyViewText={noSuggestionText}
				groupHeadingClassName="px-2"
			/>
		</div>,
		portalContainer ?? document.body,
	);
};

export { RichTextarea };