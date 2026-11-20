import { AlertCircle, ChevronDown } from "lucide-react";
import { useTranslation } from "react-i18next";

import { CopyInlineButton } from "./logHeroStat";

interface LogErrorCardProps {
	/** The line shown under the heading, with any fallback text already applied by the caller. */
	message: string;
	/** A JSON value from inside the message, already pretty-printed, shown as a block under it. */
	json?: string | null;
	/** Message text that came after the JSON block. */
	messageAfter?: string | null;
	/** Text the copy button copies; the button is hidden when there is nothing worth copying. */
	copyText?: string | null;
	/** Extra detail behind a collapsed Details toggle, shown only when it adds to the message. */
	details?: string | null;
	testId?: string;
}

const messageClass = "mt-2 text-[13px] leading-relaxed break-words whitespace-pre-wrap text-red-700 dark:text-red-400";

/** The red error card shared by the LLM and MCP log sheets. */
export function LogErrorCard({ message, json, messageAfter, copyText, details, testId }: LogErrorCardProps) {
	const { t } = useTranslation("observability");
	return (
		<div className="rounded-sm border border-red-200 bg-red-50/70 p-5 dark:border-red-900 dark:bg-red-950/30" data-testid={testId}>
			<div className="flex items-center gap-2 text-red-700 dark:text-red-400">
				<AlertCircle className="h-4 w-4 shrink-0" />
				<span className="text-[12.5px] font-semibold">{t("labels.error")}</span>
				{copyText ? <CopyInlineButton text={copyText} /> : null}
			</div>
			{message ? <div className={messageClass}>{message}</div> : null}
			{json ? (
				<pre
					className="custom-scrollbar mt-2 max-h-[400px] overflow-y-auto rounded-sm border border-red-200/70 bg-white/40 px-3 py-2 font-mono text-[11.5px] leading-[1.6] break-words whitespace-pre-wrap text-red-900 dark:border-red-900/70 dark:bg-red-950/40 dark:text-red-300"
					data-testid={testId ? `${testId}-json` : undefined}
				>
					{json}
				</pre>
			) : null}
			{messageAfter ? <div className={messageClass}>{messageAfter}</div> : null}
			{details ? (
				<details
					className="group mt-3 rounded-sm border border-red-200/70 bg-white/40 dark:border-red-900/70 dark:bg-red-950/40"
					data-testid={testId ? `${testId}-details` : undefined}
				>
					<summary className="flex cursor-pointer items-center justify-between px-3 py-2 text-[12px] text-red-700 hover:bg-red-50/80 dark:text-red-400 dark:hover:bg-red-950/60">
						<span className="font-medium">{t("logs.detail.details")}</span>
						<ChevronDown className="h-3.5 w-3.5 transition-transform group-open:rotate-180" />
					</summary>
					<div className="custom-scrollbar max-h-[400px] overflow-y-auto border-t border-red-200/70 px-3 py-2 font-mono text-[11.5px] leading-[1.6] break-words whitespace-pre-wrap text-red-900 dark:border-red-900/70 dark:text-red-300">
						{details}
					</div>
				</details>
			) : null}
		</div>
	);
}