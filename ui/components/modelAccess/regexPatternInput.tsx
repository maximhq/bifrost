import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";
import { Plus } from "lucide-react";
import { useState } from "react";
import { validateModelRegex } from "./utils";

interface RegexPatternInputProps {
	/** Called with the raw pattern (no prefix) once it passes validation. */
	onAdd: (pattern: string) => void;
	disabled?: boolean;
	placeholder?: string;
	"data-testid"?: string;
	inputId?: string;
	/** id of the element describing the input, e.g. a form error message (accessibility) */
	ariaDescribedBy?: string;
	/** marks the input invalid for assistive tech, on top of the pattern validator (accessibility) */
	ariaInvalid?: boolean;
	className?: string;
}

/**
 * One-line pattern editor: type an RE2 pattern, press Enter or click Add.
 * Invalid patterns show the validator's message inline and are not added.
 */
export function RegexPatternInput({ onAdd, disabled, placeholder, inputId, ariaDescribedBy, ariaInvalid, className, ...rest }: RegexPatternInputProps) {
	const [pattern, setPattern] = useState("");
	const [error, setError] = useState<string | null>(null);
	const testId = rest["data-testid"];

	const commit = () => {
		const trimmed = pattern.trim();
		const problem = validateModelRegex(trimmed);
		if (problem) {
			setError(problem);
			return;
		}
		onAdd(trimmed);
		setPattern("");
		setError(null);
	};

	return (
		<div className={cn("space-y-1", className)}>
			<div className="flex items-center gap-1.5">
				<Input
					id={inputId}
					data-testid={testId ? `${testId}-input` : undefined}
					value={pattern}
					disabled={disabled}
					placeholder={placeholder ?? "^gpt-4.*"}
					className={cn("h-9 font-mono text-sm", error && "border-destructive focus-visible:ring-destructive/30")}
					aria-describedby={ariaDescribedBy}
					aria-invalid={!!error || ariaInvalid}
					onChange={(e) => {
						setPattern(e.target.value);
						if (error) setError(null);
					}}
					onKeyDown={(e) => {
						if (e.key === "Enter") {
							e.preventDefault();
							commit();
						}
					}}
				/>
				<Button
					type="button"
					variant="outline"
					size="sm"
					className="h-9 shrink-0"
					disabled={disabled || pattern.trim() === ""}
					onClick={commit}
					data-testid={testId ? `${testId}-add` : undefined}
				>
					<Plus className="h-3.5 w-3.5" />
					Add
				</Button>
			</div>
			{error ? (
				<p className="text-destructive text-xs">{error}</p>
			) : (
				<p className="text-muted-foreground text-xs">Matched against the model name and provider/model, case-insensitive, full match.</p>
			)}
		</div>
	);
}