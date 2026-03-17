import { Button } from "@/components/ui/button";
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@/components/ui/command";
import { SplitButton } from "@/components/ui/splitButton";
import { DropdownMenuItem, DropdownMenuLabel, DropdownMenuSeparator } from "@/components/ui/dropdownMenu";
import { Check, GitCommit, PencilIcon, Save, Trash2 } from "lucide-react";
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
import { Badge } from "@/components/ui/badge";

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
		variables,
		hasChanges,
		hasVersionChanges,
		hasSessionChanges,
		isStreaming,
		canUpdate,
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
					variables: Object.keys(variables).length > 0 ? variables : undefined,
				},
			}).unwrap();
			setUrlState({ sessionId: result.session.id, versionId: null });
			toast.success("Session saved");
		} catch (err) {
			toast.error("Failed to save session", { description: getErrorMessage(err) });
		}
	}, [selectedPrompt?.id, messages, buildSaveParams, provider, model, variables, createSession, setUrlState, hasChanges, isStreaming]);

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
		if (!hasChanges) {
			const selectedSession = sessions.find((s) => s.id === selectedSessionId);
			if (selectedSession) {
				onSessionSaved(selectedSession);
			}
			return;
		}
		try {
			// Always create a new session with current state before committing
			const result = await createSession({
				promptId: selectedPrompt.id,
				data: {
					messages: Message.serializeAll(messages),
					model_params: buildSaveParams(),
					provider,
					model,
					variables: Object.keys(variables).length > 0 ? variables : undefined,
				},
			}).unwrap();
			setUrlState({ sessionId: result.session.id, versionId: null });
			onSessionSaved(result.session);
		} catch (err) {
			toast.error("Failed to save session", { description: getErrorMessage(err) });
		}
	}, [selectedPrompt?.id, messages, buildSaveParams, provider, model, variables, createSession, setUrlState, onSessionSaved, hasChanges]);

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

	const selectedVersion = versions.find((v) => v.id === selectedVersionId);
	const latestVersion = versions.find((v) => v.is_latest);
	const displayVersion = selectedVersion ?? latestVersion;

	return (
		<div className="flex items-center justify-between border-b px-4 py-3">
			<div className="flex min-w-0 items-center gap-2">
				<h3 className="truncate font-semibold">
					{selectedPrompt?.name || "Playground"}
					{hasChanges && <span className="text-destructive ml-1">*</span>}
				</h3>
				{displayVersion && <Badge variant={"secondary"}>v{displayVersion.version_number}</Badge>}
				{hasVersionChanges && versions.length > 0 && <Badge variant="outline">Unpublished Changes</Badge>}
			</div>
			<div className="flex shrink-0 items-center gap-4">
				{messages.length > 1 && (
					<Button variant="ghost" size="sm" data-testid="header-clear" onClick={handleClearConversation} disabled={isStreaming}>
						<Trash2 className="h-4 w-4" />
						Clear
					</Button>
				)}
				<SplitButton
					onClick={handleSaveSession}
					disabled={isCreatingSession || isStreaming}
					isLoading={isCreatingSession}
					dropdownContent={{
						className: "w-72 p-0",
						open: sessionsOpen,
						onOpenChange: setSessionsOpen,
						children: (
							<Command>
								<CommandInput placeholder="Search sessions..." data-testid="header-sessions-search" />
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
						),
					}}
					variant={"outline"}
					dropdownTrigger={{
						className: cn("bg-transparent"),
					}}
					button={{
						dataTestId: "header-save-session",
						className: "bg-transparent disabled:opacity-100 disabled:text-muted-foreground",
						disabled: !hasChanges || !canUpdate,
					}}
				>
					<Save className="h-4 w-4" />
					Save Session
				</SplitButton>
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
												<span className="text-muted-foreground text-xs">{formatSessionDate(version.created_at)}</span>
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
						className: cn("bg-transparent"),
					}}
					button={{
						dataTestId: "header-commit-version",
						className: "bg-transparent disabled:opacity-100 disabled:text-muted-foreground",
						disabled: !hasVersionChanges || !canUpdate,
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
			<div className="flex items-center gap-2 rounded-sm px-2 py-1.5" onKeyDown={(e) => e.stopPropagation()}>
				<Input
					ref={inputRef}
					defaultValue={session.name}
					placeholder="Session name"
					className="h-auto border-none bg-transparent p-0 text-sm shadow-none focus-visible:border-none focus-visible:ring-0"
					data-testid="session-rename-input"
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
			className="group/item flex items-center justify-between gap-2 py-1"
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
					data-testid="session-rename"
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
