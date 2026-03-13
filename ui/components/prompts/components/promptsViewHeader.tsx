import { Button } from "@/components/ui/button";
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@/components/ui/command";
import { SplitButton } from "@/components/ui/splitButton";
import { DropdownMenuItem, DropdownMenuLabel, DropdownMenuSeparator } from "@/components/ui/dropdownMenu";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Check, ChevronDown, GitCommit, PencilIcon, Save, Trash2 } from "lucide-react";
import { useCallback, useRef, useState } from "react";
import { useHotkeys } from "react-hotkeys-hook";
import { parseAsInteger, useQueryStates } from "nuqs";
import { useCreateSessionMutation, useGetSessionsQuery, useGetVersionsQuery, useRenameSessionMutation } from "@/lib/store/apis/promptsApi";
import { Message, MessageRole } from "@/lib/message";
import { toast } from "sonner";
import { getErrorMessage } from "@/lib/store";
import { usePromptContext } from "../context";
import { ModelParams, PromptSession } from "@/lib/types/prompts";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";

export default function PromptsViewHeader() {
	const {
		selectedPrompt,
		messages,
		setMessages: onMessagesChange,
		setCommitSheet,
		apiKeyId,
		modelParams,
		provider,
		model,
		hasChanges,
		isStreaming,
	} = usePromptContext();

	const [sessionsOpen, setSessionsOpen] = useState(false);

	const onSessionSaved = useCallback(
		(session: PromptSession) => {
			setCommitSheet({ open: true, session });
		},
		[setCommitSheet],
	);
	// UI state — persisted in URL query params
	const [{ sessionId: selectedSessionId, versionId: selectedVersionId }, setUrlState] = useQueryStates(
		{
			sessionId: parseAsInteger,
			versionId: parseAsInteger,
		},
		{ history: "replace" },
	);

	// Fetch versions and sessions for selected prompt
	const { data: versionsData } = useGetVersionsQuery(selectedPrompt?.id ?? "", { skip: !selectedPrompt?.id });
	const { data: sessionsData } = useGetSessionsQuery(selectedPrompt?.id ?? "", { skip: !selectedPrompt?.id });

	// Mutations
	const [createSession, { isLoading: isCreatingSession }] = useCreateSessionMutation();
	const [renameSession] = useRenameSessionMutation();

	const versions = versionsData?.versions ?? [];
	const sessions = sessionsData?.sessions ?? [];

	const handleSelectVersion = useCallback(
		(versionId: number) => {
			setUrlState({ versionId, sessionId: null });
		},
		[setUrlState],
	);

	// Build model_params with api_key_id for persistence
	const buildSaveParams = useCallback((): ModelParams => {
		const params = { ...modelParams };
		if (apiKeyId && apiKeyId !== "__auto__") {
			params.api_key_id = apiKeyId;
		}
		return params;
	}, [modelParams, apiKeyId]);

	const handleSaveSession = useCallback(async () => {
		if (!selectedPrompt || !hasChanges || isStreaming) return;
		try {
			const result = await createSession({
				promptId: selectedPrompt.id,
				data: {
					messages: Message.serializeAll(messages),
					model_params: buildSaveParams(),
					provider,
					model,
				},
			}).unwrap();
			setUrlState({ sessionId: result.session.id, versionId: null });
			toast.success("Session saved");
		} catch (err) {
			toast.error("Failed to save session", { description: getErrorMessage(err) });
		}
	}, [selectedPrompt?.id, messages, buildSaveParams, provider, model, createSession, setUrlState, hasChanges, isStreaming]);

	// Cmd+S / Ctrl+S to save session
	useHotkeys(
		"mod+s",
		() => handleSaveSession(),
		{
			preventDefault: true,
			enableOnFormTags: ["input", "textarea", "select"],
			enabled: !!selectedPrompt && !isCreatingSession && !isStreaming,
		},
		[handleSaveSession, selectedPrompt, isCreatingSession, isStreaming],
	);

	const handleCommitVersion = useCallback(async () => {
		if (!selectedPrompt) return;
		try {
			// Always create a new session with current state before committing
			const result = await createSession({
				promptId: selectedPrompt.id,
				data: {
					messages: Message.serializeAll(messages),
					model_params: buildSaveParams(),
					provider,
					model,
				},
			}).unwrap();
			setUrlState({ sessionId: result.session.id, versionId: null });
			onSessionSaved(result.session);
		} catch (err) {
			toast.error("Failed to save session", { description: getErrorMessage(err) });
		}
	}, [selectedPrompt?.id, messages, buildSaveParams, provider, model, createSession, setUrlState, onSessionSaved]);

	const handleRenameSession = useCallback(
		async (sessionId: number, name: string) => {
			if (!selectedPrompt) return;
			try {
				await renameSession({ id: sessionId, promptId: selectedPrompt.id, data: { name } }).unwrap();
			} catch (err) {
				toast.error("Failed to rename session", { description: getErrorMessage(err) });
			}
		},
		[selectedPrompt?.id, renameSession],
	);

	const handleClearConversation = useCallback(() => {
		const firstMsg = messages[0];
		if (firstMsg?.role === MessageRole.SYSTEM) {
			onMessagesChange([firstMsg]);
		} else {
			onMessagesChange([Message.system("")]);
		}
	}, [messages]);

	return (
		<div className="flex items-center justify-between border-b px-4 py-3">
			<h3 className="truncate font-semibold">{selectedPrompt?.name || "Playground"}</h3>
			<div className="flex shrink-0 items-center gap-4">
				{messages.length > 1 && (
					<Button variant="ghost" size="sm" onClick={handleClearConversation} disabled={isStreaming}>
						<Trash2 className="h-4 w-4" />
						Clear
					</Button>
				)}
				<div className="inline-flex items-center">
					<Button
						variant="outline"
						className="h-8 rounded-r-none border bg-transparent"
						onClick={handleSaveSession}
						disabled={isCreatingSession || !hasChanges || isStreaming}
					>
						<Save className="h-4 w-4" />
						Save Session
					</Button>
					<Popover open={sessionsOpen} onOpenChange={setSessionsOpen}>
						<PopoverTrigger asChild>
							<Button
								variant="outline"
								className={cn(
									"h-8 w-8 rounded-l-none border border-l-0 bg-transparent p-0",
									isCreatingSession || !hasChanges || isStreaming ? "border-border/50" : "",
								)}
							>
								<ChevronDown className="h-4 w-4" />
							</Button>
						</PopoverTrigger>
						<PopoverContent className="w-72 p-0" align="end">
							<Command>
								<CommandInput placeholder="Search sessions..." />
								<CommandList>
									<CommandEmpty>No sessions found.</CommandEmpty>
									<CommandGroup>
										{sessions.map((session) => (
											<SessionItem
												key={session.id}
												session={session}
												isSelected={selectedSessionId === session.id}
												onSelect={() => {
													setUrlState({ sessionId: session.id, versionId: null });
													setSessionsOpen(false);
												}}
												onRename={(name) => handleRenameSession(session.id, name)}
											/>
										))}
									</CommandGroup>
								</CommandList>
							</Command>
						</PopoverContent>
					</Popover>
				</div>
				<SplitButton
					onClick={handleCommitVersion}
					disabled={isCreatingSession || isStreaming}
					dropdownContent={{
						className: "w-64 max-h-72 overflow-y-auto",
						children: (
							<>
								<DropdownMenuLabel>Versions</DropdownMenuLabel>
								<DropdownMenuSeparator />
								{versions.length === 0 ? (
									<div className="text-muted-foreground px-2 py-3 text-center text-sm">No versions yet</div>
								) : (
									versions.map((version) => (
										<DropdownMenuItem
											key={version.id}
											onClick={() => handleSelectVersion(version.id)}
											className="flex items-center justify-between gap-2"
										>
											<div className="flex min-w-0 flex-col">
												<span className="truncate text-sm">
													v{version.version_number}
													{version.is_latest && <span className="text-primary ml-1.5 text-xs">(latest)</span>}
												</span>
												<span className="text-muted-foreground truncate text-xs">{version.commit_message || "No commit message"}</span>
												<span className="text-muted-foreground text-xs">{new Date(version.created_at).toLocaleString()}</span>
											</div>
											{selectedVersionId === version.id && <Check className="text-primary h-4 w-4 shrink-0" />}
										</DropdownMenuItem>
									))
								)}
							</>
						),
					}}
					variant={"outline"}
					dropdownTrigger={{
						className: "bg-transparent",
					}}
					button={{
						className: "bg-transparent",
					}}
				>
					<GitCommit className="h-4 w-4" />
					Commit Version
				</SplitButton>
			</div>
		</div>
	);
}

