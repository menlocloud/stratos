// OIDC auth (Keycloak, authorization code + PKCE) via react-oidc-context.
// After the first sign-in the platform user is initialized with
// POST /user (form field id_token) — idempotent get-or-create.
import { useEffect, useRef } from "react"
import { AuthProvider, useAuth as useOidcAuth, type AuthProviderProps } from "react-oidc-context"
import { WebStorageStateStore } from "oidc-client-ts"
import { config } from "./config"
import { apiFetchEnvelope, setTokenProvider } from "./api"
import { MOCK_ENABLED, mockAuthState } from "@/mocks/enabled"

const oidcConfig: AuthProviderProps = {
  authority: config.authIssuer,
  client_id: config.authClientId,
  redirect_uri: window.location.origin + "/",
  post_logout_redirect_uri: window.location.origin + "/",
  scope: config.authScope,
  userStore: new WebStorageStateStore({ store: window.localStorage }),
  onSigninCallback: () => {
    // strip ?code&state from the URL after the redirect completes
    window.history.replaceState({}, document.title, window.location.pathname)
  },
}

export function StratosAuthProvider({ children }: { children: React.ReactNode }) {
  if (MOCK_ENABLED) return <>{children}</>
  return <AuthProvider {...oidcConfig}>{children}</AuthProvider>
}

// In mock mode there is no OIDC provider; useAuth resolves to a canned
// always-authenticated session (see src/mocks/).
export const useAuth: typeof useOidcAuth = MOCK_ENABLED
  ? (() => mockAuthState as unknown as ReturnType<typeof useOidcAuth>)
  : useOidcAuth

// Wires the bearer token into the API client + runs the one-time user init.
export function AuthBridge({ children }: { children: React.ReactNode }) {
  const auth = useAuth()
  const initialized = useRef(false)

  setTokenProvider(() => auth.user?.access_token)

  useEffect(() => {
    if (!auth.isAuthenticated || initialized.current) return
    initialized.current = true
    const idToken = auth.user?.id_token
    if (!idToken) return
    if (sessionStorage.getItem("stratos.userInit") === "1") return
    apiFetchEnvelope("/user", { method: "POST", form: { id_token: idToken } })
      .then(() => sessionStorage.setItem("stratos.userInit", "1"))
      .catch((e) => console.warn("user init failed", e))
  }, [auth.isAuthenticated, auth.user])

  return <>{children}</>
}
