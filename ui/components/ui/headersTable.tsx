import { Button } from "@/components/ui/button";
import { EnvVarInput } from "@/components/ui/envVarInput";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { EnvVar } from "@/lib/types/mcp";
import { cn } from "@/lib/utils";
import { Trash } from "lucide-react";
import React, { useRef, useState } from "react";

// Support both plain string values and EnvVar objects
type HeaderValue = string | EnvVar;

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
	useEnvVarInput?: boolean;
	/** Optional custom renderer for the key (name) cell input */
	renderKeyInput?: (params: CellRenderParams) => React.ReactNode;
	/** Optional custom renderer for the value cell input */
	renderValueInput?: (params: CellRenderParams) => React.ReactNode;
}

// Empty EnvVar for new rows
const emptyEnvVar: EnvVar = { value: "", env_var: "", from_env: false };

// Helper to check if a value is an EnvVar object
const isEnvVar = (val: HeaderValue): val is EnvVar => {
	return typeof val === "object" && val !== null && "value" in val;
};

// Helper to get display value from HeaderValue
const getDisplayValue = (val: HeaderValue): string => {
	if (isEnvVar(val)) {
		return val.value || "";
	}
	return val;
};

// Helper to check if a HeaderValue is empty
const isValueEmpty = (val: HeaderValue): boolean => {
	if (isEnvVar(val)) {
		return !val.value && !val.env_var;
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
	useEnvVarInput,
	renderKeyInput,
	renderValueInput,
}: HeadersTableProps<T>) {
	// Use explicit prop if provided, otherwise detect from existing values
	const isEnvVarMode = useEnvVarInput ?? Object.values(value || {}).some((v) => isEnvVar(v));

	// Track duplicate key conflicts: maps rowIndex -> attempted duplicate key
	const [duplicateConflicts, setDuplicateConflicts] = useState<Map<number, string>>(new Map());
	// Track which row to highlight (for scroll-to-existing behavior)
	const [highlightedRow, setHighlightedRow] = useState<number | null>(null);
	// Refs for each table row to enable scrolling
	const rowRefs = useRef<(HTMLTableRowElement | null)[]>([]);

	// Get the empty value based on mode
	const getEmptyValue = (): T => {
		if (isEnvVarMode) {
			return emptyEnvVar as T;
		}
		return "" as T;
	};

	// Convert headers object to array format for table display
	// Filter out any empty string keys from stored headers
	const headerEntries = Object.entries(value || {});
	// Always show at least one empty row at the bottom
	const rows: [string, T][] = [...headerEntries, ["", getEmptyValue()]];

	const handleKeyChange = (oldKey: string, newKey: string, currentValue: T, rowIndex: number) => {
		// Check if newKey already exists (and it's not the current row's original key)
		const isDuplicate = newKey !== "" && newKey !== oldKey && newKey in value;

		if (isDuplicate) {
			// Duplicate detected - store conflict key locally, let user continue typing
			// Don't update parent value (would overwrite existing entry)
			setDuplicateConflicts((prev) => new Map(prev).set(rowIndex, newKey));
			return;
		}

		// Key is unique - clear any previous conflict for this row
		setDuplicateConflicts((prev) => {
			const next = new Map(prev);
			next.delete(rowIndex);
			return next;
		});

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

	const handleValueChange = (currentKey: string, newValue: string | EnvVar, rowIndex: number) => {
		const newHeaders = { ...value };

		if (isEnvVarMode) {
			// If newValue is already an EnvVar, use it directly
			if (typeof newValue === "object") {
				newHeaders[currentKey] = newValue as T;
			} else {
				// When user types, create a new EnvVar with the typed value
				newHeaders[currentKey] = { value: newValue, env_var: "", from_env: false } as T;
			}
		} else {
			newHeaders[currentKey] = (typeof newValue === "string" ? newValue : newValue.value) as T;
		}

		onChange(newHeaders);
	};

	const handleDelete = (key: string, rowIndex: number) => {
		// If this row has a conflict, just clear the conflict (don't modify value)
		if (duplicateConflicts.has(rowIndex)) {
			setDuplicateConflicts((prev) => {
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
		setDuplicateConflicts((prev) => {
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
				<Table>
					<TableHeader>
						<TableRow>
							<TableHead className="px-4 py-2">Name</TableHead>
							<TableHead className="px-4 py-2">Value</TableHead>
							<TableHead className="w-12 px-4 py-2">
								<span className="sr-only">Actions</span>
							</TableHead>
						</TableRow>
					</TableHeader>
					<TableBody>
						{rows.map(([key, headerValue], index) => {
							const isHeaderEnvVar = isEnvVar(headerValue);
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
											) : (
												<Input
													placeholder={keyPlaceholder}
													value={hasConflict ? conflictKey : key}
													data-row={index}
													data-column="key"
													onChange={(e) => handleKeyChange(key, e.target.value, headerValue, index)}
													onKeyDown={(e) => handleKeyDown(e, index, "key")}
													className={cn("border-0 focus-visible:ring-0 focus-visible:ring-offset-0", hasConflict && "text-destructive")}
													disabled={disabled}
												/>
											)}
											{hasConflict && <span className="text-destructive px-3 text-xs">Duplicate key</span>}
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
										) : isHeaderEnvVar ? (
											<EnvVarInput
												placeholder={valuePlaceholder}
												value={headerValue as EnvVar}
												data-row={index}
												data-column="value"
												onChange={(envVar) => handleValueChange(key, envVar, index)}
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
												onChange={(e) => handleValueChange(key, e.target.value, index)}
												onKeyDown={(e) => handleKeyDown(e, index, "value")}
												className="border-0 focus-visible:ring-0 focus-visible:ring-offset-0"
												disabled={disabled}
											/>
										)}
									</TableCell>
									<TableCell className="p-2">
										{!disabled && !isEmptyTrailingRow && (
											<Button type="button" variant="ghost" size="icon" onClick={() => handleDelete(key, index)} className="h-8 w-8">
												<Trash className="h-4 w-4" />
											</Button>
										)}
									</TableCell>
								</TableRow>
							);
						})}
					</TableBody>
				</Table>
			</div>
		</div>
	);
}