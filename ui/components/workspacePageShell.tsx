import { cn } from "@/lib/utils";
import type { ReactNode } from "react";

interface WorkspacePageShellProps {
	children: ReactNode;
	className?: string;
}

export function WorkspacePageShell({ children, className }: WorkspacePageShellProps) {
	return (
		<div className={cn("no-padding-parent mx-auto flex h-[calc(100dvh-1rem)] min-h-0 w-full flex-col p-4", className)}>
			<div className="flex min-h-0 w-full grow flex-col overflow-y-auto">{children}</div>
		</div>
	);
}
