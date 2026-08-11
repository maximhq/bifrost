import { baseApi } from "./baseApi";

/**
 * custom branding.
 *
 * Enterprise deployments can replace the Bifrost logo and icon. Every endpoint
 * below lives in the enterprise build — the read is public there (the login
 * screen renders before any session exists), while the writes carry auth and
 * RBAC. On OSS none of them exist: callers skip the query (see useBranding) and
 * every surface keeps the bundled defaults.
 *
 * The response carries URLs rather than base64 so the images are fetched and
 * cached by the browser as ordinary assets instead of riding along in JSON.
 */
export interface BrandingState {
	/** True when at least one slot is overridden. */
	enabled: boolean;
	has_logo: boolean;
	has_icon: boolean;
	/** Content-versioned; empty when the slot uses the Bifrost default. */
	logo_url?: string;
	icon_url?: string;
	updated_at?: string;
}

/**
 * Upload payload, applied as a merge:
 *
 *   omitted        -> leave that slot as stored
 *   ""             -> clear that slot, restoring the Bifrost default
 *   base64 data    -> replace that slot
 *
 * So a caller changing only the icon omits the logo fields rather than
 * re-uploading the logo to avoid wiping it.
 */
export interface BrandingPayload {
	logo?: string; // base64 image data (a full data: URI is also accepted)
	logo_mime?: string;
	icon?: string;
	icon_mime?: string;
}

export const brandingApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		getBranding: builder.query<BrandingState, void>({
			query: () => ({
				url: "/branding",
			}),
			providesTags: ["Branding"],
		}),

		updateBranding: builder.mutation<BrandingState, BrandingPayload>({
			query: (data) => ({
				url: "/branding",
				method: "PUT",
				body: data,
			}),
			invalidatesTags: ["Branding"],
		}),

		// Clears branding and restores the default Bifrost logo and icon.
		resetBranding: builder.mutation<BrandingState, void>({
			query: () => ({
				url: "/branding",
				method: "DELETE",
			}),
			invalidatesTags: ["Branding"],
		}),
	}),
});

export const { useGetBrandingQuery, useUpdateBrandingMutation, useResetBrandingMutation } = brandingApi;