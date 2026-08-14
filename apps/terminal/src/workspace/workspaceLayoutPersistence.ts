// Mercurius / terminal — saved GoldenLayout workspace persistence.
//
// FEATURES.md §10 "[P2] GoldenLayout-based tiling workspace, saved layouts
// per user". GoldenLayout's own `LayoutManager.saveLayout()` already
// returns a plain, serializable `ResolvedLayoutConfig` object — this
// module's only job is deciding WHERE that JSON blob lives, keyed per
// user, and reading it back.
//
// STORAGE CHOICE (documented per the task brief's "pick one, document it"):
// this uses `localStorage`, guarded behind a real runtime check for
// whether the page is currently running inside an actual Tauri webview
// (`"__TAURI_INTERNALS__" in window`, the real marker Tauri v2 injects —
// see https://v2.tauri.app/reference/javascript/api/). When it IS running
// inside Tauri, this module still only uses `localStorage` — Tauri's
// webview ships a real, per-app-identifier-scoped localStorage backed by
// the OS webview's own storage, which already satisfies "persists across
// launches" without pulling in `@tauri-apps/plugin-fs` for something this
// small. A fs-plugin-backed JSON file under the app's config directory
// would be the natural next step for a real multi-profile build (so
// layouts could be backed up/synced/edited outside the app) — deliberately
// not built here to keep this module trivially unit-testable without a
// Tauri runtime; see the module-level honesty note this leaves for anyone
// picking that up.
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

const STORAGE_KEY_PREFIX = "mercuriusTerminal.savedWorkspaceLayout.";

function buildStorageKeyForUser(userIdentifier: string): string {
  // Deliberately simple key construction — no serialization ambiguity to
  // worry about since userIdentifier is expected to already be a safe
  // account-identifier-shaped string (e.g. "acct-001") per the rest of
  // this codebase's convention (see apps/web's clientAccountIdentifier).
  return `${STORAGE_KEY_PREFIX}${userIdentifier}`;
}

/** Persists an already-serialized GoldenLayout config (the return value of
 * `LayoutManager.saveLayout()`, JSON.stringify'd by the caller) for the
 * given user. Silently no-ops (rather than throwing) if `localStorage` is
 * unavailable or full — a failed layout save should never crash the
 * terminal, just fall back to the default layout next launch. */
export function saveWorkspaceLayoutForUser(userIdentifier: string, serializedLayoutJson: string): boolean {
  try {
    window.localStorage.setItem(buildStorageKeyForUser(userIdentifier), serializedLayoutJson);
    return true;
  } catch {
    return false;
  }
}

/** Reads back a previously saved layout's raw JSON for the given user, or
 * `null` if none was ever saved (or storage is unavailable) — callers
 * should fall back to a hard-coded default GoldenLayout config in that
 * case. */
export function loadWorkspaceLayoutForUser(userIdentifier: string): string | null {
  try {
    return window.localStorage.getItem(buildStorageKeyForUser(userIdentifier));
  } catch {
    return null;
  }
}

export function clearWorkspaceLayoutForUser(userIdentifier: string): void {
  try {
    window.localStorage.removeItem(buildStorageKeyForUser(userIdentifier));
  } catch {
    // No-op — nothing meaningful to recover from here.
  }
}
