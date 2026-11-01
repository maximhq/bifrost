import { Input } from "@/components/ui/input";
import { useEffect, useState, type ComponentProps } from "react";

// Digits with at most one decimal point. A native number input also takes "e", "+" and "-", shows
// them, and reports an empty value, so the form and the field disagree about what was entered.
const DECIMAL = /^\d*\.?\d*$/;

const parse = (text: string) => (text === "" || text === "." ? null : Number(text));

/** What typing `raw` does: undefined refuses the keystroke; otherwise the text to keep and the value to report. */
export function readDecimalText(raw: string): { text: string; value: number | null } | undefined {
	const text = raw.trim();
	return DECIMAL.test(text) ? { text, value: parse(text) } : undefined;
}
/**
 * A number as the plain decimal text the field accepts. String() writes 5e-7 for 0.0000005, which
 * the field refuses, so a loaded price could not be edited; the exponent form is expanded using
 * its own digits, so nothing is rounded or padded.
 */
export const decimalText = (value: number | null | undefined): string => {
	if (value === null || value === undefined || Number.isNaN(value)) return "";
	const text = String(value);
	const exp = /^(\d+)(?:\.(\d+))?e([+-]\d+)$/.exec(text);
	if (!exp) return text;
	const exponent = Number(exp[3]);
	if (exponent > 0) return BigInt(value).toString();
	return value.toFixed((exp[2]?.length ?? 0) - exponent);
};

/**
 * A non-negative decimal field. It keeps the text as typed ("0." stays "0.") and reports a number,
 * or null once emptied so the field reads as unset.
 */
export function DecimalInput({
	value,
	onChange,
	...props
}: { value: number | null | undefined; onChange: (value: number | null) => void } & Omit<
	ComponentProps<typeof Input>,
	"value" | "onChange" | "type"
>) {
	const [text, setText] = useState(() => decimalText(value));

	// Follow a value set from outside (a reset, or a reopened sheet) unless it is what the text already says.
	useEffect(() => {
		setText((current) => (parse(current) === (value ?? null) ? current : decimalText(value)));
	}, [value]);

	return (
		<Input
			{...props}
			type="text"
			inputMode="decimal"
			autoComplete="off"
			value={text}
			onChange={(e) => {
				const next = readDecimalText(e.target.value);
				if (!next) return;
				setText(next.text);
				onChange(next.value);
			}}
		/>
	);
}