function formatSessionDate(dateStr: string): string {
	const date = new Date(dateStr);
	const month = date.toLocaleString("en-US", { month: "short" });
	const day = date.getDate();
	const hours = date.getHours();
	const minutes = date.getMinutes().toString().padStart(2, "0");
	const ampm = hours >= 12 ? "pm" : "am";
	const displayHours = (hours % 12 || 12).toString().padStart(2, "0");
	return `${month} ${day}, ${displayHours}:${minutes}${ampm}`;
}

function SessionItem({
	session,
	isSelected,
	onSelect,
	onRename,
}: {
	session: PromptSession;
	isSelected: boolean;
	onSelect: () => void;
	onRename: (name: string) => void;
}) {
	const [isEditing, setIsEditing] = useState(false);
	const inputRef = useRef<HTMLInputElement>(null);

	const handleRenameSubmit = () => {
		const newName = inputRef.current?.value.trim() ?? "";
		if (!newName || newName === session.name) {
			setIsEditing(false);
			return;
		}
		onRename(newName);
		setIsEditing(false);
	};

	const dateLabel = formatSessionDate(session.created_at);

	if (isEditing) {
		return (
			<div className="px-2 py-1.5" onKeyDown={(e) => e.stopPropagation()}>
				<Input
					ref={inputRef}
					defaultValue={session.name}
					placeholder="Session name"
					className="h-7 text-sm"
					autoFocus
					onKeyDown={(e) => {
						if (e.key === "Enter") handleRenameSubmit();
						if (e.key === "Escape") setIsEditing(false);
					}}
					onBlur={handleRenameSubmit}
				/>
			</div>
		);
	}

	return (
		<CommandItem
			value={`${session.id}-${dateLabel}-${session.name}`}
			onSelect={onSelect}
			className="group/item flex items-center justify-between gap-2"
		>
			<div className="flex min-w-0 flex-col">
				<span className="truncate text-sm">
					<span className="text-muted-foreground">{dateLabel}</span>
					{session.name && <span className="ml-1.5">{session.name}</span>}
				</span>
			</div>
			<div className="flex shrink-0 items-center gap-1">
				<button
					type="button"
					aria-label="Rename session"
					onPointerDown={(e) => {
						e.preventDefault();
						e.stopPropagation();
					}}
					onClick={(e) => {
						e.preventDefault();
						e.stopPropagation();
						setIsEditing(true);
					}}
					className="hover:bg-muted focus:bg-muted rounded-sm p-1 opacity-0 transition-opacity group-hover/item:opacity-100 focus:opacity-100"
				>
					<PencilIcon className="text-muted-foreground hover:text-foreground h-3.5 w-3.5 cursor-pointer" />
				</button>
				{isSelected && <Check className="text-primary h-4 w-4" />}
			</div>
		</CommandItem>
	);
}
