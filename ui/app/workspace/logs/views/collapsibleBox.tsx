import { Button } from "@/components/ui/button";
import { ChevronDown, ChevronUp, Copy } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { toast } from "sonner";

interface CollapsibleBoxProps {
	title: string;
	children: React.ReactNode;
	collapsedHeight?: number;
	expandedMaxHeight?: number;
	onCopy?: () => string;
}

export default function CollapsibleBox({ title, children, collapsedHeight = 60, expandedMaxHeight = 450, onCopy }: CollapsibleBoxProps) {
	const [isExpanded, setIsExpanded] = useState(false);
	const [needsExpansion, setNeedsExpansion] = useState(false);
	const innerContentRef = useRef<HTMLDivElement>(null);

	useEffect(() => {
		if (!innerContentRef.current) return;

		const checkHeight = () => {
			const scrollHeight = innerContentRef.current?.scrollHeight || 0;
			setNeedsExpansion(scrollHeight > collapsedHeight);
		};

		// Initial check after a small delay to allow content to render
		const timeoutId = setTimeout(checkHeight, 50);

		// Observe for resize changes
		const observer = new ResizeObserver(checkHeight);
		observer.observe(innerContentRef.current);

		return () => {
			clearTimeout(timeoutId);
			observer.disconnect();
		};
	}, [children, collapsedHeight]);

	const handleCopy = () => {
		if (!onCopy) return;

		navigator.clipboard
			.writeText(onCopy())
			.then(() => {
				toast.success("Copied to clipboard");
			})
			.catch(() => {
				toast.error("Failed to copy");
			});
	};

	return (
		<div className="w-full rounded-sm border">
			<div className="flex items-center justify-between border-b py-2 pl-6">
				<div className="text-sm font-medium">{title}</div>
				{onCopy && (
					<Button
						variant="ghost"
						size="sm"
						className="text-muted-foreground mx-2 h-6 py-1 hover:bg-transparent hover:text-black dark:hover:text-white"
						onClick={handleCopy}
					>
						<Copy className="h-3 w-3" />
					</Button>
				)}
			</div>
			<div
				className="overflow-hidden transition-all duration-200"
				style={{ maxHeight: isExpanded ? `${expandedMaxHeight}px` : `${collapsedHeight}px` }}
			>
				<div ref={innerContentRef}>{children}</div>
			</div>
			{needsExpansion && (
				<button
					onClick={() => setIsExpanded(!isExpanded)}
					className="bg-muted/50 text-muted-foreground hover:bg-muted flex w-full items-center justify-center gap-1 border-t py-1 text-xs"
				>
					{isExpanded ? (
						<>
							<ChevronUp className="h-3 w-3" />
							show less
						</>
					) : (
						<>
							<ChevronDown className="h-3 w-3" />
							show more
						</>
					)}
				</button>
			)}
		</div>
	);
}
