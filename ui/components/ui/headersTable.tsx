"use client";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Trash } from "lucide-react";

interface HeadersTableProps {
	value: Record<string, string>;
	onChange: (value: Record<string, string>) => void;
	keyPlaceholder?: string;
	valuePlaceholder?: string;
	label?: string;
	disabled?: boolean;
}

export function HeadersTable({
	value,
	onChange,
	keyPlaceholder = "Header name",
	valuePlaceholder = "Header value",
	label = "Headers",
	disabled = false,
}: HeadersTableProps) {
	// Convert headers object to array format for table display
	// Filter out any empty string keys from stored headers
	const headerEntries = Object.entries(value || {}).filter(([key]) => key !== "");
	// Always show at least one empty row at the bottom
	const rows = [...headerEntries, ["", ""]];

	const handleKeyChange = (oldKey: string, newKey: string, currentValue: string, rowIndex: number) => {
		const newHeaders = { ...value };

		// Remove old key if it exists and is not empty
		if (oldKey !== "" && oldKey in newHeaders) {
			delete newHeaders[oldKey];
		}

		// Only add new entry if key is not empty
		if (newKey !== "") {
			newHeaders[newKey] = currentValue;
		}

		// Clean up any empty string keys
		delete newHeaders[""];

		onChange(newHeaders);
	};

	const handleValueChange = (currentKey: string, newValue: string, rowIndex: number) => {
		const newHeaders = { ...value };

		// Only update if key is not empty
		if (currentKey !== "") {
			newHeaders[currentKey] = newValue;
		}

		// Clean up any empty string keys
		delete newHeaders[""];

		onChange(newHeaders);
	};

	const handleDelete = (key: string) => {
		const newHeaders = { ...value };
		delete newHeaders[key];
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
						{rows.map(([key, value], index) => {
							return (
								<TableRow key={index} className="border-b last:border-0">
									<TableCell className="p-2">
										<Input
											placeholder={keyPlaceholder}
											value={key}
											data-row={index}
											data-column="key"
											onChange={(e) => handleKeyChange(key, e.target.value, value as string, index)}
											onKeyDown={(e) => handleKeyDown(e, index, "key")}
											className="border-0 focus-visible:ring-0 focus-visible:ring-offset-0"
											disabled={disabled}
										/>
									</TableCell>
									<TableCell className="p-2">
										<Input
											placeholder={valuePlaceholder}
											value={value as string}
											data-row={index}
											data-column="value"
											onChange={(e) => handleValueChange(key, e.target.value, index)}
											onKeyDown={(e) => handleKeyDown(e, index, "value")}
											className="border-0 focus-visible:ring-0 focus-visible:ring-offset-0"
											disabled={disabled}
										/>
									</TableCell>
									<TableCell className="p-2">
										{(key !== "" || value !== "") && !disabled && (
											<Button type="button" variant="ghost" size="icon" onClick={() => handleDelete(key)} className="h-8 w-8">
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
