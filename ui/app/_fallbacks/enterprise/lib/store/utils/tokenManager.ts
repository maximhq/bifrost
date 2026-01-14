// Fallback OAuth Token Manager for non-enterprise builds
// These functions return null/no-op when enterprise features are not available

export const getAccessToken = async (): Promise<string | null> => Promise.resolve(null)

export const getRefreshToken = async (): Promise<string | null> => Promise.resolve(null)

export const getTokenExpiry = (): number | null => null

export const isTokenExpired = (): boolean => false

export const setOAuthTokens = async (accessToken: string, expiresIn?: number | null) => {
  // No-op in non-enterprise builds
}

export const clearOAuthStorage = () => {
  // No-op in non-enterprise builds
}

export const getRefreshState = () => ({
  isRefreshing: false,
  refreshPromise: null
})

export const setRefreshState = (refreshing: boolean, promise: Promise<any> | null = null) => {
  // No-op in non-enterprise builds
}

export const REFRESH_TOKEN_ENDPOINT = ''

// User info type definition (matching enterprise version)
export interface UserInfo {
  name?: string
  email?: string
  picture?: string
  preferred_username?: string
  given_name?: string
  family_name?: string
}

// Fallback getUserInfo that returns null for non-enterprise builds
export const getUserInfo = (): UserInfo | null => null

// Fallback setUserInfo - no-op
export const setUserInfo = (userInfo: UserInfo) => {
  // No-op in non-enterprise builds
}

// Fallback clearUserInfo - no-op
export const clearUserInfo = () => {
  // No-op in non-enterprise builds
}

// Fallback secure storage functions - no-op
export const setSecureItem = async (key: string, value: string): Promise<void> => {
  // No-op in non-enterprise builds
}

export const getSecureItem = async (key: string): Promise<string | null> => Promise.resolve(null)

export const removeSecureItem = (key: string): void => {
  // No-op in non-enterprise builds
}

export const setSecureLocalItem = async (key: string, value: string): Promise<void> => {
  // No-op in non-enterprise builds
}

export const getSecureLocalItem = async (key: string): Promise<string | null> => Promise.resolve(null)

export const removeSecureLocalItem = (key: string): void => {
  // No-op in non-enterprise builds
}

export const clearEncryptionKey = (): void => {
  // No-op in non-enterprise builds
}

