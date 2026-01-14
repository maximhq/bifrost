// Fallback exports for non-enterprise builds
export * from "./apis";
export * from "./slices";

// Export OAuth token management utilities (fallback no-ops)
export {
    REFRESH_TOKEN_ENDPOINT, clearOAuthStorage,
    clearUserInfo,
    getAccessToken,
    getRefreshState,
    getRefreshToken,
    getTokenExpiry,
    getUserInfo,
    isTokenExpired, setOAuthTokens,
    setRefreshState,
    setUserInfo,
    type UserInfo
} from "./utils/tokenManager";

// Export base query (fallback passthrough)
export { createBaseQueryWithRefresh } from "./utils/baseQueryWithRefresh";
