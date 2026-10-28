import { Button } from "@/components/ui/button";
import { SecretVarInput } from "@/components/ui/secretVarInput";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { SecretVar } from "@/lib/types/mcp";
import { cn } from "@/lib/utils";
import { Trash } from "lucide-react";
import React, { useRef, useState } from "react";

// A key name a plain object cannot hold as an own entry (see handleKeyChange).
const UNSUPPORTED_KEY = "__proto__";

// Support both plain string values and SecretVar objects
type HeaderValue = string | SecretVar;

export interface CellRenderParams {
	value: string;
	onChange: (val: string) => void;
	placeholder?: string;
	disabled?: boolean;
	rowKey: string;
}

interface HeadersTableProps<T extends HeaderValue> {
	value: Record<string, T>;
	onChange: (value: Record<string, T>) => void;
	keyPlaceholder?: string;
	valuePlaceholder?: string;
	label?: string;
	disabled?: boolean;
	useSecretVarInput?: boolean;
	/**
	 * When provided, the table renders exactly these keys as read-only,
	 * non-deletable rows (no trailing "add" row). Values stay editable unless
	 * `disabled` is set. Use this for fixed-schema key sets like a stdio
	 * server's required environment variables.
	 */
	fixedKeys?: string[];
	/** Optional custom renderer for the key (name) cell input */
	renderKeyInput?: (params: CellRenderParams) => React.ReactNode;
	/** Optional custom renderer for the value cell input */
	renderValueInput?: (params: CellRenderParams) => React.ReactNode;
	/** When set, the built-in inputs get `<prefix>-name-<row>` and `<prefix>-value-<row>` test ids. */
	testIdPrefix?: string;
	/**
	 * Keep a row whose name is cleared, under an empty name, instead of removing it with its value.
	 * For callers that validate the result (an empty name with a value is an error there), so a
	 * rename in progress cannot silently drop the entry.
	 */
	keepClearedNames?: boolean;
	/**
	 * Called with the rows the table holds as conflicts (row index -> the name typed there) whenever they
	 * change, and with the headers those indices refer to (after a delete, the remaining rows). The table
	 * keeps such a row's last valid name in `value`; a caller that validates names can use this to block
	 * saving until the conflict is resolved. With keepClearedNames, deleting a conflicted row removes it.
	 */
	onConflictsChange?: (conflicts: ReadonlyMap<number, string>, headers: Record<string, T>) => void;
}

/**
 * Whether the table shows its empty add row. With keepClearedNames a cleared row keeps its value under
 * the empty name, which is also the add row's key, so the add row is hidden until that row is named or
 * removed; otherwise typing into it would overwrite the cleared row's value.
 */
export function showsAddRow(value: Record<string, unknown>, keepClearedNames: boolean): boolean {
	return !(keepClearedNames && Object.hasOwn(value, ""));
}

/**
 * The headers after row `rowIndex` is renamed to `newKey`, for keepClearedNames: the row keeps its
 * position and value, and a cleared name stays as an "" entry. A plain object holds one "" key, so a
 * second cleared row cannot be kept and undefined is returned; the caller holds it as a row conflict.
 */
export function renameHeaderKeepingCleared<T>(
	value: Record<string, T>,
	rowIndex: number,
	newKey: string,
	currentValue: T,
): Record<string, T> | undefined {
	const entries = Object.entries(value);
	if (rowIndex >= entries.length) return newKey === "" ? value : { ...value, [newKey]: currentValue };
	if (newKey === "" && entries.some(([k], i) => k === "" && i !== rowIndex)) return undefined;
	return Object.fromEntries(entries.map(([k, v], i) => (i === rowIndex ? [newKey, currentValue] : [k, v])));
}

/**
 * The headers and conflicts after row `rowIndex` is deleted, for keepClearedNames: the row goes even when
 * it holds a conflict (its message offers to remove it), and conflicts below it move up with their rows.
 * A row past the headers (the add row) only loses its conflict.
 */
