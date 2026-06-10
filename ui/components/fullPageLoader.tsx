import { cn } from "@/lib/utils";
import { Loader2 } from "lucide-react";

function FullPageLoader({ className }: { className?: string }) {
	return (
		<div className={cn("h-base pb-1/2 flex items-center justify-center", className)}>
			<Loader2 className="h-4 w-4 animate-spin" />
		</div>
	);
}

export default FullPageLoader;