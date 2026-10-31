import { MonitorSmartphone } from "lucide-react";
import { useTranslation } from "react-i18next";

// OSS stub for the Edge OS icon - there are no platform icons in OSS, so
// always render the generic device glyph.
export function OsIcon({ className }: { platform?: string; className?: string }) {
	const { t } = useTranslation("config");
	return <MonitorSmartphone className={className} aria-label={t("enterprise.unknownOs")} />;
}