export function deleteHeaderRow<T>(
	value: Record<string, T>,
	conflicts: ReadonlyMap<number, string>,
	rowIndex: number,
): { value: Record<string, T>; conflicts: Map<number, string> } {
	const shifted = new Map<number, string>();
	conflicts.forEach((name, i) => {
		if (i < rowIndex) shifted.set(i, name);
		else if (i > rowIndex) shifted.set(i - 1, name);
	});
	return { value: Object.fromEntries(Object.entries(value).filter((_, i) => i !== rowIndex)), conflicts: shifted };
}

// Empty SecretVar for new rows
const emptySecretVar: SecretVar = { value: "", ref: "" };

// Helper to check if a value is an SecretVar object
const isSecretVar = (val: HeaderValue): val is SecretVar => {
	return typeof val === "object" && val !== null && "value" in val;
};

// Helper to get display value from HeaderValue
const getDisplayValue = (val: HeaderValue): string => {
	if (isSecretVar(val)) {
		return val.value || "";
	}
	return val;
};

// Helper to check if a HeaderValue is empty
const isValueEmpty = (val: HeaderValue): boolean => {
	if (isSecretVar(val)) {
		return !val.value && !val.ref;
	}
	return !val;
};

export function HeadersTable<T extends HeaderValue>({
	value,
	onChange,
	keyPlaceholder = "Header name",
	valuePlaceholder = "Header value",
	label = "Headers",
	disabled = false,
	useSecretVarInput,
	fixedKeys,
	renderKeyInput,
	renderValueInput,
	testIdPrefix,
	keepClearedNames = false,
	onConflictsChange,
}: HeadersTableProps<T>) {
	// Use explicit prop if provided, otherwise detect from existing values
	const isSecretVarMode = useSecretVarInput ?? Object.values(value || {}).some((v) => isSecretVar(v));

	// Track duplicate key conflicts: maps rowIndex -> attempted duplicate key
	const [duplicateConflicts, setDuplicateConflicts] = useState<Map<number, string>>(new Map());
	// Every conflict change goes through here, so onConflictsChange hears it before the matching onChange.
	// Without that callback this is the plain functional update the table has always used.
	// `headers` is what the indices refer to: the current value, or the remaining rows after a delete.
	const updateConflicts = (update: (prev: Map<number, string>) => Map<number, string>, headers: Record<string, T> = value) => {
		if (!onConflictsChange) {
			setDuplicateConflicts(update);
			return;
		}
		const next = update(duplicateConflicts);
		setDuplicateConflicts(next);
		onConflictsChange(next, headers);
	};
	// Track which row to highlight (for scroll-to-existing behavior)
	const [highlightedRow, setHighlightedRow] = useState<number | null>(null);
	// Refs for each table row to enable scrolling
	const rowRefs = useRef<(HTMLTableRowElement | null)[]>([]);

	// Get the empty value based on mode
	const getEmptyValue = (): T => {
		if (isSecretVarMode) {
			return emptySecretVar as T;
		}
		return "" as T;
	};

	const isFixedKeys = Array.isArray(fixedKeys);

	// Convert headers object to array format for table display
	// Filter out any empty string keys from stored headers
	const headerEntries = Object.entries(value || {});
	// In fixed-keys mode the rows are exactly the supplied keys (read-only,
	// no trailing add-row). Otherwise show one empty row at the bottom, unless a
	// cleared row already holds the empty name (see showsAddRow).
	const rows: [string, T][] = fixedKeys
		? fixedKeys.map((key) => [key, value?.[key] ?? getEmptyValue()] as [string, T])
		: showsAddRow(value || {}, keepClearedNames)
			? [...headerEntries, ["", getEmptyValue()]]
			: headerEntries;

	const handleKeyChange = (oldKey: string, newKey: string, currentValue: T, rowIndex: number) => {
		// Check if newKey already exists (and it's not the current row's original key)
		// Own keys only: `in` also sees inherited names such as constructor or toString.
		const isDuplicate = newKey !== "" && newKey !== oldKey && Object.hasOwn(value, newKey);
		// "__proto__" can't be stored as a key of a plain object: assigning it sets the prototype
		// instead, so the row would vanish and the key would silently never be saved. Hold it as a
		// row conflict, like a duplicate, so the row stays visible with an explanation.
		const isUnsupported = newKey === UNSUPPORTED_KEY;

		if (isDuplicate || isUnsupported) {
			// Duplicate detected - store conflict key locally, let user continue typing
			// Don't update parent value (would overwrite existing entry)
			updateConflicts((prev) => new Map(prev).set(rowIndex, newKey));
			return;
		}

		// Key is unique - clear any previous conflict for this row
		updateConflicts((prev) => {
			const next = new Map(prev);
			next.delete(rowIndex);
			return next;
		});

		if (keepClearedNames) {
			const next = renameHeaderKeepingCleared(value, rowIndex, newKey, currentValue);
			if (next) onChange(next);
			else updateConflicts((prev) => new Map(prev).set(rowIndex, ""));
			return;
		}

		// Rebuild the object preserving key order so the row doesn't jump to the end
		const newHeaders: Record<string, T> = {};
		for (const [k, v] of Object.entries(value) as [string, T][]) {
			if (k === oldKey && oldKey !== "") {
				// Replace old key with new key at the same position
				if (newKey !== "") {
					newHeaders[newKey] = currentValue;
				}
			} else if (k !== "") {
				newHeaders[k] = v;
			}
		}

		// If this was a new (empty-key) row, append at the end
		if (oldKey === "" && newKey !== "") {
			newHeaders[newKey] = currentValue;
		}

		onChange(newHeaders);
	};

	const handleValueChange = (currentKey: string, newValue: string | SecretVar, rowIndex: number) => {
		const newHeaders = { ...value };

		if (isSecretVarMode) {
			// If newValue is already an SecretVar, use it directly
			if (typeof newValue === "object") {
				newHeaders[currentKey] = newValue as T;
			} else {
				// When user types, create a new SecretVar with the typed value
				newHeaders[currentKey] = { value: newValue, ref: "" } as T;
			}
		} else {
			newHeaders[currentKey] = (typeof newValue === "string" ? newValue : newValue.value) as T;
		}

		onChange(newHeaders);
	};

	const handleDelete = (key: string, rowIndex: number) => {
		if (keepClearedNames) {
			const next = deleteHeaderRow(value, duplicateConflicts, rowIndex);
			updateConflicts(() => next.conflicts, next.value);
			onChange(next.value);
			return;
		}

		// If this row has a conflict, just clear the conflict (don't modify value)
		if (duplicateConflicts.has(rowIndex)) {
			updateConflicts((prev) => {
				const next = new Map(prev);
				next.delete(rowIndex);
				return next;
			});
			return;
		}

		// Delete the actual header
		const newHeaders = { ...value };
		delete newHeaders[key];

		// Shift down conflict indices for rows after the deleted one
		updateConflicts((prev) => {
			const next = new Map<number, string>();
			prev.forEach((conflictKey, conflictRowIndex) => {
				if (conflictRowIndex > rowIndex) {
					next.set(conflictRowIndex - 1, conflictKey);
				} else if (conflictRowIndex < rowIndex) {
					next.set(conflictRowIndex, conflictKey);
				}
				// If conflictRowIndex === rowIndex, we drop it (row being deleted)
			});
			return next;
		});

		onChange(newHeaders);
	};

	const handleKeyDown = (e: React.KeyboardEvent<HTMLInputElement>, rowIndex: number, column: "key" | "value") => {
		if (e.key === "Tab" && !e.shiftKey) {
			if (column === "key") {
				e.preventDefault();
				const valueInput = document.querySelector(`input[data-row="${rowIndex}"][data-column="value"]`) as HTMLInputElement;
				valueInput?.focus();
			}
		}
	};

	return (
		<div className="w-full">
			{label && (
				<label className="mb-2 block text-sm leading-none font-medium peer-disabled:cursor-not-allowed peer-disabled:opacity-70">
					{label}
				</label>
			)}
			<div className="rounded-md border">
				<Table className="table-fixed">
					<TableHeader>
						<TableRow>
							<TableHead className="w-[40%] px-4 py-2">Name</TableHead>
							<TableHead className="px-4 py-2">Value</TableHead>
							{!isFixedKeys && (
								<TableHead className="w-10 p-0">
									<span className="sr-only">Actions</span>
								</TableHead>
							)}
						</TableRow>
					</TableHeader>
					<TableBody>
						{rows.map(([key, headerValue], index) => {
							const isHeaderSecretVar = isSecretVar(headerValue);
							const hasConflict = duplicateConflicts.has(index);
							const conflictKey = duplicateConflicts.get(index);
							const isHighlighted = highlightedRow === index;
							const isEmptyTrailingRow = index === rows.length - 1 && key === "" && isValueEmpty(headerValue);

							return (
								<TableRow
									key={index}
									ref={(el) => {
										rowRefs.current[index] = el;
									}}
									className={cn(
										"border-b last:border-0 transition-colors",
										isHighlighted && "bg-yellow-100 dark:bg-yellow-900/20 animate-pulse",
									)}
								>
									<TableCell className="p-2">
										<div>
											{renderKeyInput ? (
												renderKeyInput({
													value: hasConflict ? (conflictKey ?? "") : key,
													onChange: (newKey) => handleKeyChange(key, newKey, headerValue, index),
													placeholder: keyPlaceholder,
													disabled,
													rowKey: key,
												})
											) : isFixedKeys ? (
												<Input
													value={key}
													readOnly
													data-row={index}
													data-column="key"
													className="border-0 font-mono text-xs focus-visible:ring-0 focus-visible:ring-offset-0"
												/>
											) : (
												<Input
													placeholder={keyPlaceholder}
													value={hasConflict ? conflictKey : key}
													data-row={index}
													data-column="key"
													data-testid={testIdPrefix ? `${testIdPrefix}-name-${index}` : undefined}
													onChange={(e) => handleKeyChange(key, e.target.value, headerValue, index)}
													onKeyDown={(e) => handleKeyDown(e, index, "key")}
													className={cn("border-0 focus-visible:ring-0 focus-visible:ring-offset-0", hasConflict && "text-destructive")}
													disabled={disabled}
												/>
											)}
											{hasConflict && (
												<span className="text-destructive px-3 text-xs">
													{conflictKey === UNSUPPORTED_KEY
														? "This key name is not supported"
														: conflictKey === ""
															? "Enter a name or remove the row"
															: "Duplicate key"}
												</span>
											)}
										</div>
									</TableCell>
									<TableCell className="p-2">
										{renderValueInput ? (
											renderValueInput({
												value: getDisplayValue(headerValue),
												onChange: (newVal) => handleValueChange(key, newVal, index),
												placeholder: valuePlaceholder,
												disabled,
												rowKey: key,
											})
										) : isHeaderSecretVar ? (
											<SecretVarInput
												placeholder={valuePlaceholder}
												value={headerValue as SecretVar}
												data-row={index}
												data-column="value"
												data-testid={testIdPrefix ? `${testIdPrefix}-value-${index}` : undefined}
												onChange={(secretVar) => handleValueChange(key, secretVar, index)}
												onKeyDown={(e) => handleKeyDown(e, index, "value")}
												className="border-0 focus-visible:ring-0 focus-visible:ring-offset-0"
												disabled={disabled}
											/>
										) : (
											<Input
												placeholder={valuePlaceholder}
												value={getDisplayValue(headerValue)}
												data-row={index}
												data-column="value"
												data-testid={testIdPrefix ? `${testIdPrefix}-value-${index}` : undefined}
												onChange={(e) => handleValueChange(key, e.target.value, index)}
												onKeyDown={(e) => handleKeyDown(e, index, "value")}
												className="border-0 focus-visible:ring-0 focus-visible:ring-offset-0"
												disabled={disabled}
											/>
										)}
									</TableCell>
									{!isFixedKeys && (
										<TableCell className="p-0">
											{!disabled && !isEmptyTrailingRow && (
												<Button type="button" variant="ghost" size="icon" onClick={() => handleDelete(key, index)} className="h-8 w-8">
													<Trash className="h-4 w-4" />
												</Button>
											)}
										</TableCell>
									)}
								</TableRow>
							);
						})}
					</TableBody>
				</Table>
			</div>
		</div>
	);
}