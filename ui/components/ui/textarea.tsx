import * as React from "react";
import TextareaAutosize, { type TextareaAutosizeProps } from "react-textarea-autosize";

import { cn } from "@/lib/utils";

function Textarea({ className, ...props }: React.ComponentProps<"textarea">) {
	return (
		<textarea
			data-slot="textarea"
			className={cn(
				"border-input placeholder:text-muted-foreground aria-invalid:border-destructive dark:bg-input/30 focus-visible:border-primary flex field-sizing-content min-h-16 w-full rounded-sm border bg-transparent px-3 py-2 text-base shadow-none transition-[color,box-shadow] outline-none disabled:cursor-not-allowed disabled:opacity-50 md:text-sm",
				className,
			)}
			{...props}
		/>
	);
}

function AutoSizeTextarea({ className, ...props }: TextareaAutosizeProps) {
	return (
		<TextareaAutosize
			data-slot="textarea"
			className={cn(
				"border-input placeholder:text-muted-foreground aria-invalid:border-destructive dark:bg-input/30 focus-visible:border-primary flex min-h-16 w-full rounded-sm border bg-transparent px-3 py-2 text-base shadow-none transition-[color,box-shadow] outline-none disabled:cursor-not-allowed disabled:opacity-50 md:text-sm",
				className,
			)}
			{...props}
		/>
	);
}

export { Textarea, AutoSizeTextarea };