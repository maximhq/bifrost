import { cn } from "@/lib/utils";

function GradientHeader({ title, className }: { title: string; className?: string }) {
	return (
		<div className={cn("from-primary bg-gradient-to-r to-green-600 bg-clip-text pb-2 text-5xl font-bold text-transparent", className)}>
			{title}
		</div>
	);
}

export default GradientHeader;
