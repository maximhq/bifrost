"use client";

import { useState, useEffect } from "react";
import { Button } from "@/components/ui/button";
import {
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from "@/components/ui/dialog";
import { cn } from "@/lib/utils";
import type { ConnectorOption } from "../views/connectorsEmptyState";

export interface AddConnectorDialogProps {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	/** Connector types that can be added */
	availableToAdd: ConnectorOption[];
	/** Called when user confirms adding the selected connector */
	onAdd: (connectorId: string) => void | Promise<void>;
	/** Whether add is in progress */
	isAdding?: boolean;
}

export function AddConnectorDialog({
	open,
	onOpenChange,
	availableToAdd,
	onAdd,
	isAdding = false,
}: AddConnectorDialogProps) {
	const [selectedId, setSelectedId] = useState<string | null>(null);

	const handleClose = () => {
		setSelectedId(null);
		onOpenChange(false);
	};

	const handleAdd = async () => {
		if (!selectedId) return;
		try {
			await onAdd(selectedId);
			handleClose();
		} catch {
			// Error already surfaced (toasted) by onAdd; keep dialog open for retry.
		}
	};

	// Reset selection when dialog opens
	useEffect(() => {
		if (open) setSelectedId(null);
	}, [open]);

	const empty = availableToAdd.length === 0;

	return (
		<Dialog open={open} onOpenChange={(o) => !o && handleClose()}>
			<DialogContent
				className="sm:max-w-lg"
				data-testid="add-connector-dialog"
			>
				<DialogHeader>
					<DialogTitle>Add a new connector</DialogTitle>
					<DialogDescription>
						Select an observability connector to add. You can configure it after adding.
					</DialogDescription>
				</DialogHeader>
				<div className="custom-scrollbar max-h-[60vh] space-y-2 overflow-y-auto py-2">
					{empty ? (
						<p className="text-muted-foreground text-sm">All connector types are already added.</p>
					) : (
						<ul className="flex flex-col gap-2">
							{availableToAdd.map((platform) => (
								<li key={platform.id}>
									<button
										type="button"
										onClick={() => setSelectedId(platform.id)}
										data-testid={`add-connector-option-${platform.id}`}
										className={cn(
											"flex w-full items-center gap-3 rounded-lg border bg-card px-4 py-3 text-left transition-colors",
											selectedId === platform.id
												? "border-primary ring-1 ring-primary"
												: "hover:bg-muted/50",
										)}
									>
										<div className="flex shrink-0 items-center [&>svg]:size-5 [&>img]:size-5">
											{platform.icon}
										</div>
										<span className="text-sm font-medium">{platform.name}</span>
									</button>
								</li>
							))}
						</ul>
					)}
				</div>
				<DialogFooter className="flex flex-row gap-2 pt-2">
					<Button type="button" variant="outline" onClick={handleClose} data-testid="add-connector-cancel-btn">
						Cancel
					</Button>
					<Button
						type="button"
						disabled={empty || !selectedId || isAdding}
						onClick={handleAdd}
						data-testid="add-connector-save-btn"
					>
						{isAdding ? "Adding…" : "Add connector"}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
