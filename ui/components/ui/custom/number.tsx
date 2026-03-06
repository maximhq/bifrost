"use client";

import { VariantProps, cva } from "class-variance-authority";
import React, { useCallback, useEffect, useRef, useState } from "react";
import "./input.css";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "../tooltip";
import { cn } from "../utils";

const inputVariants = cva(
	"flex h-8 w-full rounded-md bg-background-primary px-3 py-2 text-sm file:border-0 file:bg-transparent file:text-md file:font-medium placeholder:text-content-disabled focus-visible:outline-hidden disabled:cursor-not-allowed disabled:opacity-50",
	{
		variants: {
			variant: {
				default: "border border-border-default focus-visible:border-border-focus",
				ghost: "",
			},
		},
		defaultVariants: {
			variant: "default",
		},
	},
);

export interface NumberInputProps
	extends Omit<React.InputHTMLAttributes<HTMLInputElement>, "onChange" | "value" | "defaultValue">,
		VariantProps<typeof inputVariants> {
	min?: number;
	max?: number;
	step?: number;
	allowNegative?: boolean;
	allowDecimal?: boolean;
	decimalPlaces?: number;
	defaultValue?: number;
	value?: string | number;
	onChange?: (value: number | undefined) => void;
	onValueError?: (error: string) => void;
	preventOnBlurFallback?: boolean;
	hideWarnings?: boolean;
}

