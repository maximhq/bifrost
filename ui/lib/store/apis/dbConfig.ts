import { baseApi } from "./baseApi";

export interface DbConfig {
  enabled: boolean;
  type: string;
  config: Record<string, any>;
}

export interface UpdateDbResponse {
  status: string;
  message: string;
  config: DbConfig;
}

export const dbApi = baseApi.injectEndpoints({
  endpoints: (builder) => ({
    // Get DB configuration (reads from config.json or SQLite fallback)
    getDbConfig: builder.query<DbConfig, void>({
      query: () => ({
        url: "/db",
      }),
      providesTags: ["DbConfig"],
    }),

    // Update DB configuration (writes to config.json)
    updateDbConfig: builder.mutation<UpdateDbResponse, DbConfig>({
      query: (data) => ({
        url: "/db",
        method: "POST",
        body: data,
      }),
      invalidatesTags: ["DbConfig"],
    }),
  }),
});

export const {
  useGetDbConfigQuery,
  useUpdateDbConfigMutation,
  useLazyGetDbConfigQuery,
} = dbApi;
