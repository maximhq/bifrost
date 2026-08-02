import { cn } from "@/lib/utils";
import { Check } from "lucide-react";
import type { HarnessPlatform } from "./types";

interface PlatformSelectProps {
	platform: HarnessPlatform;
	onPlatformChange: (platform: HarnessPlatform) => void;
}

export function PlatformSelect({ platform, onPlatformChange }: PlatformSelectProps) {
	const platforms: Array<{ value: HarnessPlatform; label: string; icon: string }> = [
		{ value: "macos", label: "macOS", icon: "/images/platforms/mac.svg" },
		{ value: "windows", label: "Windows", icon: "/images/platforms/windows.svg" },
		{ value: "linux", label: "Linux", icon: "/images/platforms/linux.svg" },
	];

	return (
		<div className="grid gap-2 sm:grid-cols-3" data-testid="mcp-usage-guide-platform">
			{platforms.map((option) => (
				<button
					key={option.value}
					type="button"
					onClick={() => onPlatformChange(option.value)}
					className={cn(
						"flex h-9 items-center gap-2 rounded-sm border px-3 py-2 text-left text-sm transition-[background-color,border-color,transform] duration-150 ease-out hover:bg-accent active:scale-[0.99]",
						platform === option.value && "border-primary bg-primary/5",
					)}
					aria-pressed={platform === option.value}
					data-testid={`mcp-usage-guide-platform-${option.value}`}
				>
					<img src={option.icon} alt="" aria-hidden="true" className="size-4 shrink-0" />
					<span className="font-medium">{option.label}</span>
					{platform === option.value && <Check className="ml-auto size-4 text-green-600" />}
				</button>
			))}
		</div>
	);
}