import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { getProviderLabel } from "@/lib/constants/logs";
import { cn } from "@/lib/utils";

interface ProviderProps {
	provider: string;
	size?: number;
	className?: string;
}

export default function Provider({ provider, size = 16, className }: ProviderProps) {
	return (
			<div className="flex items-center gap-1">
			<RenderProviderIcon provider={provider as ProviderIconType} size={size} className={cn("mt-0.5", className)} />
			<span>{getProviderLabel(provider)}</span>
		</div>
	);
}