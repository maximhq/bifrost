import { baseApi, setAuthToken } from './baseApi'

export interface LoginRequest {
  username: string
  password: string
}

export interface LoginResponse {
  message: string
  token: string
}

export interface IsAuthEnabledResponse {
  is_auth_enabled: boolean
  has_valid_token: boolean
}

export interface LogoutResponse {
  message: string
}

export const sessionApi = baseApi.injectEndpoints({
  endpoints: (builder) => ({
    // Check if auth is enabled
    isAuthEnabled: builder.query<IsAuthEnabledResponse, void>({
      query: () => ({
        url: '/session/is-auth-enabled',
        method: 'GET',
      }),
    }),
    // Login endpoint
    login: builder.mutation<LoginResponse, LoginRequest>({
      query: (credentials) => ({
        url: '/session/login',
        method: 'POST',
        body: credentials,
      }),
      invalidatesTags: [],
    }),

    // Logout endpoint
    logout: builder.mutation<LogoutResponse, void>({
      query: () => ({
        url: '/session/logout',
        method: 'POST',
      }),
      // After logout, clear token and all cached data
      async onQueryStarted (arg, { queryFulfilled }) {
        try {
          await queryFulfilled
          // Clear the token from localStorage
          setAuthToken(null)
        } catch (error) {
          // Logout failed but clear token anyway for security
          setAuthToken(null)
        }
      },
      invalidatesTags: ['Config', 'Providers', 'Logs', 'VirtualKeys', 'Teams', 'Customers', 'Budgets', 'RateLimits'],
    }),
  }),
})

export const {
  useIsAuthEnabledQuery,
  useLoginMutation,
  useLogoutMutation,
} = sessionApi

