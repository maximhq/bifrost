import { ComboboxSelect } from "@/components/ui/combobox";
import { type ContentLoggingChoice, type ContentLoggingLabels, contentLoggingOptions } from "@/components/contentLoggingSelect.utils";
import { useMemo } from "react";

interface ContentLoggingSelectProps {
	value: ContentLoggingChoice;
	onValueChange: (choice: ContentLoggingChoice) => void;
	// The translated names of the three choices for this kind of entity.
	labels: ContentLoggingLabels;
	// Test ids are `<testIdPrefix>-select` and `<testIdPrefix>-option-<choice>`.
	testIdPrefix: string;
	// Forwarded to the trigger so a FormControl or <Label htmlFor> can target it.
	id?: string;
	disabled?: boolean;
}

// ContentLoggingSelect is the one three-way content-logging control every entity editor uses
// (virtual key, team, provider key, and the enterprise levels), so the choices and their test ids
// read the same everywhere.
export function ContentLoggingSelect({ value, onValueChange, labels, testIdPrefix, id, disabled }: ContentLoggingSelectProps) {
	const { inherit, off, on } = labels;
	const options = useMemo(() => contentLoggingOptions({ inherit, off, on }), [inherit, off, on]);
	return (
		<ComboboxSelect
			id={id}
			options={options}
			value={value}
			onValueChange={(next) => onValueChange((next as ContentLoggingChoice) ?? "inherit")}
			disableSearch
			hideClear
			disabled={disabled}
			data-testid={`${testIdPrefix}-select`}
			optionTestId={(choice) => `${testIdPrefix}-option-${choice}`}
		/>
	);
}