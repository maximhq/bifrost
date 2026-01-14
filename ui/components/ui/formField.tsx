"use client";

import React, { useState } from "react";
import { Input } from "./input";
import { Label } from "./label";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "./tooltip";
import { Info } from "lucide-react";
import { cn } from "@/lib/utils";
import { ValidationConfig, validateField } from "@/lib/utils/validation";

interface FormFieldProps {
	label?: string;
	validation?: ValidationConfig;
	onChange?: (value: string) => void;
	className?: string;
	tooltipSide?: "top" | "right" | "bottom" | "left";
	value?: string;
	[key: string]: any; // Allow any additional input props
}

export function FormField({
	label,
	validation,
	onChange,
	className,
	tooltipSide = "right",
	value: initialValue = "",
	...props
}: FormFieldProps) {
	const [touched, setTouched] = useState(false);
	const [value, setValue] = useState(initialValue);

	const handleChange = (e: React.ChangeEvent<HTMLInputElement>) => {
		const newValue = e.target.value;
		setValue(newValue);
		onChange?.(newValue);
	};

	const handleBlur = () => {
		setTouched(true);
	};

	const validationResult = validation ? validateField(value, validation, touched) : { isValid: true, message: "", showTooltip: false };

	return (
		<div className={cn("space-y-2", className)}>
			{label && (
				<div className="flex items-center space-x-2">
					<Label>{label}</Label>
					{validation && (validationResult.showTooltip || validation.showAlways) && (
						<TooltipProvider>
							<Tooltip>
								<TooltipTrigger asChild>
									<Info className={cn("h-4 w-4", validationResult.isValid ? "text-muted-foreground" : "text-destructive")} />
								</TooltipTrigger>
								<TooltipContent side={tooltipSide}>
									<p>{validationResult.message}</p>
								</TooltipContent>
							</Tooltip>
						</TooltipProvider>
					)}
				</div>
			)}
			<Input
				{...props}
				value={value}
				onChange={handleChange}
				onBlur={handleBlur}
				className={cn(!validationResult.isValid && touched && "border-destructive", props.className)}
			/>
		</div>
	);
}
