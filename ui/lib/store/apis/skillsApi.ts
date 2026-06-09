import { baseApi } from "@/lib/store/apis/baseApi";
import {
  CreateSkillRequest,
  CreateSkillResponse,
  GetSkillResponse,
  ListSkillsResponse,
  ListSkillVersionsResponse,
  ShiftSkillVersionRequest,
  UpdateSkillRequest,
  UpdateSkillResponse,
  UploadFileResponse,
} from "@/lib/types/skills";

// Inject Skills Repository endpoints into baseApi
export const skillsApi = baseApi.injectEndpoints({
  overrideExisting: true,
  endpoints: (builder) => ({
    // List all skills (paginated)
    listSkills: builder.query<
      ListSkillsResponse,
      { limit?: number; offset?: number; search?: string } | void
    >({
      query: (params) => {
        const searchParams = new URLSearchParams();
        if (params?.limit) searchParams.set("limit", String(params.limit));
        if (params?.offset) searchParams.set("offset", String(params.offset));
        if (params?.search) searchParams.set("search", params.search);
        const qs = searchParams.toString();
        return `/skills${qs ? `?${qs}` : ""}`;
      },
      providesTags: ["Skills"],
    }),

    // Get single skill by ID (optionally at a specific version)
    getSkill: builder.query<
      GetSkillResponse,
      string | { id: string; version?: string }
    >({
      query: (arg) => {
        const id = typeof arg === "string" ? arg : arg.id;
        const version = typeof arg === "string" ? undefined : arg.version;
        return `/skills/${id}${version ? `?version=${encodeURIComponent(version)}` : ""}`;
      },
      providesTags: (_result, _error, arg) => {
        const id = typeof arg === "string" ? arg : arg.id;
        return [{ type: "Skills", id }];
      },
    }),

    // Create a new skill
    createSkill: builder.mutation<CreateSkillResponse, CreateSkillRequest>({
      query: (data) => ({
        url: "/skills",
        method: "POST",
        body: data,
      }),
      invalidatesTags: ["Skills"],
    }),

    // Update an existing skill (creates a new version)
    updateSkill: builder.mutation<
      UpdateSkillResponse,
      { id: string; data: UpdateSkillRequest }
    >({
      query: ({ id, data }) => ({
        url: `/skills/${id}`,
        method: "PUT",
        body: data,
      }),
      invalidatesTags: (_result, _error, { id }) => [
        "Skills",
        { type: "Skills", id },
      ],
    }),

    // Delete a skill
    deleteSkill: builder.mutation<void, string>({
      query: (id) => ({
        url: `/skills/${id}`,
        method: "DELETE",
      }),
      invalidatesTags: (_result, _error, id) => [
        "Skills",
        { type: "Skills", id },
      ],
    }),

    // List versions for a skill (paginated)
    listSkillVersions: builder.query<
      ListSkillVersionsResponse,
      { id: string; limit?: number; offset?: number }
    >({
      query: ({ id, ...params }) => {
        const searchParams = new URLSearchParams();
        if (params?.limit) searchParams.set("limit", String(params.limit));
        if (params?.offset) searchParams.set("offset", String(params.offset));
        const qs = searchParams.toString();
        return `/skills/${id}/versions${qs ? `?${qs}` : ""}`;
      },
      providesTags: (_result, _error, { id }) => [
        { type: "Skills", id: `${id}-versions` },
      ],
    }),

    // Shift a skill to serve a specific version
    shiftSkillVersion: builder.mutation<
      GetSkillResponse,
      ShiftSkillVersionRequest
    >({
      query: ({ id, version }) => ({
        url: `/skills/${id}/shift-version`,
        method: "POST",
        body: { version },
      }),
      invalidatesTags: (_result, _error, { id }) => [
        "Skills",
        { type: "Skills", id },
        { type: "Skills", id: `${id}-versions` },
      ],
    }),

    // Upload a skill file
    uploadSkillFile: builder.mutation<UploadFileResponse, { file: File }>({
      query: ({ file }) => {
        const formData = new FormData();
        formData.append("file", file);
        return {
          url: "/skills/files/upload",
          method: "POST",
          body: formData,
          // Let the browser set the Content-Type with boundary for multipart
          headers: {
            // Remove the default Content-Type so fetch sets multipart boundary
          },
          formData: true,
        };
      },
    }),
  }),
});

export const {
  useListSkillsQuery,
  useGetSkillQuery,
  useCreateSkillMutation,
  useUpdateSkillMutation,
  useDeleteSkillMutation,
  useListSkillVersionsQuery,
  useShiftSkillVersionMutation,
  useUploadSkillFileMutation,
} = skillsApi;
