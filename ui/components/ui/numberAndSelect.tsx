import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Input } from "@/components/ui/input";
import { cleanNumericInput } from "@/lib/utils/strings";
import React, { useEffect, useState } from "react";

const NumberAndSelect = ({
	id,
	label,
	value,
	selectValue,
	onChangeNumber,
	onChangeSelect,
	options,
	labelClassName,
	placeholder = "100",
	dataTestId,
	inputClassName,
}: {
	id: string;
	label: string;
	value: number | undefined;
	onChangeNumber: (value: number | undefined) => void;
	selectValue?: string;
	onChangeSelect?: (value: string) => void;
	options?: { label: string; value: string }[];
	labelClassName?: string;
	placeholder?: string;
	dataTestId?: string;
	inputClassName?: string;
}) => {
	// Internal string state to allow intermediate inputs like "0." or ""
	const [displayValue, setDisplayValue] = useState(value !== undefined ? String(value) : "");

	// Sync from prop when it changes externally (e.g. loading saved data).
	// displayValue is intentionally omitted from deps — we only want to update
	// when the external `value` prop changes, not on every keystroke.
	useEffect(() => {
		const displayNum = displayValue === "" ? undefined : parseFloat(displayValue);
		if (value !== displayNum) {
			setDisplayValue(value !== undefined ? String(value) : "");
		}
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [value]);

	const showSelect = selectValue !== undefined && onChangeSelect && options;

	const numberInput = (
		<>
			<Label htmlFor={id} className={labelClassName}>
				{label}
			</Label>
			<Input
				id={id}
				data-testid={dataTestId}
				placeholder={placeholder}
				className={inputClassName}
				value={displayValue}
				onChange={(e) => {
					const cleaned = cleanNumericInput(e.target.value);
					setDisplayValue(cleaned);
					if (cleaned === "" || cleaned === ".") {
						onChangeNumber(undefined);
					} else {
						const n = Number(cleaned);
						if (!isNaN(n)) {
							onChangeNumber(n);
						}
					}
				}}
				onBlur={() => {
					const trimmed = displayValue.trim();
					if (trimmed === "" || trimmed === ".") {
						setDisplayValue("");
						onChangeNumber(undefined);
					} else {
						const num = Number(trimmed);
						if (!isNaN(num)) {
							setDisplayValue(String(num));
							onChangeNumber(num);
						} else {
							setDisplayValue("");
							onChangeNumber(undefined);
						}
					}
				}}
				type="text"
			/>
		</>
	);

	if (!showSelect) {
		return <div className="space-y-2">{numberInput}</div>;
	}

	return (
		<div className="flex w-full items-center justify-between gap-4">
			<div className="grow space-y-2">{numberInput}</div>
			<div className="w-40 space-y-2">
				<Label htmlFor={`${id}-select`} className={labelClassName}>
					Reset Period
				</Label>
				<Select value={selectValue} onValueChange={(value) => onChangeSelect(value as string)}>
					<SelectTrigger className="m-0 w-full">
						<SelectValue />
					</SelectTrigger>
					<SelectContent>
						{options
							.filter((option) => option.value)
							.map((option) => (
								<SelectItem key={option.value} value={option.value}>
									{option.label}
								</SelectItem>
							))}
					</SelectContent>
				</Select>
			</div>
		</div>
	);
};

export default NumberAndSelect;