const NumberInput = React.forwardRef<HTMLInputElement, NumberInputProps>(
	(
		{
			className,
			variant,
			min,
			max,
			step = 1,
			defaultValue,
			allowDecimal = true,
			allowNegative = true,
			decimalPlaces,
			value,
			onChange,
			onValueError,
			preventOnBlurFallback,
			hideWarnings,
			...props
		},
		ref,
	) => {
		// Internal state to handle intermediate values (like empty string or partial input)
		const initialValue = value === undefined ? defaultValue : value;
		const [internalValue, setInternalValue] = useState<string>(() => {
			if (initialValue === undefined || initialValue === null) return "";
			return String(initialValue);
		});
		const [errorMessage, setErrorMessage] = useState<string | null>(null);
		const fallbackValue = defaultValue ?? min ?? 0;
		const prevValueRef = useRef<number | string | undefined>(value);

		// Validate and format the number
		const validateAndFormatNumber = useCallback(
			(value: string): string => {
				if (!value) return "";

				let formattedValue = value;

				// Handle decimal places
				if (decimalPlaces !== undefined && value.includes(".")) {
					const [whole, decimal] = value.split(".");
					formattedValue = `${whole}.${decimal.slice(0, decimalPlaces)}`;
				}

				const numValue = Number(formattedValue);

				// Validate min/max
				if (min !== undefined && numValue < min) {
					onValueError?.(`Value cannot be less than ${min}`);
					setErrorMessage(`Value cannot be less than ${min}`);
					return min.toString();
				}

				if (max !== undefined && numValue > max) {
					onValueError?.(`Value cannot be greater than ${max}`);
					setErrorMessage(`Value cannot be greater than ${max}`);
					return max.toString();
				}

				return formattedValue;
			},
			[min, max, decimalPlaces, onValueError],
		);

		const handleChange = (e: React.ChangeEvent<HTMLInputElement>) => {
			const newValue = e.target.value;

			// Allow empty input
			if (!newValue) {
				setInternalValue("");
				onChange?.(undefined);
				return;
			}

			// Validate number format
			if (!/^-?\d*\.?\d*$/.test(newValue)) return;

			setInternalValue(newValue);

			// Only call onChange with valid numbers
			const parsed = Number(newValue);
			if (!isNaN(parsed)) {
				onChange?.(parsed);
			}
		};

		const handleBlur = (e: React.FocusEvent<HTMLInputElement>) => {
			props.onBlur?.(e);
			if (!internalValue) {
				if (preventOnBlurFallback) {
					onChange?.(undefined);
				} else {
					const normalized = validateAndFormatNumber(String(fallbackValue));
					const normalizedNum = normalized ? Number(normalized) : fallbackValue;
					setInternalValue(String(normalizedNum));
					prevValueRef.current = normalizedNum;
					if (!errorMessage) setErrorMessage(`Value cannot be empty, replaced with ${normalizedNum}`);
					onChange?.(normalizedNum);
				}
				return;
			}

			const formattedValue = validateAndFormatNumber(internalValue);
			setInternalValue(formattedValue);

			if (!errorMessage && !formattedValue) setErrorMessage(`Value cannot be empty, replaced with ${fallbackValue}`);
			onChange?.(formattedValue ? Number(formattedValue) : fallbackValue);
		};

		const handlePaste = (e: React.ClipboardEvent<HTMLInputElement>) => {
			e.preventDefault();
			const pastedText = e.clipboardData.getData("text");

			// Check if the pasted content is a valid number format
			if (!/^-?\d*\.?\d*$/.test(pastedText)) {
				onValueError?.("Invalid number format");
				setErrorMessage("Invalid number format");
				return;
			}

			// Handle decimal restriction
			if (!allowDecimal && pastedText.includes(".")) {
				onValueError?.("Decimal numbers are not allowed");
				setErrorMessage("Decimal numbers are not allowed");
				return;
			}

			// Handle negative restriction
			if (!allowNegative && pastedText.includes("-")) {
				onValueError?.("Negative numbers are not allowed");
				setErrorMessage("Negative numbers are not allowed");
				return;
			}

			const newValue = validateAndFormatNumber(pastedText);
			setInternalValue(newValue);

			// Only call onChange with valid numbers
			const parsed = Number(newValue);
			if (!isNaN(parsed)) {
				onChange?.(parsed);
			}
		};

		const handleKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
			props.onKeyDown?.(e);
			const target = e.target as HTMLInputElement;

			// Allow select all
			if ((e.ctrlKey || e.metaKey) && e.key === "a") {
				return;
			}

			// Allow copy/paste/undo shortcuts
			if ((e.ctrlKey || e.metaKey) && (e.key === "c" || e.key === "v" || e.key === "x" || e.key === "z")) {
				return;
			}

			// Allow control keys
			const allowedKeys = ["Backspace", "Delete", "ArrowLeft", "ArrowRight", "Tab", "Enter", "Home", "End", "ArrowUp", "ArrowDown"];

			if (allowedKeys.includes(e.key)) {
				if (e.key === "ArrowUp") {
					e.preventDefault();
					const currentValue = Number(target.value) || 0;
					const newValue = validateAndFormatNumber((currentValue + step).toString());
					setInternalValue(newValue);
					onChange?.(Number(newValue));
				} else if (e.key === "ArrowDown") {
					e.preventDefault();
					const currentValue = Number(target.value) || 0;
					const newValue = validateAndFormatNumber((currentValue - step).toString());
					setInternalValue(newValue);
					onChange?.(Number(newValue));
				}
				return;
			}

			// Handle negative sign
			if (!allowNegative && e.key === "-") {
				e.preventDefault();
				return;
			}

			// Handle decimal point
			if (!allowDecimal && e.key === ".") {
				e.preventDefault();
				return;
			}

			// Allow negative sign at start
			if (allowNegative && e.key === "-" && target.selectionStart === 0) {
				if (!target.value.includes("-")) return;
				e.preventDefault();
				return;
			}

			// Allow decimal point if configured
			if (allowDecimal && e.key === "." && !target.value.includes(".")) {
				return;
			}

			// Allow only numbers
			if (!/^\d$/.test(e.key)) {
				e.preventDefault();
			}
		};

		useEffect(() => {
			if (!errorMessage) return;
			const timer = setTimeout(() => {
				setErrorMessage(null);
			}, 2000);
			return () => clearTimeout(timer);
		}, [errorMessage]);

		useEffect(() => {
			// Only update if value prop actually changed
			if (prevValueRef.current === value) return;
			prevValueRef.current = value;

			// Clear internal value when value becomes undefined/null/empty
			if (value === undefined || value === null || value === "") {
				setInternalValue("");
				return;
			}
			// Update internal value when value changes to a number
			const stringValue = String(value);
			setInternalValue(stringValue);
		}, [value]);

		return (
			<>
				<TooltipProvider delayDuration={100} disableHoverableContent>
					<Tooltip open={!!errorMessage && !hideWarnings}>
						<TooltipTrigger asChild>
							<input
								ref={ref}
								type="text"
								inputMode={allowDecimal ? "decimal" : "numeric"}
								value={internalValue}
								className={cn(inputVariants({ variant, className }))}
								{...props}
								onChange={handleChange}
								onBlur={handleBlur}
								onKeyDown={handleKeyDown}
								onPaste={handlePaste}
							/>
						</TooltipTrigger>
						<TooltipContent align="start" className="text-md">
							{errorMessage}
						</TooltipContent>
					</Tooltip>
				</TooltipProvider>
			</>
		);
	},
);

NumberInput.displayName = "NumberInput";

export default NumberInput;