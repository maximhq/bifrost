import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import { Check, Copy, Terminal } from "lucide-react";
import type { HarnessCommandSectionProps } from "./types";

export function HarnessCommandSection({
	canCopyCommand,
	command,
	controls,
	copySuccessMessage = "Command copied",
	deeplink,
	deeplinkLabel = "Install",
	emptyMessage,
	harnessName,
	label = "Command",
	logoSrc,
	registrationLabel,
}: HarnessCommandSectionProps) {
	const { copy, copied } = useCopyToClipboard({ successMessage: copySuccessMessage });
	const copyLabel = label.toLowerCase();
	const canUseDeeplink = canCopyCommand && !!deeplink;

	return (
		<section className="flex flex-col gap-2">
			{/* ── Header row: label + action buttons ─────────────────── */}
			<div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
				<div className="flex items-center gap-2 text-sm font-medium">
					<span>{label}</span>
				</div>

				<div className="flex items-center gap-2">
					{controls}

					{/* Deeplink button (optional) */}
					{deeplink !== undefined && (
						<Tooltip>
							<TooltipTrigger asChild>
								{canUseDeeplink ? (
									<Button type="button" variant="secondary" size="sm" asChild data-testid="mcp-usage-guide-deeplink">
										<a href={deeplink} aria-label={deeplinkLabel}>
											{logoSrc && <img src={logoSrc} alt="" aria-hidden="true" className="size-4 rounded-[2px]" />}
											<span>{deeplinkLabel}</span>
										</a>
									</Button>
								) : (
									<Button
										type="button"
										variant="secondary"
										size="sm"
										disabled
										aria-label={deeplinkLabel}
										data-testid="mcp-usage-guide-deeplink"
									>
										{logoSrc && <img src={logoSrc} alt="" aria-hidden="true" className="size-4 rounded-[2px]" />}
										<span>{deeplinkLabel}</span>
									</Button>
								)}
							</TooltipTrigger>
							<TooltipContent>{canUseDeeplink ? deeplinkLabel : "Finish the selections first"}</TooltipContent>
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
								aria-label={copied ? `${label} copied` : `Copy ${copyLabel}`}
								data-testid="mcp-usage-guide-copy-command"
							>
								{copied ? <Check className="size-4 text-green-600" /> : <Copy className="size-4" />}
								<span>{copied ? "Copied" : "Copy"}</span>
							</Button>
						</TooltipTrigger>
						<TooltipContent>{canCopyCommand ? (copied ? "Copied" : `Copy ${copyLabel}`) : "Finish the selections first"}</TooltipContent>
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