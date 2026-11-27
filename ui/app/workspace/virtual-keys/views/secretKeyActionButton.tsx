import { Button } from "@/components/ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Copy, KeyRound } from "lucide-react";
import { type ReactNode, useState } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";

export const compactIconButtonClass =
	"text-muted-foreground hover:text-foreground hover:bg-muted inline-flex size-6 shrink-0 cursor-pointer items-center justify-center rounded-sm transition-colors";

interface SecretKeyActionButtonProps {
	action: "copy" | "reveal";
	vkName: string;
	onConfirm: () => void;
	onCopyName: () => void;
	testId: string;
	children: ReactNode;
}

// Copying or revealing the secret asks first, so people reach for the name when they only mean to refer to the key.
export function SecretKeyActionButton({ action, vkName, onConfirm, onCopyName, testId, children }: SecretKeyActionButtonProps) {
	const { t } = useTranslation("models");
	const [open, setOpen] = useState(false);

	return (
		<Popover open={open} onOpenChange={setOpen}>
			{open &&
				createPortal(
					<div className="animate-in fade-in-0 fixed inset-0 z-50 bg-black/50" onClick={() => setOpen(false)} aria-hidden />,
					document.body,
				)}
			<PopoverTrigger asChild>
				<button
					type="button"
					className={compactIconButtonClass}
					aria-label={action === "copy" ? t("virtualKeys.secretKey.copyAria") : t("virtualKeys.secretKey.revealAria")}
					data-testid={testId}
				>
					{children}
				</button>
			</PopoverTrigger>
			<PopoverContent className="w-72 p-0" align="end" data-testid={`${testId}-confirm`}>
				<div className="flex flex-col gap-3 p-3">
					<div className="flex flex-col gap-1">
						<div className="flex items-center gap-2">
							<KeyRound className="text-muted-foreground size-3.5 shrink-0" />
							<p className="text-sm font-medium">{t("virtualKeys.secretKey.title")}</p>
						</div>
						<p className="text-muted-foreground text-xs">{t("virtualKeys.secretKey.warning")}</p>
					</div>
					<div className="flex flex-col gap-1.5">
						<p className="text-muted-foreground text-xs">{t("virtualKeys.secretKey.shareName")}</p>
						<div className="bg-muted/60 flex items-center gap-1 rounded-sm border py-0.5 pr-0.5 pl-2">
							<code className="min-w-0 flex-1 truncate font-mono text-xs" title={vkName}>
								{vkName}
							</code>
							<button
								type="button"
								className={compactIconButtonClass}
								onClick={() => {
									setOpen(false);
									onCopyName();
								}}
								aria-label={t("virtualKeys.copyNameAria", { name: vkName })}
								data-testid={`${testId}-copy-name`}
							>
								<Copy className="size-3.5" />
							</button>
						</div>
					</div>
				</div>
				<div className="flex justify-end gap-2 border-t px-3 py-2">
					<Button variant="ghost" size="sm" className="h-7 text-xs" onClick={() => setOpen(false)} data-testid={`${testId}-cancel`}>
						{t("virtualKeys.secretKey.cancel")}
					</Button>
					<Button
						variant="outline"
						size="sm"
						className="h-7 text-xs"
						onClick={() => {
							setOpen(false);
							onConfirm();
						}}
						data-testid={`${testId}-confirm-btn`}
					>
						{action === "copy" ? t("virtualKeys.secretKey.confirmCopy") : t("virtualKeys.secretKey.confirmReveal")}
					</Button>
				</div>
			</PopoverContent>
		</Popover>
	);
}