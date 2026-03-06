import { Button } from "@/components/ui/button";
import { SplitButton } from "@/components/ui/splitButton";
import { DropdownMenuItem, DropdownMenuLabel, DropdownMenuSeparator } from "@/components/ui/dropdownMenu";
import { Check, GitCommit, Save, Trash2 } from "lucide-react";
import { useCallback } from "react";
import { useHotkeys } from "react-hotkeys-hook";
import { parseAsInteger, useQueryStates } from "nuqs";
import { useCreateSessionMutation, useGetSessionsQuery, useGetVersionsQuery } from "@/lib/store/apis/promptsApi";
import { Message, MessageRole } from "@/lib/message";
import { ModelParams, PromptSession } from "@/app/enterprise/lib/types/prompts";
import { toast } from "sonner";
import { getErrorMessage } from "@/lib/store";
import { usePromptContext } from "../context";

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
	} = usePromptContext();

	const onSessionSaved = useCallback((session: PromptSession) => {
		setCommitSheet({ open: true, session });
	}, [setCommitSheet]);
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
		if (!selectedPrompt) return;
		try {
			const result = await createSession({
				promptId: selectedPrompt.id,
				data: {
					name: `Session ${new Date().toLocaleString()}`,
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
	}, [selectedPrompt?.id, messages, buildSaveParams, provider, model, createSession, setUrlState]);

	// Cmd+S / Ctrl+S to save session
	useHotkeys("mod+s", () => handleSaveSession(), {
		preventDefault: true,
		enableOnFormTags: ["input", "textarea", "select"],
		enabled: !!selectedPrompt && !isCreatingSession,
	}, [handleSaveSession, selectedPrompt, isCreatingSession]);

	const handleCommitVersion = useCallback(async () => {
		if (!selectedPrompt) return;
		try {
			// Always create a new session with current state before committing
			const result = await createSession({
				promptId: selectedPrompt.id,
				data: {
					name: `Session ${new Date().toLocaleString()}`,
					messages: Message.serializeAll(messages),
					model_params: buildSaveParams(),
					provider,
					model,
				},
			}).unwrap();
			setUrlState({ sessionId: result.session.id });
			onSessionSaved(result.session);
		} catch (err) {
			toast.error("Failed to save session", { description: getErrorMessage(err) });
		}
	}, [selectedPrompt?.id, messages, buildSaveParams, provider, model, createSession, setUrlState, onSessionSaved]);

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
					<Button variant="ghost" size="sm" onClick={handleClearConversation}>
						<Trash2 className="h-4 w-4" />
						Clear
					</Button>
				)}
				<SplitButton
					onClick={handleSaveSession}
					disabled={isCreatingSession}
					isLoading={isCreatingSession}
					dropdownContent={{
						className: "w-64 max-h-72 overflow-y-auto",
						children: (
							<>
								<DropdownMenuLabel>Sessions</DropdownMenuLabel>
								<DropdownMenuSeparator />
								{sessions.length === 0 ? (
									<div className="text-muted-foreground px-2 py-3 text-center text-sm">No sessions yet</div>
								) : (
									sessions.map((session) => (
										<DropdownMenuItem
											key={session.id}
											onClick={() => setUrlState({ sessionId: session.id, versionId: null })}
											className="flex items-center justify-between gap-2"
										>
											<div className="flex min-w-0 flex-col">
												<span className="truncate text-sm">{session.name}</span>
												<span className="text-muted-foreground text-xs">{new Date(session.created_at).toLocaleString()}</span>
											</div>
											{selectedSessionId === session.id && <Check className="text-primary h-4 w-4 shrink-0" />}
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
					<Save className="h-4 w-4" />
					Save Session
				</SplitButton>
				<SplitButton
					onClick={handleCommitVersion}
					disabled={isCreatingSession}
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
