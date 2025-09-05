import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { PROVIDER_LABELS } from "@/lib/constants/logs";

interface ProviderProps {
	provider: string;
	size?: number;
}

export default function Provider({ provider, size = 16 }: ProviderProps) {
	return (
		<>
			<RenderProviderIcon provider={provider as ProviderIconType} size={size} />
			<span>{PROVIDER_LABELS[provider as keyof typeof PROVIDER_LABELS]}</span>
		</>
	);
}
