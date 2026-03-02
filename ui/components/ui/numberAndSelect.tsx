import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Input } from "@/components/ui/input";
import React from "react";

const NumberAndSelect = ({
	id,
	label,
	value,
	selectValue,
	onChangeNumber,
	onChangeSelect,
	options,
	labelClassName,
	dataTestId,
}: {
	id: string;
	label: string;
	value: string;
	selectValue: string;
	onChangeNumber: (value: string) => void;
	onChangeSelect: (value: string) => void;
	options: { label: string; value: string }[];
	labelClassName?: string;
	dataTestId?: string;
}) => {
	return (
		<div className="flex w-full items-center justify-between gap-4">
			<div className="grow space-y-2">
				<Label htmlFor={id} className={labelClassName}>
					{label}
				</Label>
				<Input
					id={id}
					data-testid={dataTestId}
					placeholder="100"
					value={value}
					onChange={(e) => {
						const inputValue = e.target.value;
						// Allow empty string, numbers, and partial decimal inputs like "0."
						if (inputValue === "" || !isNaN(parseFloat(inputValue)) || inputValue.endsWith(".")) {
							onChangeNumber(inputValue);
						}
					}}
					onBlur={(e) => {
						const inputValue = e.target.value.trim();
						if (inputValue === "") {
							onChangeNumber("");
						} else {
							const num = parseFloat(inputValue);
							if (!isNaN(num)) {
								onChangeNumber(String(num));
							} else {
								onChangeNumber("");
							}
						}
					}}
					type="text"
				/>
			</div>
			<div className="w-40 space-y-2">
				<Label htmlFor={`${id}-select`} className={labelClassName}>
					Reset Period
				</Label>
				<Select value={selectValue} onValueChange={(value) => onChangeSelect(value as string)}>
					<SelectTrigger className="m-0 w-full">
						<SelectValue />
					</SelectTrigger>
					<SelectContent>
						{options.filter((option) => option.value).map((option) => (
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
