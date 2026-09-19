import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import type { ShortcutKey } from "@/hooks/useSheetNavigation";
import { ChevronDown, ChevronUp } from "lucide-react";
import React from "react";
import { useTranslation } from "react-i18next";

const kbdClass =
	"inline-flex items-center justify-center size-4 rounded border border-border/60 bg-muted/80 text-[10px] leading-none text-muted-foreground shadow-[0_1px_0_0.5px] shadow-border/40";

interface SheetNavigationButtonsProps {
	hasPrev: boolean;
	hasNext: boolean;
	onNavigate: (direction: "prev" | "next") => void;
	prevKeys?: ShortcutKey[];
	nextKeys?: ShortcutKey[];
	entityLabel?: string;
}

const ENTITY_NOUN_KEYS: Record<string, string> = {
	item: "entities.item",
	log: "entities.log",
	"virtual key": "entities.virtualKey",
	server: "entities.server",
	"Virtual MCP": "entities.virtualMcp",
	rule: "entities.rule",
};

function ShortcutKeys({ keys }: { keys: ShortcutKey[] }) {
	const { t } = useTranslation("common");
	return (
		<span className="inline-flex items-center gap-1">
			{keys.map((k, i) => (
				<React.Fragment key={i}>
					{i > 0 && t("or")}
					<kbd className={kbdClass}>{k.icon ? <k.icon className="size-2.5" /> : k.label}</kbd>
				</React.Fragment>
			))}
		</span>
	);
}

export function SheetNavigationButtons({
	hasPrev,
	hasNext,
	onNavigate,
	prevKeys,
	nextKeys,
	entityLabel = "item",
}: SheetNavigationButtonsProps) {
	const { t } = useTranslation("common");
	const nounKey = ENTITY_NOUN_KEYS[entityLabel];
	const entity = nounKey ? t(nounKey) : entityLabel;
	return (
		<div className="flex items-center">
			<Tooltip delayDuration={0}>
				<TooltipTrigger asChild>
					<Button
						variant="ghost"
						className="size-8"
						disabled={!hasPrev}
						onClick={() => onNavigate("prev")}
						aria-label={t("previousEntity", { entity })}
						type="button"
					>
						<ChevronUp className="size-4" />
					</Button>
				</TooltipTrigger>
				<TooltipContent className="flex items-center gap-1.5 px-2 py-1 text-xs">
					{t("prev")} {prevKeys && <ShortcutKeys keys={prevKeys} />}
				</TooltipContent>
			</Tooltip>
			<Tooltip delayDuration={0}>
				<TooltipTrigger asChild>
					<Button
						variant="ghost"
						className="size-8"
						disabled={!hasNext}
						onClick={() => onNavigate("next")}
						aria-label={t("nextEntity", { entity })}
						type="button"
					>
						<ChevronDown className="size-4" />
					</Button>
				</TooltipTrigger>
				<TooltipContent className="flex items-center gap-1.5 px-2 py-1 text-xs">
					{t("next")} {nextKeys && <ShortcutKeys keys={nextKeys} />}
				</TooltipContent>
			</Tooltip>
		</div>
	);
}
