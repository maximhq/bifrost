import { useCallback, useState } from "react";

/**
 * Defers a save until the operator has said whether to clear accumulated budget
 * spend.
 *
 * The save payload is parked rather than recomputed after the choice, so the
 * form cannot change underneath the dialog and produce a submission the operator
 * never saw. Generic over that payload so each owner's sheet keeps its own shape.
 *
 * Usage:
 *   const resetPrompt = useBudgetUsageResetPrompt<FormData>();
 *   ...
 *   if (budgetsChanged) return resetPrompt.ask(data);
 *   await save(data, false);
 *   ...
 *   <BudgetUsageResetDialog
 *     open={resetPrompt.isOpen}
 *     onOpenChange={resetPrompt.setOpen}
 *     onChoice={(resetUsage) => resetPrompt.resolve((payload) => save(payload, resetUsage))}
 *   />
 */
export function useBudgetUsageResetPrompt<T>() {
	const [isOpen, setOpen] = useState(false);
	const [pending, setPending] = useState<T | null>(null);

	// Park the payload and open the dialog. Returns nothing so a caller can
	// `return resetPrompt.ask(data)` straight out of a submit handler.
	const ask = useCallback((payload: T) => {
		setPending(payload);
		setOpen(true);
	}, []);

	// Hand the parked payload to the caller's save and clear the prompt. Clearing
	// before awaiting keeps a slow save from leaving the dialog open and
	// re-submittable.
	const resolve = useCallback(
		(save: (payload: T) => void | Promise<void>) => {
			if (pending === null) return;
			const payload = pending;
			setPending(null);
			setOpen(false);
			void save(payload);
		},
		[pending],
	);

	return { isOpen, setOpen, ask, resolve };
}