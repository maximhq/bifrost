import { MonitorSmartphone } from "lucide-react";

// OSS stub for the Edge OS icon - there are no platform icons in OSS, so
// always render the generic device glyph.
export function OsIcon({ className }: { platform?: string; className?: string }) {
	return <MonitorSmartphone className={className} aria-label="Unknown OS" />;
}