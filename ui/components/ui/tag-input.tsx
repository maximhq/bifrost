"use client";

import React from "react";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { X } from "lucide-react";
import { cn } from "@/lib/utils";

type OmittedInputProps = Omit<React.InputHTMLAttributes<HTMLInputElement>, "value" | "onChange">;

interface TagInputProps extends OmittedInputProps {
	value: string[];
	onValueChange: (value: string[]) => void;
}

export const TagInput = React.forwardRef<HTMLInputElement, TagInputProps>(({ className, value, onValueChange, ...props }, ref) => {
	const [inputValue, setInputValue] = React.useState("");

	const handleInputChange = (e: React.ChangeEvent<HTMLInputElement>) => {
		setInputValue(e.target.value);
	};

	const handleKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
		if (e.key === "Enter" || e.key === ",") {
			e.preventDefault();
			const newTag = inputValue.trim();
			if (newTag && !value.includes(newTag)) {
				onValueChange([...value, newTag]);
			}
			setInputValue("");
		} else if (e.key === "Backspace" && inputValue === "" && value.length > 0) {
			onValueChange(value.slice(0, -1));
		}
	};

	const removeTag = (tagToRemove: string) => {
		onValueChange(value.filter((tag) => tag !== tagToRemove));
	};

	return (
		<div className={cn("border-input flex flex-wrap items-center gap-2 rounded-md border p-2", className)}>
			{value.map((tag) => (
				<Badge key={tag} variant="secondary" className="flex items-center gap-1">
					{tag}
					<button
						type="button"
						className="ring-offset-background focus:ring-ring cursor-pointer rounded-full outline-none focus:ring-2 focus:ring-offset-2"
						onClick={() => removeTag(tag)}
					>
						<X className="h-3 w-3" />
					</button>
				</Badge>
			))}
			<Input
				ref={ref}
				type="text"
				value={inputValue}
				onChange={handleInputChange}
				onKeyDown={handleKeyDown}
				className="flex-1 border-0 shadow-none focus-visible:ring-0"
				{...props}
			/>
		</div>
	);
});

TagInput.displayName = "TagInput";
