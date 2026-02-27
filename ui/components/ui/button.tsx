import { Slot } from "@radix-ui/react-slot";
import { cva, type VariantProps } from "class-variance-authority";
import * as React from "react";

import { cn } from "@/lib/utils";
import { Loader2 } from "lucide-react";

const buttonVariants = cva(
	"inline-flex items-center ring-none justify-center gap-2 whitespace-nowrap rounded-sm text-sm font-medium transition-all disabled:pointer-events-none disabled:opacity-50 [&_svg]:pointer-events-none [&_svg:not([class*='size-'])]:size-4 shrink-0 [&_svg]:shrink-0 outline-none  aria-invalid:border-destructive active:scale-[0.99] transition-transform duration-100",
	{
		variants: {
			variant: {
				default: "bg-primary text-primary-foreground  hover:bg-primary/90",
				destructive: "bg-destructive text-white font-normal  hover:bg-destructive/90  dark:bg-destructive/60",
				outline:
					"border bg-background hover:bg-accent hover:text-accent-foreground dark:bg-input/30 dark:border-input dark:hover:bg-input/50",
				secondary: "bg-secondary text-secondary-foreground  hover:bg-secondary/80",
				ghost: "hover:bg-accent hover:text-accent-foreground dark:hover:bg-accent/50",
				link: "text-primary underline-offset-4 hover:underline",
			},
			size: {
				default: "h-7.5 px-2 py-1 has-[>svg]:px-2",
				sm: "h-8 rounded-sm gap-1.5 px-3 has-[>svg]:px-2.5",
				lg: "h-10 rounded-sm px-6 has-[>svg]:px-4",
				icon: "size-9",
			},
		},
		defaultVariants: {
			variant: "default",
			size: "default",
		},
	},
);

function Button({
	className,
	variant,
	size,
	asChild = false,
	children,
	isLoading = false,
	...props
}: React.ComponentProps<"button"> &
	VariantProps<typeof buttonVariants> & {
		asChild?: boolean;
		isLoading?: boolean;
	}) {
	return (
		<BaseButton className={className} variant={variant} size={size} asChild={asChild} {...props}>
			{isLoading ? <Loader2 className="size-4 animate-spin" /> : children}
		</BaseButton>
	);
}

function BaseButton({
	className,
	variant,
	size,
	asChild = false,
	...props
}: React.ComponentProps<"button"> &
	VariantProps<typeof buttonVariants> & {
		asChild?: boolean;
	}) {
	const Comp = asChild ? Slot : "button";

	return <Comp data-slot="button" className={cn(buttonVariants({ variant, size, className }), "cursor-pointer")} {...props} />;
}

export { Button, buttonVariants };
