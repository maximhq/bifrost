import { Textarea } from "@/components/ui/textarea";
import { parseArrayFromText } from "@/lib/utils/array";
import { useEffect, useRef, useState } from "react";

interface RequestHeadersTextareaProps {
	value: string[];
	onChange: (value: string[]) => void;
	disabled?: boolean;
	className?: string;
	placeholder?: string;
	"data-testid"?: string;
}

export function RequestHeadersTextarea({ value, onChange, disabled, className, placeholder, ...rest }: RequestHeadersTextareaProps) {
	const [text, setText] = useState(() => (value ?? []).join(", "));
	const textRef = useRef(text);
	textRef.current = text;

	useEffect(() => {
		if (parseArrayFromText(textRef.current).join(",") !== (value ?? []).join(",")) {
			setText((value ?? []).join(", "));
		}
	}, [value]);

	return (
		<Textarea
			className={className}
			placeholder={placeholder}
			disabled={disabled}
			value={text}
			onChange={(e) => {
				setText(e.target.value);
				onChange(parseArrayFromText(e.target.value));
			}}
			data-testid={rest["data-testid"]}
		/>
	);
}