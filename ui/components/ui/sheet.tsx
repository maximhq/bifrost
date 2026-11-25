import * as SheetPrimitive from "@radix-ui/react-dialog";
import { GripVerticalIcon, XIcon } from "lucide-react";
import * as React from "react";
import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import {
	SHEET_MIN_WIDTH,
	SHEET_RIGHT_INSET,
	clampSheetWidth,
	effectiveSheetWidth,
	maxSheetWidth,
	sheetWidthFromPointer,
	sheetWidthKeyboardDelta,
	sheetWidthStorageKey,
	useSheetWidthPreference,
} from "@/lib/hooks/useSheetWidthPreference";
import { cn } from "@/lib/utils";

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

// Tracks the rendered width of `node` so the resize handle can sit on its left edge
// before the user has dragged anything. The observer only exists while the sheet is open.
function useElementWidth(node: HTMLElement | null): number | null {
	const [width, setWidth] = useState<number | null>(null);
	useEffect(() => {
		if (!node || typeof ResizeObserver === "undefined") {
			setWidth(null);
			return;
		}
		const observer = new ResizeObserver(([entry]) => {
			const box = entry.borderBoxSize?.[0];
			setWidth(Math.round(box ? box.inlineSize : entry.target.getBoundingClientRect().width));
		});
		observer.observe(node);
		return () => observer.disconnect();
	}, [node]);
	return width;
}

type SheetResizeHandleProps = {
	width: number;
	dragging: boolean;
	onDragStart: () => void;
	onDrag: (width: number) => void;
	onDragEnd: () => void;
	onStep: (width: number) => void;
	onReset: () => void;
};

// Drag handle on the left edge of a right-side sheet. It is `fixed` so it stays put
// when the sheet's own content scrolls; the sheet has no transform once its enter
// animation ends, so the viewport is its containing block.
function SheetResizeHandle({ width, dragging, onDragStart, onDrag, onDragEnd, onStep, onReset }: SheetResizeHandleProps) {
	const { t } = useTranslation("common");

	const handlePointerDown = (event: React.PointerEvent<HTMLDivElement>) => {
		if (event.button !== 0) return;
		event.preventDefault();
		event.currentTarget.setPointerCapture(event.pointerId);
		onDragStart();
	};

	const handlePointerMove = (event: React.PointerEvent<HTMLDivElement>) => {
		if (!dragging) return;
		onDrag(sheetWidthFromPointer(event.clientX, window.innerWidth));
	};

	const handlePointerEnd = (event: React.PointerEvent<HTMLDivElement>) => {
		if (!dragging) return;
		if (event.currentTarget.hasPointerCapture(event.pointerId)) {
			event.currentTarget.releasePointerCapture(event.pointerId);
		}
		onDragEnd();
	};

	const handleKeyDown = (event: React.KeyboardEvent<HTMLDivElement>) => {
		if (event.key === "Home") {
			event.preventDefault();
			onReset();
			return;
		}
		const delta = sheetWidthKeyboardDelta(event.key);
		if (delta === null) return;
		event.preventDefault();
		onStep(clampSheetWidth(width + delta, window.innerWidth));
	};

	return (
		<div
			role="separator"
			aria-orientation="vertical"
			aria-label={t("sheet.resizeHandle")}
			aria-valuemin={SHEET_MIN_WIDTH}
			aria-valuemax={maxSheetWidth(window.innerWidth)}
			aria-valuenow={width}
			tabIndex={0}
			data-slot="sheet-resize-handle"
			data-testid="sheet-resize-handle"
			data-dragging={dragging || undefined}
			onPointerDown={handlePointerDown}
			onPointerMove={handlePointerMove}
			onPointerUp={handlePointerEnd}
			onPointerCancel={handlePointerEnd}
			onDoubleClick={onReset}
			onKeyDown={handleKeyDown}
			className="group fixed top-2 bottom-2 z-50 hidden w-3 cursor-col-resize touch-none items-center justify-center outline-none sm:flex"
			style={{ right: SHEET_RIGHT_INSET + width - 6 }}
		>
			{/* Same grip as ResizableHandle (the warp dock's divider), shown on hover, focus and drag. */}
			<div className="bg-border flex h-5 w-3 items-center justify-center rounded-sm border opacity-0 transition-opacity group-hover:opacity-100 group-focus-visible:opacity-100 group-data-[dragging=true]:opacity-100">
				<GripVerticalIcon className="size-2.5" />
			</div>
		</div>
	);
}

