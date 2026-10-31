import { useCallback, useRef, useState } from "react";
import { toast } from "sonner";
import i18n from "@/lib/i18n";

interface UseCopyToClipboardOptions {
	successMessage?: string;
	errorMessage?: string;
	resetDelay?: number;
}

export function useCopyToClipboard(options: UseCopyToClipboardOptions = {}) {
	const { successMessage, errorMessage, resetDelay = 2000 } = options;
	const [copied, setCopied] = useState(false);
	const timeoutRef = useRef<ReturnType<typeof setTimeout>>(undefined);

	const copy = useCallback(
		async (text: string) => {
			try {
				await navigator.clipboard.writeText(text);
				setCopied(true);
				toast.success(successMessage ?? i18n.t("copyToClipboard.success", { ns: "common" }));

				if (timeoutRef.current) clearTimeout(timeoutRef.current);
				timeoutRef.current = setTimeout(() => setCopied(false), resetDelay);
			} catch {
				toast.error(errorMessage ?? i18n.t("copyToClipboard.failed", { ns: "common" }));
			}
		},
		[successMessage, errorMessage, resetDelay],
	);

	return { copy, copied };
}