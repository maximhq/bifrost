"use client";

import { getErrorMessage } from "@/lib/store";
import {
	useCreateSessionMutation,
	useDeleteFolderMutation,
	useDeletePromptMutation,
	useGetFoldersQuery,
	useGetPromptsQuery,
	useGetPromptVersionQuery,
	useGetSessionsQuery,
	useGetVersionsQuery,
	useUpdatePromptMutation,
} from "@/lib/store/apis/promptsApi";
import { useGetModelParametersQuery } from "@/lib/store/apis/providersApi";
import { Message, MessageRole, MessageType, extractVariablesFromMessages, mergeVariables, type VariableMap } from "@/lib/message";
import { Folder, ModelParams, Prompt, PromptSession, PromptVersion } from "@/lib/types/prompts";
import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { parseAsInteger, parseAsString, useQueryStates } from "nuqs";
import { toast } from "sonner";
import { executePrompt } from "./utils/executor";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";

interface PromptContextValue {
	// Data
	folders: Folder[];
	prompts: Prompt[];
	selectedPrompt?: Prompt;
	sessions: PromptSession[];
	selectedSession?: PromptSession;
	selectedVersion?: PromptVersion;

	// Loading states
	foldersLoading: boolean;
	promptsLoading: boolean;
	foldersError: unknown;
	promptsError: unknown;
	isLoadingPlayground: boolean;
	isStreaming: boolean;

	// URL state
	selectedPromptId: string | null;
	selectedSessionId: number | null;
	selectedVersionId: number | null;
	setUrlState: (state: { promptId?: string | null; sessionId?: number | null; versionId?: number | null }) => void;

	// Playground state
	messages: Message[];
	setMessages: React.Dispatch<React.SetStateAction<Message[]>>;
	provider: string;
	setProvider: React.Dispatch<React.SetStateAction<string>>;
	model: string;
	setModel: React.Dispatch<React.SetStateAction<string>>;
	modelParams: ModelParams;
	setModelParams: React.Dispatch<React.SetStateAction<ModelParams>>;
	apiKeyId: string;
	setApiKeyId: React.Dispatch<React.SetStateAction<string>>;

	// Jinja2 variables
	variables: VariableMap;
	setVariables: React.Dispatch<React.SetStateAction<VariableMap>>;

	// Sheet states
	folderSheet: { open: boolean; folder?: Folder };
	setFolderSheet: React.Dispatch<React.SetStateAction<{ open: boolean; folder?: Folder }>>;
	promptSheet: { open: boolean; prompt?: Prompt; folderId?: string };
	setPromptSheet: React.Dispatch<React.SetStateAction<{ open: boolean; prompt?: Prompt; folderId?: string }>>;
	commitSheet: { open: boolean; session?: PromptSession };
	setCommitSheet: React.Dispatch<React.SetStateAction<{ open: boolean; session?: PromptSession }>>;

	// Delete dialog states
	deleteFolderDialog: { open: boolean; folder?: Folder };
	setDeleteFolderDialog: React.Dispatch<React.SetStateAction<{ open: boolean; folder?: Folder }>>;
	deletePromptDialog: { open: boolean; prompt?: Prompt };
	setDeletePromptDialog: React.Dispatch<React.SetStateAction<{ open: boolean; prompt?: Prompt }>>;

	// Mutation loading states
	isDeletingFolder: boolean;
	isDeletingPrompt: boolean;

	// Model capabilities
	supportsVision: boolean;

	// Diff detection
	hasChanges: boolean;
	hasVersionChanges: boolean;
	hasSessionChanges: boolean;

	// Handlers
	handleSelectPrompt: (id: string) => void;
	handleMovePrompt: (promptId: string, folderId: string | null) => Promise<void>;
	handleDeleteFolder: () => Promise<void>;
	handleDeletePrompt: () => Promise<void>;
	handleSendMessage: (pendingMessage?: Message) => Promise<void>;
	handleSubmitToolResult: (afterIndex: number, toolCallId: string, content: string) => Promise<void>;

	// RBAC permissions
	canCreate: boolean;
	canUpdate: boolean;
	canDelete: boolean;
}

const PromptContext = createContext<PromptContextValue | null>(null);

export function usePromptContext() {
	const context = useContext(PromptContext);
	if (!context) {
		throw new Error("usePromptContext must be used within a PromptProvider");
	}
	return context;
}