function SheetContent({
	className,
	children,
	side = "right",
	resizable = true,
	resizeKey,
	style,
	ref,
	onPointerDownOutside,
	onInteractOutside,
	onOpenAutoFocus,
	...props
}: React.ComponentProps<typeof SheetPrimitive.Content> & {
	side?: "top" | "right" | "bottom" | "left";
	/** Lets the user drag the left edge of a right-side sheet to change its width. On by default. */
	resizable?: boolean;
	/** Identifies the sheet for the saved width. Defaults to the sheet's `data-testid`; without either the width is not saved. */
	resizeKey?: string;
}) {
	const canResize = resizable && side === "right";
	const storageId = resizeKey ?? (props as { "data-testid"?: string })["data-testid"];
	const [widthOverride, setWidthOverride, resetWidth] = useSheetWidthPreference(
		canResize && storageId ? sheetWidthStorageKey(storageId) : undefined,
	);
	const [contentNode, setContentNode] = useState<HTMLDivElement | null>(null);
	const measuredWidth = useElementWidth(canResize ? contentNode : null);
	const [dragging, setDragging] = useState(false);
	const dragWidth = useRef<number | null>(null);

	// Ends a drag and saves the width it reached. Runs on pointer-up, and also when the
	// content unmounts mid-drag (the sheet closed by Escape): the handle is gone by then,
	// so its pointer-up never arrives, and this component stays mounted with the sheet closed.
	const endDrag = useCallback(() => {
		setDragging(false);
		if (dragWidth.current !== null) setWidthOverride(dragWidth.current, true);
		dragWidth.current = null;
	}, [setWidthOverride]);

	// Radix re-attaches contentRef on every render, so a null ref call does not mean the sheet closed.
	useEffect(() => (contentNode ? endDrag : undefined), [contentNode, endDrag]);

	const contentRef = useCallback(
		(node: HTMLDivElement | null) => {
			setContentNode(node);
			if (typeof ref === "function") ref(node);
			else if (ref) ref.current = node;
		},
		[ref],
	);

	// Check if the target is a portaled element (like react-select menu)
	const isPortaledElement = (target: HTMLElement | null): boolean => {
		if (!target) return false;
		return !!(
			target.closest('[class*="-menu"]') ||
			target.closest('[class*="MenuPortal"]') ||
			target.closest('[role="listbox"]') ||
			target.closest('[role="option"]') ||
			target.closest("[data-radix-popper-content-wrapper]") ||
			target.closest("[data-sonner-toast]") ||
			target.closest("[data-sonner-toaster]")
		);
	};

	const handlePointerDownOutside = (event: React.PointerEvent | CustomEvent) => {
		const target = (event as CustomEvent).detail?.originalEvent?.target as HTMLElement;
		if (isPortaledElement(target)) {
			event.preventDefault();
			return;
		}
		onPointerDownOutside?.(event as any);
	};

	const handleInteractOutside = (event: React.FocusEvent | CustomEvent) => {
		const target = (event as CustomEvent).detail?.originalEvent?.target as HTMLElement;
		if (isPortaledElement(target)) {
			event.preventDefault();
			return;
		}
		onInteractOutside?.(event as any);
	};

	// The handle sits on the rendered edge, which CSS caps when a saved width is wider than the viewport.
	const handleWidth = effectiveSheetWidth(widthOverride, measuredWidth, window.innerWidth);
	const resized = canResize && widthOverride !== null;

	return (
		<SheetPortal>
			<SheetOverlay />
			<SheetPrimitive.Content
				ref={contentRef}
				data-slot="sheet-content"
				onPointerDownOutside={handlePointerDownOutside}
				onInteractOutside={handleInteractOutside}
				onOpenAutoFocus={(e) => {
					e.preventDefault();
					onOpenAutoFocus?.(e);
				}}
				className={cn(
					"bg-card data-[state=open]:animate-in data-[state=closed]:animate-out custom-scrollbar fixed z-50 flex flex-col shadow-lg transition-all ease-in-out overscroll-none data-[state=closed]:duration-100 data-[state=open]:duration-100",
					side === "right" &&
						"data-[state=closed]:slide-out-to-right data-[state=open]:slide-in-from-right top-2 right-2 bottom-2 h-auto w-full rounded-sm border sm:w-3/4 sm:max-w-2xl",
					side === "left" &&
						"data-[state=closed]:slide-out-to-left data-[state=open]:slide-in-from-left top-2 bottom-2 left-2 h-auto w-full rounded-sm border sm:w-3/4 sm:max-w-sm",
					side === "top" && "data-[state=closed]:slide-out-to-top data-[state=open]:slide-in-from-top inset-x-0 top-2 h-auto border",
					side === "bottom" &&
						"data-[state=closed]:slide-out-to-bottom data-[state=open]:slide-in-from-bottom inset-x-0 bottom-2 h-auto border",
					className,
					// A saved width beats every call site's width classes at sm and up; below sm the sheet stays full width.
					resized && "sm:w-(--sheet-width)! sm:min-w-0! sm:max-w-[calc(100vw-1rem)]!",
					dragging && "transition-none select-none",
				)}
				style={resized ? ({ ...style, "--sheet-width": `${widthOverride}px` } as React.CSSProperties) : style}
				{...props}
			>
				{children}
				{canResize && handleWidth !== null && (
					<SheetResizeHandle
						width={handleWidth}
						dragging={dragging}
						onDragStart={() => {
							dragWidth.current = null;
							setDragging(true);
						}}
						onDrag={(width) => {
							dragWidth.current = width;
							setWidthOverride(width);
						}}
						onDragEnd={endDrag}
						onStep={(width) => setWidthOverride(width, true)}
						onReset={resetWidth}
					/>
				)}
			</SheetPrimitive.Content>
		</SheetPortal>
	);
}

function SheetHeader({
	className,
	children,
	headerClassName,
	showCloseButton = true,
	...props
}: React.ComponentProps<"div"> & { showCloseButton?: boolean; headerClassName?: string }) {
	const { t } = useTranslation("common");

	return (
		<div data-slot="sheet-header" className={cn("mb-6 flex w-full items-center gap-1 pr-0", headerClassName)} {...props}>
			<div className={cn("flex h-full min-w-0 flex-1 flex-row items-center", className)}>{children}</div>
			{showCloseButton && (
				<SheetPrimitive.Close className="hover:bg-accent shrink-0 cursor-pointer rounded-md p-2 opacity-70 transition-opacity hover:opacity-100">
					<XIcon className="size-4" />
					<span className="sr-only">{t("close")}</span>
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