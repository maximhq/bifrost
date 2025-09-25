"use client";

import * as React from "react";

import { cn } from "@/lib/utils";
import { CopyIcon } from "lucide-react";
import { toast } from "sonner";
import { Button } from "./button";

export interface InputProps extends React.InputHTMLAttributes<HTMLInputElement> {
	showCopyButton?: boolean;
	inputClassName?: string;
}

export const Input = React.forwardRef<HTMLInputElement, InputProps>(
	({ className, type, showCopyButton = false, inputClassName, ...props }, ref) => {
		if (showCopyButton) {
			return (
				<div
					className={cn(
						"dark:bg-input/30 border-input focus-visible:border-primary flex w-full items-center rounded-sm border bg-transparent",
						className,
					)}
				>
					<input
						type={type}
						ref={ref}
						data-slot="input"
						className={cn(
							"file:text-foreground placeholder:text-muted-foreground/70 selection:bg-primary selection:text-primary-foreground flex h-9 w-full min-w-0 px-3 py-1 text-base shadow-none transition-[color,box-shadow] outline-none file:inline-flex file:h-7 file:border-0 file:bg-transparent file:text-sm file:font-medium disabled:pointer-events-none disabled:cursor-not-allowed disabled:opacity-50 md:text-sm",
							"aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 aria-invalid:border-destructive",
							inputClassName,
						)}
						{...props}
					/>
					<Button
						className="ml-auto"
						variant="ghost"
						size="icon"
						onClick={() => {
							if (typeof props.value === "string") {
								navigator.clipboard.writeText(props.value as string);
							} else {
								navigator.clipboard.writeText(JSON.stringify(props.value));
							}
							toast.success("Copied to clipboard");
						}}
					>
						<CopyIcon className="h-4 w-4" />
					</Button>
				</div>
			);
		}
		return (
			<input
				type={type}
				ref={ref}
				data-slot="input"
				className={cn(
					"file:text-foreground placeholder:text-muted-foreground/70 selection:bg-primary selection:text-primary-foreground dark:bg-input/30 border-input focus-visible:border-primary flex h-9 w-full min-w-0 rounded-sm border bg-transparent px-3 py-1 text-base shadow-none transition-[color,box-shadow] outline-none file:inline-flex file:h-7 file:border-0 file:bg-transparent file:text-sm file:font-medium disabled:pointer-events-none disabled:cursor-not-allowed disabled:opacity-50 md:text-sm",
					"aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 aria-invalid:border-destructive",
					className,
					inputClassName,
				)}
				{...props}
			/>
		);
	},
);

Input.displayName = "Input";
