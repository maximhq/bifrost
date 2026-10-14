import { rssFeedIconSource } from "@/components/rssFeedIcon.utils";
import { RenderProviderIcon } from "@/lib/constants/icons";
import { cn } from "@/lib/utils";
import { Rss } from "lucide-react";
import { useTheme } from "next-themes";
import { useState } from "react";

interface RSSFeedIconProps {
	icon?: string;
	iconUrl?: string;
	feedUrl: string;
	name: string;
	size?: number;
	className?: string;
}

/** The source icon shown beside a feed in the news tab and the Sources sheet. */
export default function RSSFeedIcon({ icon, iconUrl, feedUrl, name, size = 16, className }: RSSFeedIconProps) {
	const { resolvedTheme } = useTheme();
	const source = rssFeedIconSource(icon, feedUrl, iconUrl);
	// Keyed by URL so a logo that changes after a failed load gets another try.
	const [failedUrl, setFailedUrl] = useState<string>();
	const box = cn("flex shrink-0 items-center justify-center overflow-hidden", className);
	// object-contain: the Bifrost mark and most favicons/logos are not square,
	// and forcing them into a square box distorts them.
	const imgClass = "max-h-full max-w-full object-contain";

	if (source.kind === "bifrost") {
		return (
			<span className={box} style={{ width: size, height: size }}>
				<img alt={name} src={`/bifrost-icon${resolvedTheme === "dark" ? "-dark" : ""}.webp`} className={imgClass} />
			</span>
		);
	}
	if (source.kind === "provider") {
		return (
			<span className={box} style={{ width: size, height: size }}>
				<RenderProviderIcon provider={source.provider} size={size} className="size-full" />
			</span>
		);
	}
	if (source.kind === "image" && failedUrl !== source.url) {
		return (
			<span className={box} style={{ width: size, height: size }}>
				<img
					alt={name}
					src={source.url}
					className={imgClass}
					referrerPolicy="no-referrer"
					loading="lazy"
					onError={() => setFailedUrl(source.url)}
				/>
			</span>
		);
	}
	return (
		<span className={cn(box, "text-orange-500")} style={{ width: size, height: size }}>
			<Rss style={{ width: size * 0.85, height: size * 0.85 }} />
		</span>
	);
}