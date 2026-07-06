import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { ArrowUpRight, type LucideIcon } from "lucide-react";
import type { ReactNode } from "react";

interface EmptyStateViewProps {
	icon: LucideIcon;
	title: string;
	description: ReactNode;
	readmeLink?: string;
	readMoreAriaLabel?: string;
	readMoreTestId?: string;
	actions?: ReactNode;
	className?: string;
	testId?: string;
}

export function EmptyStateView({
	icon: Icon,
	title,
	description,
	readmeLink,
	readMoreAriaLabel,
	readMoreTestId,
	actions,
	className,
	testId,
}: EmptyStateViewProps) {
	return (
		<div
			className={cn("flex min-h-[calc(100dvh-3rem)] w-full flex-col items-center justify-center gap-4 text-center", className)}
			data-testid={testId}
		>
			<div className="text-muted-foreground">
				<Icon className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />
			</div>
			<div className="flex flex-col gap-1">
				<h1 className="text-muted-foreground text-xl font-medium">{title}</h1>
				<div className="text-muted-foreground mx-auto mt-2 max-w-[600px] text-sm font-normal">{description}</div>
				{(readmeLink || actions) && (
					<div className="mx-auto mt-6 flex flex-row flex-wrap items-center justify-center gap-2">
						{readmeLink && (
							<Button
								variant="outline"
								aria-label={readMoreAriaLabel ?? "Read more (opens in new tab)"}
								data-testid={readMoreTestId}
								onClick={() => {
									window.open(`${readmeLink}?utm_source=bfd`, "_blank", "noopener,noreferrer");
								}}
							>
								Read more <ArrowUpRight className="text-muted-foreground h-3 w-3" />
							</Button>
						)}
						{actions}
					</div>
				)}
			</div>
		</div>
	);
}
