import { baseApi } from '@/lib/store/apis/baseApi';
import {
    CommitSessionRequest,
    CommitSessionResponse,
    CreateFolderRequest,
    CreateFolderResponse,
    CreatePromptRequest,
    CreatePromptResponse,
    CreateSessionRequest,
    CreateSessionResponse,
    CreateVersionRequest,
    CreateVersionResponse,
    DeleteFolderResponse,
    DeletePromptResponse,
    DeleteSessionResponse,
    DeleteVersionResponse,
    GetFolderResponse,
    GetFoldersResponse,
    GetPromptResponse,
    GetPromptsResponse,
    GetSessionResponse,
    GetSessionsResponse,
    GetVersionResponse,
    GetVersionsResponse,
    UpdateFolderRequest,
    UpdateFolderResponse,
    UpdatePromptRequest,
    UpdatePromptResponse,
    UpdateSessionRequest,
    UpdateSessionResponse,
} from '@/lib/types/prompts';

// Inject Prompt Repository endpoints into baseApi
export const promptsApi = baseApi.injectEndpoints({
  overrideExisting: true,
  endpoints: (builder) => ({
    // Get all folders
    getFolders: builder.query<GetFoldersResponse, void>({
      query: () => '/prompt-repo/folders',
      providesTags: ['Folders'],
    }),

    // Get single folder
    getFolder: builder.query<GetFolderResponse, string>({
      query: (id) => `/prompt-repo/folders/${id}`,
      providesTags: (result, error, id) => [{ type: 'Folders', id }],
    }),

    // Create folder
    createFolder: builder.mutation<CreateFolderResponse, CreateFolderRequest>({
      query: (data) => ({
        url: '/prompt-repo/folders',
        method: 'POST',
        body: data,
      }),
      invalidatesTags: ['Folders'],
    }),

    // Update folder
    updateFolder: builder.mutation<UpdateFolderResponse, { id: string; data: UpdateFolderRequest }>({
      query: ({ id, data }) => ({
        url: `/prompt-repo/folders/${id}`,
        method: 'PUT',
        body: data,
      }),
      invalidatesTags: (result, error, { id }) => ['Folders', { type: 'Folders', id }],
    }),

    // Delete folder
    deleteFolder: builder.mutation<DeleteFolderResponse, string>({
      query: (id) => ({
        url: `/prompt-repo/folders/${id}`,
        method: 'DELETE',
      }),
      invalidatesTags: ['Folders', 'Prompts'],
    }),

    // Get all prompts (optionally filtered by folder)
    getPrompts: builder.query<GetPromptsResponse, { folderId?: string } | void>({
      query: (params) => {
        const queryParams = params && params.folderId ? `?folder_id=${params.folderId}` : ''
        return `/prompt-repo/prompts${queryParams}`
      },
      providesTags: ['Prompts'],
    }),

    // Get single prompt
    getPrompt: builder.query<GetPromptResponse, string>({
      query: (id) => `/prompt-repo/prompts/${id}`,
      providesTags: (result, error, id) => [{ type: 'Prompts', id }],
    }),

    // Create prompt
    createPrompt: builder.mutation<CreatePromptResponse, CreatePromptRequest>({
      query: (data) => ({
        url: '/prompt-repo/prompts',
        method: 'POST',
        body: data,
      }),
      invalidatesTags: ['Prompts', 'Folders'],
    }),

    // Update prompt
    updatePrompt: builder.mutation<UpdatePromptResponse, { id: string; data: UpdatePromptRequest }>({
      query: ({ id, data }) => ({
        url: `/prompt-repo/prompts/${id}`,
        method: 'PUT',
        body: data,
      }),
      invalidatesTags: (result, error, { id }) => ['Prompts', { type: 'Prompts', id }],
      async onQueryStarted({ id, data }, { dispatch, queryFulfilled }) {
        // Optimistic update on the prompts list cache
        const patchResult = dispatch(
          promptsApi.util.updateQueryData('getPrompts', undefined, (draft) => {
            const prompt = draft.prompts.find((p) => p.id === id)
            if (prompt) {
              if (data.name !== undefined) prompt.name = data.name
              if ('folder_id' in data) prompt.folder_id = data.folder_id ?? undefined
            }
          })
        )
        try {
          await queryFulfilled
        } catch {
          patchResult.undo()
        }
      },
    }),

    // Delete prompt
    deletePrompt: builder.mutation<DeletePromptResponse, string>({
      query: (id) => ({
        url: `/prompt-repo/prompts/${id}`,
        method: 'DELETE',
      }),
      invalidatesTags: ['Prompts', 'Folders'],
    }),

    // Get all versions for a prompt
    getVersions: builder.query<GetVersionsResponse, string>({
      query: (promptId) => `/prompt-repo/prompts/${promptId}/versions`,
      providesTags: (result, error, promptId) => [{ type: 'Versions', id: promptId }],
    }),

    // Get single version
    getPromptVersion: builder.query<GetVersionResponse, number>({
      query: (id) => `/prompt-repo/versions/${id}`,
      providesTags: (result, error, id) => [{ type: 'Versions', id }],
    }),

    // Create version
    createVersion: builder.mutation<CreateVersionResponse, { promptId: string; data: CreateVersionRequest }>({
      query: ({ promptId, data }) => ({
        url: `/prompt-repo/prompts/${promptId}/versions`,
        method: 'POST',
        body: data,
      }),
      invalidatesTags: (result, error, { promptId }) => [
        'Prompts',
        { type: 'Prompts', id: promptId },
        { type: 'Versions', id: promptId },
      ],
    }),

    // Delete version
    deleteVersion: builder.mutation<DeleteVersionResponse, { id: number; promptId: string }>({
      query: ({ id }) => ({
        url: `/prompt-repo/versions/${id}`,
        method: 'DELETE',
      }),
      invalidatesTags: (result, error, { promptId }) => [
        'Prompts',
        { type: 'Prompts', id: promptId },
        { type: 'Versions', id: promptId },
      ],
    }),

    // Get all sessions for a prompt
    getSessions: builder.query<GetSessionsResponse, string>({
      query: (promptId) => `/prompt-repo/prompts/${promptId}/sessions`,
      providesTags: (result, error, promptId) => [{ type: 'Sessions', id: promptId }],
    }),

    // Get single session
    getSession: builder.query<GetSessionResponse, number>({
      query: (id) => `/prompt-repo/sessions/${id}`,
      providesTags: (result, error, id) => [{ type: 'Sessions', id }],
    }),

    // Create session
    createSession: builder.mutation<CreateSessionResponse, { promptId: string; data: CreateSessionRequest }>({
      query: ({ promptId, data }) => ({
        url: `/prompt-repo/prompts/${promptId}/sessions`,
        method: 'POST',
        body: data,
      }),
      invalidatesTags: (result, error, { promptId }) => [{ type: 'Sessions', id: promptId }],
    }),

    // Update session
    updateSession: builder.mutation<UpdateSessionResponse, { id: number; promptId: string; data: UpdateSessionRequest }>({
      query: ({ id, data }) => ({
        url: `/prompt-repo/sessions/${id}`,
        method: 'PUT',
        body: data,
      }),
      invalidatesTags: (result, error, { id, promptId }) => [
        { type: 'Sessions', id },
        { type: 'Sessions', id: promptId },
      ],
    }),

    // Delete session
    deleteSession: builder.mutation<DeleteSessionResponse, { id: number; promptId: string }>({
      query: ({ id }) => ({
        url: `/prompt-repo/sessions/${id}`,
        method: 'DELETE',
      }),
      invalidatesTags: (result, error, { promptId }) => [{ type: 'Sessions', id: promptId }],
    }),

    // Commit session as new version
    commitSession: builder.mutation<CommitSessionResponse, { id: number; promptId: string; data: CommitSessionRequest }>({
      query: ({ id, data }) => ({
        url: `/prompt-repo/sessions/${id}/commit`,
        method: 'POST',
        body: data,
      }),
      invalidatesTags: (result, error, { promptId }) => [
        'Prompts',
        { type: 'Prompts', id: promptId },
        { type: 'Versions', id: promptId },
      ],
    }),
  }),
})

export const {
  // Folders
  useGetFoldersQuery,
  useGetFolderQuery,
  useCreateFolderMutation,
  useUpdateFolderMutation,
  useDeleteFolderMutation,
  // Prompts
  useGetPromptsQuery,
  useGetPromptQuery,
  useCreatePromptMutation,
  useUpdatePromptMutation,
  useDeletePromptMutation,
  // Versions
  useGetVersionsQuery,
  useGetPromptVersionQuery,
  useLazyGetPromptVersionQuery,
  useCreateVersionMutation,
  useDeleteVersionMutation,
  // Sessions
  useGetSessionsQuery,
  useGetSessionQuery,
  useCreateSessionMutation,
  useUpdateSessionMutation,
  useDeleteSessionMutation,
  useCommitSessionMutation,
} = promptsApi
