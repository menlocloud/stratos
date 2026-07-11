// Mock-mode flag + the tiny bits that must be importable from production code
// paths (lib/auth). Everything heavy (fixtures, handlers) lives behind the
// dynamic import in main.tsx so real builds don't carry it.
export const MOCK_ENABLED = import.meta.env.VITE_MOCK === "1"

export const mockProfile = {
  sub: "mock-user-0001",
  email: "dev@menlo.ai",
  name: "Dev User",
  preferred_username: "dev",
}

// Mimics the react-oidc-context state surface the app consumes
// (isLoading/isAuthenticated/user.profile/signin*/signout*).
export const mockAuthState = {
  isLoading: false,
  isAuthenticated: true,
  activeNavigator: undefined,
  error: undefined,
  user: {
    access_token: "mock-access-token",
    id_token: "mock-id-token",
    token_type: "Bearer",
    profile: mockProfile,
    expired: false,
  },
  signinRedirect: async () => {},
  signinSilent: async () => null,
  signoutRedirect: async () => {
    window.location.href = "/"
  },
  removeUser: async () => {},
  clearStaleState: async () => {},
  startSilentRenew: () => {},
  stopSilentRenew: () => {},
  revokeTokens: async () => {},
}
