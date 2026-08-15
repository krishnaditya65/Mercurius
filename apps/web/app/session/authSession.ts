// Mercurius / web — shared services/auth session storage.
//
// A bare localStorage key/JSON shape that genuinely needs to stay
// consistent across every page that reads it back (every page that talks
// to oms-gateway, backoffice, kyc-onboarding, or api-gateway now needs the
// bearer token this holds). This is NOT a shared fetch-client abstraction
// — each page still inlines its own `fetch` calls per this codebase's
// convention; this module only centralizes the localStorage read/write/
// clear so the key name and JSON shape can't drift between pages.
//
// Mirrors watchlist/page.tsx's localStorage convention: a single
// long, descriptive camelCase key, minted/read with a
// `typeof window === "undefined"` guard for Next.js SSR.

const AUTH_SESSION_LOCAL_STORAGE_KEY = "mercuriusAuthSession";

export type StoredSession = {
  accountIdentifier?: string;
  accessToken: string;
  refreshToken?: string;
  expiresInSeconds?: number;
};

/// Persists a session (post-login/register+login) to this browser's
/// localStorage. No-ops on the server.
export function saveSession(session: StoredSession): void {
  if (typeof window === "undefined") return;
  window.localStorage.setItem(AUTH_SESSION_LOCAL_STORAGE_KEY, JSON.stringify(session));
}

/// Reads back the persisted session, if any. Returns null on the server,
/// when nothing has been saved yet, or if the stored value can't be parsed.
export function loadSession(): StoredSession | null {
  if (typeof window === "undefined") return null;
  const rawValue = window.localStorage.getItem(AUTH_SESSION_LOCAL_STORAGE_KEY);
  if (!rawValue) return null;
  try {
    const parsed = JSON.parse(rawValue);
    if (parsed && typeof parsed.accessToken === "string") return parsed as StoredSession;
    return null;
  } catch {
    return null;
  }
}

/// Clears the persisted session (post-logout). No-ops on the server.
export function clearSession(): void {
  if (typeof window === "undefined") return;
  window.localStorage.removeItem(AUTH_SESSION_LOCAL_STORAGE_KEY);
}
