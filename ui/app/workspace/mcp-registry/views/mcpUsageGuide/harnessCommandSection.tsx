import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import { Check, Copy, Terminal } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { HarnessCommandSectionProps } from "./types";

export function HarnessCommandSection({
	canCopyCommand,
	command,
	controls,
	copySuccessMessage,
	deeplink,
	deeplinkLabel,
	emptyMessage,
	harnessName,
	label,
	logoSrc,
	registrationLabel,
}: HarnessCommandSectionProps) {
	const { t } = useTranslation("mcp");
	const resolvedLabel = label ?? t("common.command");
	const resolvedDeeplinkLabel = deeplinkLabel ?? t("common.install");
	const resolvedCopySuccess = copySuccessMessage ?? t("common.commandCopied");
	const { copy, copied } = useCopyToClipboard({ successMessage: resolvedCopySuccess });
	const copyLabel = resolvedLabel.toLowerCase();
	const canUseDeeplink = canCopyCommand && !!deeplink;

	return (
		<section className="flex flex-col gap-2">
			{/* ── Header row: label + action buttons ─────────────────── */}
			<div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
				<div className="flex items-center gap-2 text-sm font-medium">
					<span>{resolvedLabel}</span>
				</div>

				<div className="flex items-center gap-2">
					{controls}

					{/* Deeplink button (optional) */}
					{deeplink !== undefined && (
						<Tooltip>
							<TooltipTrigger asChild>
								{canUseDeeplink ? (
									<Button type="button" variant="secondary" size="sm" asChild data-testid="mcp-usage-guide-deeplink">
										<a href={deeplink} aria-label={resolvedDeeplinkLabel}>
											{logoSrc && <img src={logoSrc} alt="" aria-hidden="true" className="size-4 rounded-[2px]" />}
											<span>{resolvedDeeplinkLabel}</span>
										</a>
									</Button>
								) : (
									<Button
										type="button"
										variant="secondary"
										size="sm"
										disabled
										aria-label={resolvedDeeplinkLabel}
										data-testid="mcp-usage-guide-deeplink"
									>
										{logoSrc && <img src={logoSrc} alt="" aria-hidden="true" className="size-4 rounded-[2px]" />}
										<span>{resolvedDeeplinkLabel}</span>
									</Button>
								)}
							</TooltipTrigger>
							<TooltipContent>{canUseDeeplink ? resolvedDeeplinkLabel : t("usageGuide.finishSelections")}</TooltipContent>
						</Tooltip>
					)}

					{/* Copy button */}
					<Tooltip>
						<TooltipTrigger asChild>
							<Button
								type="button"
								variant={copied ? "secondary" : "default"}
								size="sm"
								disabled={!canCopyCommand}
								onClick={() => void copy(command)}
								aria-label={copied ? t("usageGuide.copiedLabel", { label: resolvedLabel }) : t("usageGuide.copyLabel", { label: copyLabel })}
								data-testid="mcp-usage-guide-copy-command"
							>
								{copied ? <Check className="size-4 text-green-600" /> : <Copy className="size-4" />}
								<span>{copied ? t("common.copied") : t("common.copy")}</span>
							</Button>
						</TooltipTrigger>
						<TooltipContent>{canCopyCommand ? (copied ? t("common.copied") : t("usageGuide.copyLabel", { label: copyLabel })) : t("usageGuide.finishSelections")}</TooltipContent>
					</Tooltip>
				</div>
			</div>

			{/* ── Code block ─────────────────────────────────────────── */}
			<div className="overflow-hidden rounded-sm border bg-[#111827] text-slate-100">
				<div className="flex flex-wrap items-center justify-between gap-2 border-b border-white/10 px-3 py-2">
					<div className="flex items-center gap-2">
						{logoSrc ? (
							<img src={logoSrc} alt="" aria-hidden="true" className="size-4 rounded-[2px]" />
						) : (
							<Terminal className="size-4 text-slate-400" />
						)}
						<span className="text-xs font-medium text-slate-300">{harnessName}</span>
					</div>
					<span className="font-mono text-[11px] text-slate-500">{registrationLabel}</span>
				</div>

				{canCopyCommand ? (
					<pre className="overflow-x-auto p-4 text-xs leading-5">
						<code>{command}</code>
					</pre>
				) : (
					<div className="p-4 text-sm text-slate-400">{emptyMessage}</div>
				)}
			</div>
		</section>
	);
}