export function PromptProvider({ children }: { children: ReactNode }) {
	// RBAC permissions
	const canCreate = useRbac(RbacResource.PromptRepository, RbacOperation.Create);
	const canUpdate = useRbac(RbacResource.PromptRepository, RbacOperation.Update);
	const canDelete = useRbac(RbacResource.PromptRepository, RbacOperation.Delete);

	// API queries
	const { data: foldersData, isLoading: foldersLoading, error: foldersError } = useGetFoldersQuery();
	const { data: promptsData, isLoading: promptsLoading, error: promptsError } = useGetPromptsQuery();

	// Mutations
	const [deleteFolder, { isLoading: isDeletingFolder }] = useDeleteFolderMutation();
	const [deletePrompt, { isLoading: isDeletingPrompt }] = useDeletePromptMutation();
	const [updatePrompt] = useUpdatePromptMutation();
	const [createSession] = useCreateSessionMutation();

	// UI state — persisted in URL query params
	const [{ promptId: selectedPromptId, sessionId: selectedSessionId, versionId: selectedVersionId }, setUrlState] = useQueryStates(
		{
			promptId: parseAsString,
			sessionId: parseAsInteger,
			versionId: parseAsInteger,
		},
		{ history: "replace" },
	);

	// Sheet states
	const [folderSheet, setFolderSheet] = useState<{ open: boolean; folder?: Folder }>({ open: false });
	const [promptSheet, setPromptSheet] = useState<{ open: boolean; prompt?: Prompt; folderId?: string }>({ open: false });
	const [commitSheet, setCommitSheet] = useState<{ open: boolean; session?: PromptSession }>({ open: false });

	// Delete dialog states
	const [deleteFolderDialog, setDeleteFolderDialog] = useState<{ open: boolean; folder?: Folder }>({ open: false });
	const [deletePromptDialog, setDeletePromptDialog] = useState<{ open: boolean; prompt?: Prompt }>({ open: false });

	// Playground state
	const [messages, setMessagesRaw] = useState<Message[]>([Message.system("")]);
	const setMessages = useCallback<React.Dispatch<React.SetStateAction<Message[]>>>((action) => {
		setMessagesRaw((prev) => {
			const next = typeof action === "function" ? action(prev) : action;
			return next.map((msg, i) => msg.withIndex(i));
		});
	}, []);
	const [provider, setProvider] = useState("");
	const [model, setModel] = useState("");
	const [modelParams, setModelParams] = useState<ModelParams>({});
	const [apiKeyId, setApiKeyId] = useState("__auto__");
	const [isStreaming, setIsStreaming] = useState(false);
	const activeRunRef = useRef<symbol | null>(null);
	const [variables, setVariables] = useState<VariableMap>({});

	// Fetch model datasheet for capabilities
	const { data: datasheetData } = useGetModelParametersQuery(model, { skip: !model });
	const supportsVision = datasheetData?.supports_vision ?? false;

	// Derived data
	const folders = useMemo(() => foldersData?.folders ?? [], [foldersData]);
	const prompts = useMemo(() => promptsData?.prompts ?? [], [promptsData]);
	const selectedPrompt = useMemo(() => prompts.find((p) => p.id === selectedPromptId), [prompts, selectedPromptId]);

	// Fetch versions and sessions for selected prompt
	const { data: versionsData } = useGetVersionsQuery(selectedPromptId ?? "", { skip: !selectedPromptId });
	const { data: sessionsData, isLoading: isSessionsLoading } = useGetSessionsQuery(selectedPromptId ?? "", { skip: !selectedPromptId });

	// Filter sessions to current prompt — RTK Query may briefly return stale cached data from the previous prompt
	const sessions = useMemo(() => {
		const all = sessionsData?.sessions ?? [];
		if (!selectedPromptId) return [];
		return all.filter((s) => s.prompt_id === selectedPromptId);
	}, [sessionsData, selectedPromptId]);
	const selectedSession = useMemo(() => sessions.find((s) => s.id === selectedSessionId), [sessions, selectedSessionId]);

	// Fetch full version data (with messages) when a version is selected
	const { currentData: selectedVersionData, isLoading: isVersionLoading, isFetching: isVersionFetching } = useGetPromptVersionQuery(selectedVersionId ?? 0, {
		skip: !selectedVersionId,
	});
	const selectedVersion = selectedVersionData?.version;

	// Show loader only on initial fetch, not on cache refetches (avoids flicker on save)
	const isLoadingPlayground = !!(
		selectedPromptId &&
		(isSessionsLoading ||
			(selectedVersionId && isVersionLoading) ||
			// Sessions loaded but auto-select hasn't happened yet
			(sessions.length > 0 && !selectedSessionId && !selectedVersionId))
	);

	// Load session or version data when selection changes
	useEffect(() => {
		// Don't reset state while waiting for data that hasn't arrived yet
		if (selectedSessionId && !selectedSession) return;
		if (selectedVersionId && !selectedVersion) return;

		const loadFromParams = (params: ModelParams, prov: string, mod: string) => {
			const { api_key_id, ...rest } = params || ({} as ModelParams);
			setModelParams(rest);
			setApiKeyId(api_key_id || "__auto__");
			setProvider(prov || "");
			setModel(mod || "");
		};

		const loadMessages = (msgs: Message[]) => {
			setMessages(msgs);
			const varNames = extractVariablesFromMessages(msgs);
			setVariables((prev) => mergeVariables(prev, varNames));
		};

		if (selectedSession) {
			const raw = (selectedSession.messages ?? []).map((m) => m.message);
			const loaded = Message.fromLegacyAll(raw);
			loadMessages(loaded.length > 0 ? loaded : [Message.system("")]);
			loadFromParams(selectedSession.model_params, selectedSession.provider, selectedSession.model);
		} else if (selectedVersion) {
			// If sessions are still loading and no session is explicitly selected,
			// wait — a session may auto-select and take priority
			if (isSessionsLoading && !selectedSessionId) return;
			const raw = (selectedVersion.messages ?? []).map((m) => m.message);
			const loaded = Message.fromLegacyAll(raw);
			loadMessages(loaded.length > 0 ? loaded : [Message.system("")]);
			loadFromParams(selectedVersion.model_params, selectedVersion.provider, selectedVersion.model);
		} else if (selectedPrompt?.latest_version) {
			// Only fall back to latest_version after sessions have settled
			// to avoid racing with the session auto-select effect
			if (isSessionsLoading) return;
			const version = selectedPrompt.latest_version;
			const raw = (version.messages ?? []).map((m) => m.message);
			const loaded = Message.fromLegacyAll(raw);
			loadMessages(loaded.length > 0 ? loaded : [Message.system("")]);
			loadFromParams(version.model_params, version.provider, version.model);
			if (sessions.length === 0) {
				setUrlState({ versionId: version.id });
			}
		} else {
			setMessages([Message.system("")]);
			setProvider("");
			setModel("");
			setModelParams({});
			setApiKeyId("__auto__");
		}
	}, [selectedSession, selectedVersion, selectedPrompt, selectedSessionId, selectedVersionId, setUrlState, isSessionsLoading, sessions.length]);

	// Auto-select the most recent session when sessions load and none is selected
	// Sessions take priority over versions for initial loading
	useEffect(() => {
		if (sessions.length > 0 && !selectedSessionId && !selectedVersionId) {
			setUrlState({ sessionId: sessions[0].id });
		}
	}, [selectedPromptId, sessions, selectedSessionId, selectedVersionId, setUrlState]);

	// Diff detection helper — compares current playground state against a reference config
	const diffAgainst = useCallback(
		(ref: { messages?: any[]; model_params?: ModelParams; provider?: string; model?: string } | undefined) => {
			if (!ref) return true; // No reference — treat as changed
			const refMessages = ref.messages ?? [];
			const refProvider = ref.provider;
			const refModel = ref.model;
			const refParams = ref.model_params;

			if (provider !== refProvider) return true;
			if (model !== refModel) return true;

			const { api_key_id: refApiKeyId, ...refParamsRest } = refParams || ({} as ModelParams);
			const currentApiKeyId = apiKeyId !== "__auto__" ? apiKeyId : undefined;
			if (currentApiKeyId !== (refApiKeyId || undefined)) return true;

			if (JSON.stringify(modelParams, Object.keys(modelParams).sort()) !== JSON.stringify(refParamsRest, Object.keys(refParamsRest).sort()))
				return true;

			const currentSerialized = Message.serializeAll(messages);
			if (JSON.stringify(currentSerialized) !== JSON.stringify(refMessages)) return true;

			return false;
		},
		[provider, model, modelParams, apiKeyId, messages],
	);

	// Diff detection — compare current playground state against the loaded session/version
	const hasChanges = useMemo(() => {
		// Suppress diff while version data is in flight to avoid flicker
		if (selectedVersionId && (isVersionFetching || selectedVersion?.id !== selectedVersionId)) return false;
		if (selectedSession) {
			return diffAgainst({
				messages: selectedSession.messages?.map((m) => m.message) ?? [],
				model_params: selectedSession.model_params,
				provider: selectedSession.provider,
				model: selectedSession.model,
			});
		}
		if (selectedVersion) {
			return diffAgainst({
				messages: selectedVersion.messages?.map((m) => m.message) ?? [],
				model_params: selectedVersion.model_params,
				provider: selectedVersion.provider,
				model: selectedVersion.model,
			});
		}
		return true;
	}, [selectedSession, selectedVersion, diffAgainst, selectedVersionId, isVersionFetching]);

	// Diff against the active version — drives "unpublished changes" badge & commit button
	// Uses the explicitly selected version if available, otherwise falls back to latest_version
	const activeVersionRef = selectedVersion ?? selectedPrompt?.latest_version;

	const hasVersionChanges = useMemo(() => {
		// Suppress diff while version data is in flight or mismatched to avoid flash
		if (selectedVersionId && (isVersionFetching || selectedVersion?.id !== selectedVersionId)) return false;
		if (!activeVersionRef) return true; // No versions yet — always allow commit
		return diffAgainst({
			messages: activeVersionRef.messages?.map((m) => m.message) ?? [],
			model_params: activeVersionRef.model_params,
			provider: activeVersionRef.provider,
			model: activeVersionRef.model,
		});
	}, [activeVersionRef, diffAgainst, selectedVersionId, isVersionFetching, selectedVersion?.id]);

	// Diff against the selected session — drives red asterisk indicator
	const hasSessionChanges = useMemo(() => {
		if (!selectedSession) return false;
		return diffAgainst({
			messages: selectedSession.messages?.map((m) => m.message) ?? [],
			model_params: selectedSession.model_params,
			provider: selectedSession.provider,
			model: selectedSession.model,
		});
	}, [selectedSession, diffAgainst]);

	// Handlers
	const handleSelectPrompt = useCallback(
		(id: string) => {
			setMessages([Message.system("")]);
			setProvider("");
			setModel("");
			setModelParams({});
			setApiKeyId("__auto__");
			setUrlState({ promptId: id, sessionId: null, versionId: null });
		},
		[setUrlState],
	);

	const handleMovePrompt = useCallback(
		async (promptId: string, folderId: string | null) => {
			try {
				await updatePrompt({ id: promptId, data: { folder_id: folderId } }).unwrap();
				toast.success("Prompt moved successfully");
			} catch (err) {
				toast.error(getErrorMessage(err) || "Failed to move prompt");
			}
		},
		[updatePrompt],
	);

	const handleDeleteFolder = useCallback(async () => {
		if (!deleteFolderDialog.folder) return;

		try {
			await deleteFolder(deleteFolderDialog.folder.id).unwrap();
			toast.success("Folder deleted");
			setDeleteFolderDialog({ open: false });
			if (selectedPrompt?.folder_id === deleteFolderDialog.folder.id) {
				setUrlState({ promptId: null, sessionId: null, versionId: null });
			}
		} catch (err) {
			toast.error("Failed to delete folder", { description: getErrorMessage(err) });
		}
	}, [deleteFolderDialog.folder, deleteFolder, selectedPrompt, setUrlState]);

	const handleDeletePrompt = useCallback(async () => {
		if (!deletePromptDialog.prompt) return;

		try {
			await deletePrompt(deletePromptDialog.prompt.id).unwrap();
			toast.success("Prompt deleted");
			setDeletePromptDialog({ open: false });
			if (selectedPromptId === deletePromptDialog.prompt.id) {
				setUrlState({ promptId: null, sessionId: null, versionId: null });
			}
		} catch (err) {
			toast.error("Failed to delete prompt", { description: getErrorMessage(err) });
		}
	}, [deletePromptDialog.prompt, deletePrompt, selectedPromptId, setUrlState]);

	const handleSendMessage = useCallback(
		async (pendingMessage?: Message) => {
			const runToken = Symbol();
			activeRunRef.current = runToken;
			const isActive = () => activeRunRef.current === runToken;

			setIsStreaming(true);
			await executePrompt(messages, pendingMessage, { provider, model, modelParams, apiKeyId, variables }, {
				onStreamingStart: (allMessages, placeholder) => {
					if (!isActive()) return;
					setMessages([...allMessages, placeholder]);
				},
				onStreamChunk: (content) => {
					if (!isActive()) return;
					setMessages((prev) => {
						const updated = [...prev];
						const last = updated[updated.length - 1];
						const clone = last.clone();
						clone.content = content;
						updated[updated.length - 1] = clone;
						return updated;
					});
				},
				onComplete: (content, usage) => {
					if (!isActive()) return;
					setMessages((prev) => {
						const updated = [...prev];
						updated[updated.length - 1] = Message.response(content, 0, usage);
						return updated;
					});
				},
				onToolCallComplete: (content, toolCalls, usage) => {
					if (!isActive()) return;
					setMessages((prev) => {
						const updated = [...prev];
						updated[updated.length - 1] = Message.toolCallResponse(content, toolCalls, 0, usage);
						return updated;
					});
				},
				onEmptyResponse: () => {
					if (!isActive()) return;
					setMessages((prev) => prev.slice(0, -1));
				},
				onError: (error) => {
					if (!isActive()) return;
					setMessages((prev) => {
						const withoutPlaceholder = prev.slice(0, -1);
						return [...withoutPlaceholder, Message.error(error)];
					});
				},
				onFinally: () => {
					if (!isActive()) return;
					setIsStreaming(false);
				},
			});
		},
		[messages, provider, model, modelParams, apiKeyId, variables],
	);

	const handleSubmitToolResult = useCallback(
		async (afterIndex: number, toolCallId: string, content: string) => {
			const runToken = Symbol();
			activeRunRef.current = runToken;
			const isActive = () => activeRunRef.current === runToken;

			const toolResultMsg = new Message(
				crypto.randomUUID(),
				0,
				MessageType.ToolResult,
				{
					role: MessageRole.TOOL,
					content,
					tool_call_id: toolCallId,
				},
			);
			const newMessages = [...messages];
			// Insert after any existing tool results that follow the assistant message
			let insertAt = afterIndex + 1;
			while (insertAt < newMessages.length && newMessages[insertAt].type === MessageType.ToolResult) {
				insertAt++;
			}
			newMessages.splice(insertAt, 0, toolResultMsg);
			setMessages(newMessages);

			// Execute with the updated messages
			setIsStreaming(true);
			await executePrompt(newMessages, undefined, { provider, model, modelParams, apiKeyId, variables }, {
				onStreamingStart: (allMessages, placeholder) => {
					if (!isActive()) return;
					setMessages([...allMessages, placeholder]);
				},
				onStreamChunk: (content) => {
					if (!isActive()) return;
					setMessages((prev) => {
						const updated = [...prev];
						const last = updated[updated.length - 1];
						const clone = last.clone();
						clone.content = content;
						updated[updated.length - 1] = clone;
						return updated;
					});
				},
				onComplete: (content, usage) => {
					if (!isActive()) return;
					setMessages((prev) => {
						const updated = [...prev];
						updated[updated.length - 1] = Message.response(content, 0, usage);
						return updated;
					});
				},
				onToolCallComplete: (content, toolCalls, usage) => {
					if (!isActive()) return;
					setMessages((prev) => {
						const updated = [...prev];
						updated[updated.length - 1] = Message.toolCallResponse(content, toolCalls, 0, usage);
						return updated;
					});
				},
				onEmptyResponse: () => {
					if (!isActive()) return;
					setMessages((prev) => prev.slice(0, -1));
				},
				onError: (error) => {
					if (!isActive()) return;
					setMessages((prev) => {
						const withoutPlaceholder = prev.slice(0, -1);
						return [...withoutPlaceholder, Message.error(error)];
					});
				},
				onFinally: () => {
					if (!isActive()) return;
					setIsStreaming(false);
				},
			});
		},
		[messages, provider, model, modelParams, apiKeyId, variables],
	);

	const value: PromptContextValue = {
		folders,
		prompts,
		selectedPrompt,
		sessions,
		selectedSession,
		selectedVersion,
		foldersLoading,
		promptsLoading,
		foldersError,
		promptsError,
		isLoadingPlayground,
		isStreaming,
		selectedPromptId,
		selectedSessionId,
		selectedVersionId,
		setUrlState,
		messages,
		setMessages,
		provider,
		setProvider,
		model,
		setModel,
		modelParams,
		setModelParams,
		apiKeyId,
		setApiKeyId,
		variables,
		setVariables,
		folderSheet,
		setFolderSheet,
		promptSheet,
		setPromptSheet,
		commitSheet,
		setCommitSheet,
		deleteFolderDialog,
		setDeleteFolderDialog,
		deletePromptDialog,
		setDeletePromptDialog,
		isDeletingFolder,
		isDeletingPrompt,
		supportsVision,
		hasChanges,
		hasVersionChanges,
		hasSessionChanges,
		handleSelectPrompt,
		handleMovePrompt,
		handleDeleteFolder,
		handleDeletePrompt,
		handleSendMessage,
		handleSubmitToolResult,
		canCreate,
		canUpdate,
		canDelete,
	};

	return <PromptContext.Provider value={value}>{children}</PromptContext.Provider>;
}
