import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ScrollArea } from "@/components/ui/scrollArea";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Message, MessageType } from "@/lib/message";
import { Markdown } from "@/components/ui/markdown";
import { getErrorMessage } from "@/lib/store";
import { useCommitSessionMutation } from "@/lib/store/apis/promptsApi";
import { PromptSession, PromptSessionMessage } from "@/lib/types/prompts";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { cn } from "@/lib/utils";

interface CommitVersionFormData {
	commitMessage: string;
}

interface CommitVersionSheetProps {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	session: PromptSession;
	onCommitted: (versionId: number) => void;
}

function MessagePreview({
	sessionMessage,
	selected,
	onToggle,
}: {
	sessionMessage: PromptSessionMessage;
	selected: boolean;
	onToggle: () => void;
}) {
	const { t } = useTranslation();
	const msg = useMemo(() => Message.deserialize(sessionMessage.message), [sessionMessage.message]);
	const role = msg.role;
	const content = msg.content;
	const hasToolCalls = msg.type === MessageType.CompletionResult && msg.toolCalls && msg.toolCalls.length > 0;

	return (
		<label
			className={cn(
				"group flex items-start gap-3 rounded-md border px-3 py-2.5 cursor-pointer transition-colors",
				selected ? "border-border" : "border-transparent",
			)}
		>
			<Checkbox checked={selected} onCheckedChange={onToggle} className="mt-1 shrink-0" />
			<div className="min-w-0 flex-1">
				<span className="text-xs font-medium uppercase">{role}</span>
				<div className="text-muted-foreground mt-1 line-clamp-3 text-sm">
					{hasToolCalls && !content ? (
						<span className="italic">
							{t("workspace.promptRepository.sheets.toolCall", { names: msg.toolCalls!.map((tc) => tc.function.name).join(", ") })}
						</span>
					) : content ? (
						<Markdown content={content} className="text-muted-foreground [&_*]:text-sm" />
					) : (
						<span className="italic">{t("workspace.promptRepository.sheets.emptyMessage")}</span>
					)}
				</div>
			</div>
		</label>
	);
}

export function CommitVersionSheet({ open, onOpenChange, session, onCommitted }: CommitVersionSheetProps) {
	const { t } = useTranslation();
	const [commitSession, { isLoading }] = useCommitSessionMutation();
	const [selectedIndices, setSelectedIndices] = useState<Set<number>>(new Set());

	const {
		register,
		handleSubmit,
		reset,
		formState: { errors },
	} = useForm<CommitVersionFormData>({
		defaultValues: { commitMessage: "" },
	});

	// Reset form and select only the first message when sheet opens
	useEffect(() => {
		if (open) {
			reset({ commitMessage: "" });
			setSelectedIndices(new Set(session.messages.length > 0 ? [0] : []));
		}
	}, [open, reset, session?.messages?.length]);

	const toggleMessage = useCallback((index: number) => {
		setSelectedIndices((prev) => {
			const next = new Set(prev);
			if (next.has(index)) {
				next.delete(index);
			} else {
				next.add(index);
			}
			return next;
		});
	}, []);

	const allSelected = selectedIndices.size === session.messages.length;

	const toggleAll = useCallback(() => {
		if (allSelected) {
			setSelectedIndices(new Set());
		} else {
			setSelectedIndices(new Set(session.messages.map((_, i) => i)));
		}
	}, [allSelected, session.messages]);

	async function onSubmit(data: CommitVersionFormData) {
		if (selectedIndices.size === 0) {
			toast.error(t("workspace.promptRepository.sheets.commitSelectRequired"));
			return;
		}
		try {
			const sortedIndices = Array.from(selectedIndices).sort((a, b) => a - b);
			const commitData: { commit_message: string; message_indices?: number[] } = {
				commit_message: data.commitMessage.trim(),
			};
			// Only send message_indices if not all messages are selected
			if (!allSelected) {
				commitData.message_indices = sortedIndices;
			}
			const result = await commitSession({
				id: session.id,
				promptId: session.prompt_id,
				data: commitData,
			}).unwrap();
			toast.success(t("workspace.promptRepository.sheets.versionCommitted"));
			reset();
			onCommitted(result.version.id);
			onOpenChange(false);
		} catch (err) {
			toast.error(t("workspace.promptRepository.sheets.commitFailed"), {
				description: getErrorMessage(err),
			});
		}
	}

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent
				className="flex h-full flex-col p-8"
				onOpenAutoFocus={(e) => {
					e.preventDefault();
					document.getElementById("commitMessage")?.focus();
				}}
			>
				<form onSubmit={handleSubmit(onSubmit)} className="flex flex-1 flex-col overflow-hidden">
					<SheetHeader className="flex flex-col items-start">
						<SheetTitle>{t("workspace.promptRepository.sheets.commitAsVersion")}</SheetTitle>
						<SheetDescription>{t("workspace.promptRepository.sheets.commitDescription")}</SheetDescription>
					</SheetHeader>

					{/* Messages selection - scrollable */}
					<div className="mt-4 flex flex-1 flex-col overflow-hidden">
						<div className="mb-2 flex items-center justify-between">
							<Label className="text-sm">
								{t("workspace.promptRepository.sheets.messagesCount", { selected: selectedIndices.size, total: session.messages.length })}
							</Label>
							<button type="button" onClick={toggleAll} className="text-muted-foreground hover:text-foreground text-xs transition-colors">
								{allSelected ? t("workspace.promptRepository.sheets.deselectAll") : t("workspace.promptRepository.sheets.selectAll")}
							</button>
						</div>
						<ScrollArea className="flex-1 overflow-y-auto rounded-md border">
							<div className="space-y-1 p-2">
								{session.messages.map((sessionMsg, index) => (
									<MessagePreview
										key={sessionMsg.id}
										sessionMessage={sessionMsg}
										selected={selectedIndices.has(index)}
										onToggle={() => toggleMessage(index)}
									/>
								))}
							</div>
						</ScrollArea>
					</div>

					{/* Commit message + CTAs - always visible at bottom */}
					<div className="mt-4 shrink-0 space-y-4">
						<div className="space-y-2">
							<Label htmlFor="commitMessage">{t("workspace.promptRepository.sheets.commitMessage")}</Label>
							<Input
								id="commitMessage"
								data-testid="commit-version-message"
								placeholder={t("workspace.promptRepository.sheets.commitMessagePlaceholder")}
								{...register("commitMessage", {
									required: t("workspace.promptRepository.sheets.commitMessageRequired"),
									validate: (v) => v.trim().length > 0 || t("workspace.promptRepository.sheets.commitMessageBlank"),
								})}
								autoFocus
							/>
							{errors.commitMessage ? (
								<p className="text-destructive text-xs">{errors.commitMessage.message}</p>
							) : (
								<p className="text-muted-foreground text-xs">{t("workspace.promptRepository.sheets.commitMessageHint")}</p>
							)}
						</div>

						<SheetFooter className="flex flex-row items-center justify-end gap-2 p-0">
							<Button type="button" variant="outline" data-testid="commit-version-cancel" onClick={() => onOpenChange(false)}>
								{t("workspace.promptRepository.sheets.cancel")}
							</Button>
							<Button
								type="submit"
								data-testid="commit-version-submit"
								disabled={isLoading || selectedIndices.size === 0}
								className={selectedIndices.size === 0 ? "opacity-50" : ""}
							>
								{isLoading ? t("workspace.promptRepository.sheets.committing") : t("workspace.promptRepository.sheets.commitVersion")}
							</Button>
						</SheetFooter>
					</div>
				</form>
			</SheetContent>
		</Sheet>
	);
}