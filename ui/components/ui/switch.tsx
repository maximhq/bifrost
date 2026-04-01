"use client";

import * as SwitchPrimitives from "@radix-ui/react-switch";
import * as React from "react";

import { cn } from "@/lib/utils";

interface SwitchProps extends React.ComponentPropsWithoutRef<typeof SwitchPrimitives.Root> {
	size?: "default" | "md";
}

const Switch = React.forwardRef<React.ElementRef<typeof SwitchPrimitives.Root>, SwitchProps>(
	({ className, size = "md", ...props }, ref) => (
		<SwitchPrimitives.Root
			className={cn(
				"peer focus-visible:ring-ring focus-visible:ring-offset-background data-[state=checked]:bg-primary data-[state=unchecked]:bg-input inline-flex shrink-0 cursor-pointer items-center rounded-sm border-2 border-transparent transition-colors focus-visible:ring-2 focus-visible:ring-offset-2 focus-visible:outline-none disabled:cursor-not-allowed disabled:opacity-50",
				size === "default" && "h-6 w-11",
				size === "md" && "h-5 w-9",
				className,
			)}
			{...props}
			ref={ref}
		>
			<SwitchPrimitives.Thumb
				className={cn(
					"pointer-events-none block rounded-sm bg-white shadow-lg ring-0 transition-transform dark:bg-zinc-900",
					size === "default" && "h-5 w-5 data-[state=checked]:translate-x-5 data-[state=unchecked]:translate-x-0",
					size === "md" && "h-4 w-4 data-[state=checked]:translate-x-4 data-[state=unchecked]:translate-x-0",
				)}
			/>
		</SwitchPrimitives.Root>
	),
);
Switch.displayName = SwitchPrimitives.Root.displayName;

export { Switch };
