// Fallback exports for non-enterprise builds
export * from "./store";

// Re-export OAuth token management utilities for convenience (fallback no-ops)
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
} from "./store/utils/tokenManager";

// Re-export base query (fallback passthrough)
export { createBaseQueryWithRefresh } from "./store/utils/baseQueryWithRefresh";

// Re-export RBAC context (dummy implementation for OSS)
export * from "./contexts/rbacContext";

