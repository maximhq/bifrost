"use client";

import { Button } from "@/components/ui/button";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "@/components/ui/dropdownMenu";
import { cn } from "@/lib/utils";
import { Header, flexRender } from "@tanstack/react-table";
import { ArrowLeftToLine, ArrowRightToLine, Ellipsis, EyeOff, PinOff } from "lucide-react";
import { CSSProperties, useState } from "react";

export const TH_CLASS = "text-foreground h-10 px-4 text-left align-middle font-medium whitespace-nowrap";

export function DraggableColumnHeader<TData>({
	header,
	isConfigurable,
	pinStyle,
	pinnedHeaderClassName,
	className: extraClassName,
	onHide,
	onPin,
	onDrop,
	cellRef,
}: {
	header: Header<TData, unknown>;
	isConfigurable: boolean;
	pinStyle: CSSProperties;
	pinnedHeaderClassName?: string;
	className?: string;
	onHide: (id: string) => void;
	onPin: (id: string, position: "left" | "right") => void;
	onDrop: (draggedId: string, targetId: string) => void;
	cellRef: (el: HTMLTableCellElement | null) => void;
}) {
	const [isDragging, setIsDragging] = useState(false);
	const [isDropTarget, setIsDropTarget] = useState(false);
	const pinned = header.column.getIsPinned();

	return (
		<th
			ref={cellRef}
			style={pinStyle}
			className={cn(
				TH_CLASS,
				pinned && (pinnedHeaderClassName ?? "bg-card"),
				isDragging && "opacity-50",
				isDropTarget && "ring-primary ring-inset ring-1",
				isConfigurable && "cursor-grab active:cursor-grabbing",
				extraClassName,
			)}
			draggable={isConfigurable}
			onDragStart={(e) => {
				setIsDragging(true);
				e.dataTransfer.setData("text/plain", header.column.id);
				e.dataTransfer.effectAllowed = "move";
			}}
			onDragEnd={() => setIsDragging(false)}
			onDragOver={(e) => {
				if (!isConfigurable) return;
				e.preventDefault();
				e.dataTransfer.dropEffect = "move";
				setIsDropTarget(true);
			}}
			onDragLeave={() => setIsDropTarget(false)}
			onDrop={(e) => {
				e.preventDefault();
				setIsDropTarget(false);
				const draggedId = e.dataTransfer.getData("text/plain");
				if (draggedId && draggedId !== header.column.id) {
					onDrop(draggedId, header.column.id);
				}
			}}
		>
			{header.isPlaceholder ? null : (
				<div className="group/col flex items-center">
					<div className="flex-1">
						{flexRender(header.column.columnDef.header, header.getContext())}
					</div>
					{isConfigurable && (
						<DropdownMenu>
							<DropdownMenuTrigger asChild>
								<Button
									type="button"
									variant="ghost"
									size="sm"
									className="ml-1 shrink-0 opacity-0 transition-opacity group-hover/col:opacity-100 focus-visible:opacity-100"
									onClick={(e) => e.stopPropagation()}
									onMouseDown={(e) => e.stopPropagation()}
									aria-label="Column actions"
								>
									<Ellipsis className="h-3.5 w-3.5" />
								</Button>
							</DropdownMenuTrigger>
							<DropdownMenuContent align="start" side="bottom">
								<DropdownMenuItem onClick={() => onHide(header.column.id)}>
									<EyeOff className="h-4 w-4" />
									Hide column
								</DropdownMenuItem>
								<DropdownMenuSeparator />
								{pinned === "left" ? (
									<DropdownMenuItem onClick={() => onPin(header.column.id, "left")}>
										<PinOff className="h-4 w-4" />
										Unpin
									</DropdownMenuItem>
								) : (
									<DropdownMenuItem onClick={() => onPin(header.column.id, "left")}>
										<ArrowLeftToLine className="h-4 w-4" />
										Pin to left
									</DropdownMenuItem>
								)}
								{pinned === "right" ? (
									<DropdownMenuItem onClick={() => onPin(header.column.id, "right")}>
										<PinOff className="h-4 w-4" />
										Unpin
									</DropdownMenuItem>
								) : (
									<DropdownMenuItem onClick={() => onPin(header.column.id, "right")}>
										<ArrowRightToLine className="h-4 w-4" />
										Pin to right
									</DropdownMenuItem>
								)}
							</DropdownMenuContent>
						</DropdownMenu>
					)}
				</div>
			)}
		</th>
	);
}
