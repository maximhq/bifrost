import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { getProviderLabel, ProviderLabels } from "@/lib/constants/logs";

interface ProviderProps {
	provider: string;
	size?: number;
}

export default function Provider({ provider, size = 16 }: ProviderProps) {
	return (
		<div className="flex items-center gap-1">
			<RenderProviderIcon provider={provider as ProviderIconType} size={size} className="mt-0.5" />
			<span>{getProviderLabel(provider)}</span>
		</div>
	);
}
