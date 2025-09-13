"use client";

import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";
import { X } from "lucide-react";
import React from "react";

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

	const addCurrentTag = () => {
		const newTag = inputValue.trim();
		if (newTag && !value.includes(newTag)) {
			onValueChange([...value, newTag]);
		}
		setInputValue("");
	};

	const handleKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
		if (e.key === "Enter" || e.key === ",") {
			e.preventDefault();
			addCurrentTag();
		} else if (e.key === "Backspace" && inputValue === "" && value.length > 0) {
			onValueChange(value.slice(0, -1));
		}
	};

	const handleBlur = () => {
		addCurrentTag();
	};

	const removeTag = (tagToRemove: string) => {
		onValueChange(value.filter((tag) => tag !== tagToRemove));
	};

	return (
		<div className={cn("border-input flex flex-wrap items-center gap-2 rounded-sm border", className)}>
			<div className={cn("flex flex-row gap-2", value.length > 0 && "pl-2")}>
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
			</div>
			<Input
				ref={ref}
				type="text"
				value={inputValue}
				onChange={handleInputChange}
				onKeyDown={handleKeyDown}
				onBlur={handleBlur}
				className={cn("min-w-32 flex-1 border-0 shadow-none focus-visible:ring-0", value.length > 0 ? "px-0" : "px-1")}
				{...props}
			/>
		</div>
	);
});

TagInput.displayName = "TagInput";
