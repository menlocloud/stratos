// OIDC auth for the admin console — Keycloak master realm, client
// `stratos-admin`. Any master-realm user is auto-provisioned SUPER_ADMIN by
// the API; /admin/me returns role + granted permissions.
import { AuthProvider, useAuth as useOidcAuth, type AuthProviderProps } from "react-oidc-context"
import { WebStorageStateStore } from "oidc-client-ts"
import { useQuery } from "@tanstack/react-query"
import { config } from "./config"
import { apiFetch, setTokenProvider } from "./api"
import { MOCK_ENABLED, mockAuthState } from "@/mocks/enabled"

const oidcConfig: AuthProviderProps = {
  authority: config.authIssuer,
  client_id: config.authClientId,
  redirect_uri: window.location.origin + "/",
  post_logout_redirect_uri: window.location.origin + "/",
  scope: config.authScope,
  userStore: new WebStorageStateStore({ store: window.localStorage }),
  onSigninCallback: () => {
    window.history.replaceState({}, document.title, window.location.pathname)
  },
}

export function StratosAuthProvider({ children }: { children: React.ReactNode }) {
  if (MOCK_ENABLED) return <>{children}</>
  return <AuthProvider {...oidcConfig}>{children}</AuthProvider>
}

// In mock mode there is no OIDC provider; useAuth resolves to a canned
// always-authenticated master-realm session (see src/mocks/).
export const useAuth: typeof useOidcAuth = MOCK_ENABLED
  ? (() => mockAuthState as unknown as ReturnType<typeof useOidcAuth>)
  : useOidcAuth

export function AuthBridge({ children }: { children: React.ReactNode }) {
  const auth = useAuth()
  setTokenProvider(() => auth.user?.access_token)
  return <>{children}</>
}

export type AdminMe = {
  sub?: string
  email?: string
  firstName?: string
  lastName?: string
  role?: string
  permissions?: string[]
}

export function useAdminMe() {
  const auth = useAuth()
  return useQuery({
    queryKey: ["admin-me"],
    queryFn: () => apiFetch<AdminMe>("/admin/me"),
    enabled: auth.isAuthenticated,
  })
}
