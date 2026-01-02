"use client";

import * as SheetPrimitive from "@radix-ui/react-dialog";
import { ArrowLeftFromLineIcon, ArrowRightFromLineIcon, XIcon } from "lucide-react";
import * as React from "react";
import { createContext, useContext, useState } from "react";

import { cn } from "@/lib/utils";

// Context to share expanded state between SheetContent and SheetHeader
type SheetContextValue = {
	expanded: boolean;
	setExpanded: (expanded: boolean) => void;
	side: "top" | "right" | "bottom" | "left";
	expandable: boolean;
};

const SheetContext = createContext<SheetContextValue | null>(null);

function useSheetContext() {
	const context = useContext(SheetContext);
	return context;
}

function Sheet({ ...props }: React.ComponentProps<typeof SheetPrimitive.Root>) {
	return <SheetPrimitive.Root data-slot="sheet" {...props} />;
}

function SheetTrigger({ ...props }: React.ComponentProps<typeof SheetPrimitive.Trigger>) {
	return <SheetPrimitive.Trigger data-slot="sheet-trigger" {...props} />;
}

function SheetClose({ ...props }: React.ComponentProps<typeof SheetPrimitive.Close>) {
	return <SheetPrimitive.Close data-slot="sheet-close" {...props} />;
}

function SheetPortal({ ...props }: React.ComponentProps<typeof SheetPrimitive.Portal>) {
	return <SheetPrimitive.Portal data-slot="sheet-portal" {...props} />;
}

function SheetOverlay({ className, ...props }: React.ComponentProps<typeof SheetPrimitive.Overlay>) {
	return (
		<SheetPrimitive.Overlay
			data-slot="sheet-overlay"
			className={cn(
				"data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0 fixed inset-0 z-50 bg-black/50",
				className,
			)}
			{...props}
		/>
	);
}

function SheetContent({
	className,
	children,
	side = "right",
	expandable = false,
	...props
}: React.ComponentProps<typeof SheetPrimitive.Content> & {
	side?: "top" | "right" | "bottom" | "left";
	expandable?: boolean;
}) {
	const [expanded, setExpanded] = useState(false);

	return (
		<SheetContext.Provider value={{ expanded, setExpanded, side, expandable }}>
			<SheetPortal>
				<SheetOverlay />
				<SheetPrimitive.Content
					data-slot="sheet-content"
					className={cn(
						"bg-background data-[state=open]:animate-in data-[state=closed]:animate-out fixed z-50 flex flex-col shadow-lg transition-all ease-in-out data-[state=closed]:duration-100 data-[state=open]:duration-100",
						side === "right" &&
							"data-[state=closed]:slide-out-to-right data-[state=open]:slide-in-from-right top-2 right-0 bottom-2 h-auto w-3/4 rounded-l-lg border-l",
						side === "right" && (!expandable || !expanded) && "sm:max-w-2xl",
						side === "right" && expandable && expanded && "sm:max-w-5xl",
						side === "left" &&
							"data-[state=closed]:slide-out-to-left data-[state=open]:slide-in-from-left top-2 bottom-2 left-0 h-auto w-3/4 rounded-r-lg border-r sm:max-w-sm",
						side === "top" && "data-[state=closed]:slide-out-to-top data-[state=open]:slide-in-from-top inset-x-0 top-0 h-auto border-b",
						side === "bottom" &&
							"data-[state=closed]:slide-out-to-bottom data-[state=open]:slide-in-from-bottom inset-x-0 bottom-0 h-auto border-t",
						className,
					)}
					{...props}
				>
					{children}
				</SheetPrimitive.Content>
			</SheetPortal>
		</SheetContext.Provider>
	);
}

function SheetHeader({
	className,
	children,
	showCloseButton = true,
	...props
}: React.ComponentProps<"div"> & { showCloseButton?: boolean }) {
	const sheetContext = useSheetContext();

	return (
		<div data-slot="sheet-header" className={cn("flex items-center", sheetContext?.expandable ? "p-0" : "mb-6")} {...props}>
			{sheetContext?.expandable && sheetContext?.side === "right" && (
				<button
					type="button"
					onClick={() => sheetContext?.setExpanded(!sheetContext?.expanded)}
					className="-ml-5 shrink-0 cursor-pointer opacity-70 transition-opacity hover:scale-105 hover:opacity-100"
				>
					{sheetContext?.expanded ? <ArrowRightFromLineIcon className="size-4" /> : <ArrowLeftFromLineIcon className="size-4" />}
					<span className="sr-only">{sheetContext?.expanded ? "Collapse" : "Expand"}</span>
				</button>
			)}

			<div className={cn("flex h-full min-w-0 flex-1 flex-row items-center", sheetContext?.expandable ? "ml-1" : "ml-4", className)}>
				{children}
			</div>
			{showCloseButton && (
				<SheetPrimitive.Close className="hover:bg-accent shrink-0 cursor-pointer rounded-md p-2 opacity-70 transition-opacity hover:scale-105 hover:opacity-100 ml-1">
					<XIcon className="size-4" />
					<span className="sr-only">Close</span>
				</SheetPrimitive.Close>
			)}
		</div>
	);
}

function SheetFooter({ className, ...props }: React.ComponentProps<"div">) {
	return <div data-slot="sheet-footer" className={cn("mt-auto flex flex-col gap-2 p-4", className)} {...props} />;
}

function SheetTitle({ className, ...props }: React.ComponentProps<typeof SheetPrimitive.Title>) {
	return <SheetPrimitive.Title data-slot="sheet-title" className={cn("text-foreground font-semibold", className)} {...props} />;
}

function SheetDescription({ className, ...props }: React.ComponentProps<typeof SheetPrimitive.Description>) {
	return <SheetPrimitive.Description data-slot="sheet-description" className={cn("text-muted-foreground text-sm", className)} {...props} />;
}

export { Sheet, SheetClose, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle, SheetTrigger };
