import { fetchBaseQuery, createApi } from "@reduxjs/toolkit/query/react"

// OSS fallback: mirrors the enterprise licenseApi surface so shared consumers
// (clientLayout's LicenseGate, store.ts middleware wiring) compile and run in
// non-enterprise builds. OSS servers don't mount /api/license/*, so the status
// query resolves to a 404 (isError) and LicenseGate passes through.

export interface LicenseStatus {
  valid: boolean
  bootstrap_complete?: boolean
  expired?: boolean
  organization?: string
  tier?: string
  expires_at?: string
}

const licenseBaseQuery = fetchBaseQuery({
  baseUrl: "/api",
  credentials: "include",
})

export const licenseApi = createApi({
  reducerPath: "licenseApi",
  baseQuery: licenseBaseQuery,
  tagTypes: ["LicenseStatus"],
  endpoints: (builder) => ({
    getLicenseStatus: builder.query<LicenseStatus, void>({
      query: () => "/license/status",
      providesTags: ["LicenseStatus"],
    }),

    uploadLicense: builder.mutation<LicenseStatus, string>({
      query: (base64License) => ({
        url: "/license",
        method: "POST",
        body: base64License,
        headers: { "Content-Type": "text/plain" },
      }),
      invalidatesTags: ["LicenseStatus"],
    }),
  }),
})

export const { useGetLicenseStatusQuery, useUploadLicenseMutation } = licenseApi
