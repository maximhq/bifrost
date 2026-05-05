import { CacheConfig } from "@/lib/types/config";
import { baseApi } from "./baseApi";

// Server returns the LocalCacheConfig struct shape directly. TTL serializes
// as a number (nanoseconds, Go time.Duration default) when not set via the
// custom unmarshaler — but our PUT path always sends seconds, and the
// editor exposes it as seconds. We normalize on read to avoid surprises.
type LocalCacheConfigResponse = Omit<CacheConfig, "ttl"> & { ttl?: number | string };

export const localCacheApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		// GET /api/local-cache/config — current live config. Returns an empty
		// object when the plugin has never been configured (no DB row yet).
		getLocalCacheConfig: builder.query<CacheConfig, void>({
			query: () => "/local-cache/config",
			providesTags: ["LocalCacheConfig"],
			transformResponse: (response: LocalCacheConfigResponse): CacheConfig => {
				// Go time.Duration JSON-serializes as a string ("5m") when the
				// custom UnmarshalJSON path is used, or as raw nanoseconds (a
				// number) when fields default. The editor wants seconds.
				const ttl = (() => {
					if (response.ttl == null) return 0;
					if (typeof response.ttl === "number") {
						// Heuristic: values larger than 1e7 are nanoseconds, else
						// seconds (no real cache TTL is 10M seconds = ~115 days).
						return response.ttl > 1e7 ? Math.round(response.ttl / 1e9) : response.ttl;
					}
					// String like "5m", "30s" — naive parse for the common cases.
					const match = String(response.ttl).match(/^(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h)?$/);
					if (!match) return 0;
					const value = parseFloat(match[1]);
					switch (match[2]) {
						case "ns": return value / 1e9;
						case "us":
						case "µs": return value / 1e6;
						case "ms": return value / 1e3;
						case "m": return value * 60;
						case "h": return value * 3600;
						case "s":
						default: return value;
					}
				})();
				return { ...response, ttl } as CacheConfig;
			},
		}),

		// PUT /api/local-cache/config — persists, then mutates the in-memory
		// shared pointer so the running plugin observes the new values on its
		// next request without a reload.
		updateLocalCacheConfig: builder.mutation<CacheConfig, CacheConfig>({
			query: (data) => ({
				url: "/local-cache/config",
				method: "PUT",
				body: data,
			}),
			invalidatesTags: ["LocalCacheConfig"],
		}),
	}),
});

export const {
	useGetLocalCacheConfigQuery,
	useUpdateLocalCacheConfigMutation,
} = localCacheApi;
