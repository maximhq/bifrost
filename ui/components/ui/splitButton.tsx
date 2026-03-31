'use client'

import { cn } from "@/lib/utils";
import { Content } from "@radix-ui/react-dropdown-menu";
import { cva, type VariantProps } from "class-variance-authority";
import { Check, ChevronDown, Loader2 } from "lucide-react";
import React, { MouseEventHandler, useCallback, useRef, useState } from "react";
import { Button, buttonVariants } from "./button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuTrigger } from "./dropdownMenu";

export const splitButtonVariants = cva(
  'inline-flex items-center justify-center whitespace-nowrap rounded-md text-sm font-medium',
  {
    variants: {
      variant: {
        default: 'text-foreground bg-background',
        primary: 'bg-primary text-primary-foreground hover:bg-primary/80',
        outline:
          'hover:bg-accent hover:text-accent-foreground dark:bg-input/30 dark:hover:bg-input/50',
      },
      size: {
        default: 'font-medium text-sm leading-5 h-[32px]',
      },
    },
    defaultVariants: {
      variant: 'default',
      size: 'default',
    },
  }
)

export interface SplitButtonProps extends Omit<React.HTMLAttributes<HTMLDivElement>, "onClick">, VariantProps<typeof splitButtonVariants> {
	button?: Omit<React.ButtonHTMLAttributes<HTMLButtonElement> & VariantProps<typeof buttonVariants>, "children" | "onClick"> & {
		dataTestId?: string;
	};
	dropdownTrigger?: Omit<React.ButtonHTMLAttributes<HTMLButtonElement> & VariantProps<typeof buttonVariants>, "onClick"> & {
		dataTestId?: string;
	};
	dropdownContent: React.ComponentPropsWithoutRef<typeof Content> & {
		open?: boolean;
		onOpenChange?: (open: boolean) => void;
	};
	disabled?: boolean;
	isLoading?: boolean;
	onClick?: MouseEventHandler<HTMLButtonElement>;
	disabledCheck?: boolean;
}

const a11yClasses =
	"ring-offset-background focus-visible:outline-none focus-visible:ring-0 focus-visible:ring-offset-0 disabled:pointer-events-none disabled:opacity-50";

export const SplitButton = React.forwardRef<HTMLDivElement, SplitButtonProps>(
	(
		{ className, variant, size, children, button, dropdownContent, dropdownTrigger, disabled, isLoading, onClick, disabledCheck, ...props },
		ref,
	) => {
		const [clicked, setClicked] = useState(false);
		const [contentWidth, setContentWidth] = useState(0);
		const [open, setOpen] = useState(false);
		const buttonRef = useRef<HTMLButtonElement | null>(null);

		const memoizedRefCallback = useCallback(
			(el: HTMLButtonElement | null) => {
				if (!clicked && el) {
					setContentWidth(el.clientWidth);
				}
				buttonRef.current = el;
			},
			[clicked],
		);

		const { className: buttonClassName, variant: buttonVariant, disabled: buttonDisabled, ...buttonProps } = button ?? {};
		const {
			className: dropdownTriggerClassName,
			variant: dropdownTriggerVariant,
			children: dropdownTriggerChildren,
			disabled: dropdownTriggerDisabled,
			dataTestId: dropdownTriggerDataTestId,
			...dropdownTriggerProps
		} = dropdownTrigger ?? {};
		const {
			className: dropdownContentClassName,
			open: dropdownContentOpen,
			onOpenChange: dropdownContentOnOpenChange,
			align: dropdownContentAlign,
			...dropdownContentProps
		} = dropdownContent ?? {};

		return (
			<div className={cn(splitButtonVariants({ variant, size, className }))} ref={ref} {...props}>
				<Button
					ref={memoizedRefCallback}
					className={cn(
						"h-full rounded-r-none border active:scale-100",
						a11yClasses,
						clicked ? "disabled:opacity-100" : "",
						buttonClassName ?? "",
					)}
					dataTestId={buttonProps.dataTestId}
					variant={buttonVariant ?? "outline"}
					disabled={buttonDisabled || disabled || clicked || isLoading}
					onClick={async (e) => {
						if (onClick) {
							try {
								await onClick(e);
								if (!disabledCheck) {
									setClicked(true);
									setTimeout(() => setClicked(false), 1000);
								}
							} catch (err) {
								throw err;
							}
						}
					}}
					{...buttonProps}
				>
					{clicked ? (
						<div style={{ width: contentWidth - 24 }} className="flex items-center justify-center">
							<Check className="h-4 w-4" />
						</div>
					) : isLoading ? (
						<div style={{ width: contentWidth - 24 }} className="flex items-center justify-center">
							<Loader2 className="h-4 w-4 animate-spin" />
						</div>
					) : (
						children
					)}
				</Button>
				<DropdownMenu open={dropdownContentOpen ?? open} onOpenChange={dropdownContentOnOpenChange ?? setOpen}>
					<DropdownMenuTrigger asChild>
						<Button
							className={cn(
								"h-full w-[32px] shrink-0 rounded-l-none border border-l-0 p-0 active:scale-100",
								a11yClasses,
								dropdownTriggerClassName ?? "",
							)}
							variant={dropdownTriggerVariant ?? "outline"}
							disabled={dropdownTriggerDisabled || disabled || isLoading}
							{...dropdownTriggerProps}
							dataTestId={dropdownTriggerDataTestId}
						>
							{dropdownTriggerChildren ?? <ChevronDown className="h-4 w-4" />}
						</Button>
					</DropdownMenuTrigger>
					<DropdownMenuContent
						className={cn(dropdownContentClassName ?? "")}
						align={dropdownContentAlign ?? "end"}
						{...dropdownContentProps}
					/>
				</DropdownMenu>
			</div>
		);
	},
);
SplitButton.displayName = 'SplitButton'
