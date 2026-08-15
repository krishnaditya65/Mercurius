"use client";

// Mercurius / web — Developer API key management (FEATURES.md §18 item
// 8), wired against api-gateway's real `internal/apikeymanager` endpoints
// on :8089:
//
//   POST /developer/api-keys
//     body: {accountIdentifier, tenantIdentifier?, rateLimitTier, isSandboxKey}
//     -> 201 with the full IssuedApiKey JSON (including the raw key value)
//   GET  /developer/api-keys?accountIdentifier=...
//     -> IssuedApiKey[]
//   POST /developer/api-keys/revoke
//     body: {apiKeyValue}
//     -> {wasRevoked: true} on 200, 404 if the key doesn't exist
//
// The raw key value is shown prominently once after issuance, the usual
// "copy this now" API-key UX convention — but this is a UI-only
// convention on this page, NOT a server-side guarantee: `GET
// /developer/api-keys` genuinely returns every issued key's raw
// `apiKeyValue` again on every subsequent call (see apiKeyManager.go's
// `IssuedApiKey` struct and its own TODO acknowledging real keys should
// be stored hashed, not in plaintext). Don't mistake the "shown once"
// framing here for the backend actually withholding it afterward.
//
// KNOWN GAP, found while building this page: api-gateway's
// cmd/server/main.go has NO CORS middleware anywhere (grepped for
// "Access-Control"/"Cors" — zero matches), unlike oms-gateway's
// `withPermissiveCorsForDevelopment` or quant-engine's permissive CORS
// header. A real browser running this page against a real api-gateway
// on a different origin will have every fetch below blocked by the
// browser's CORS policy — curl/server-to-server calls are unaffected.
// See docs/BUILD_LOG.md for how this was verified (curl-only, not
// browser-live) and docs/DOCUMENTATION.md's api-gateway section for the
// gap recorded there too.
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

import { useState } from "react";
import Link from "next/link";
import { loadSession } from "../session/authSession";

const apiGatewayBaseUrl = process.env.NEXT_PUBLIC_API_GATEWAY_BASE_URL ?? "http://localhost:8089";
const LOG_IN_FIRST_MESSAGE = "Log in first from the dashboard's Account panel to use this.";

type IssuedApiKey = {
  apiKeyValue: string;
  accountIdentifier: string;
  tenantIdentifier?: string;
  rateLimitTier: string;
  isSandboxKey: boolean;
  issuedAtTime: string;
  isRevoked: boolean;
  revokedAtTime?: string;
};

const RATE_LIMIT_TIERS = ["retail", "institutional", "sandbox"];

export default function DeveloperApiPage() {
  const [accountIdentifier, setAccountIdentifier] = useState("acct-001");
  const [tenantIdentifier, setTenantIdentifier] = useState("");
  const [rateLimitTier, setRateLimitTier] = useState("retail");
  const [isSandboxKey, setIsSandboxKey] = useState(false);

  const [issuedKeys, setIssuedKeys] = useState<IssuedApiKey[]>([]);
  const [justIssuedKey, setJustIssuedKey] = useState<IssuedApiKey | null>(null);

  const [isIssuing, setIsIssuing] = useState(false);
  const [isListing, setIsListing] = useState(false);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);
  const [statusMessage, setStatusMessage] = useState<string | null>(null);

  async function fetchKeysForAccount() {
    setErrorMessage(null);
    const storedSession = loadSession();
    if (!storedSession?.accessToken) {
      setErrorMessage(LOG_IN_FIRST_MESSAGE);
      return;
    }
    setIsListing(true);
    try {
      const httpResponse = await fetch(
        `${apiGatewayBaseUrl}/developer/api-keys?accountIdentifier=${encodeURIComponent(accountIdentifier)}`,
        { headers: { Authorization: `Bearer ${storedSession.accessToken}` } }
      );
      if (!httpResponse.ok) throw new Error(`HTTP ${httpResponse.status}`);
      const parsedKeys: IssuedApiKey[] = await httpResponse.json();
      setIssuedKeys(parsedKeys ?? []);
    } catch (thrownError) {
      setErrorMessage(
        thrownError instanceof Error
          ? `Couldn't reach api-gateway: ${thrownError.message}. Is it running on ${apiGatewayBaseUrl}? (Also see this page's file-header comment about api-gateway's missing CORS support — a real browser fetch may be blocked even when the server is up.)`
          : "Unknown error fetching keys."
      );
    } finally {
      setIsListing(false);
    }
  }

  async function issueApiKey() {
    setErrorMessage(null);
    setStatusMessage(null);
    setJustIssuedKey(null);
    const storedSession = loadSession();
    if (!storedSession?.accessToken) {
      setErrorMessage(LOG_IN_FIRST_MESSAGE);
      return;
    }
    setIsIssuing(true);
    try {
      const httpResponse = await fetch(`${apiGatewayBaseUrl}/developer/api-keys`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Authorization: `Bearer ${storedSession.accessToken}`,
        },
        body: JSON.stringify({
          accountIdentifier,
          tenantIdentifier: tenantIdentifier || undefined,
          rateLimitTier,
          isSandboxKey,
        }),
      });
      const bodyText = await httpResponse.text();
      if (!httpResponse.ok) throw new Error(bodyText || `HTTP ${httpResponse.status}`);
      const issued: IssuedApiKey = JSON.parse(bodyText);
      setJustIssuedKey(issued);
      await fetchKeysForAccount();
    } catch (thrownError) {
      setErrorMessage(
        thrownError instanceof Error
          ? `Couldn't reach api-gateway: ${thrownError.message}. Is it running on ${apiGatewayBaseUrl}?`
          : "Unknown error issuing key."
      );
    } finally {
      setIsIssuing(false);
    }
  }

  async function revokeApiKey(apiKeyValue: string) {
    setErrorMessage(null);
    setStatusMessage(null);
    const storedSession = loadSession();
    if (!storedSession?.accessToken) {
      setErrorMessage(LOG_IN_FIRST_MESSAGE);
      return;
    }
    try {
      const httpResponse = await fetch(`${apiGatewayBaseUrl}/developer/api-keys/revoke`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Authorization: `Bearer ${storedSession.accessToken}`,
        },
        body: JSON.stringify({ apiKeyValue }),
      });
      if (httpResponse.status === 404) {
        setStatusMessage("Revoke failed: no such API key (already revoked, or never existed).");
        return;
      }
      if (!httpResponse.ok) throw new Error(`HTTP ${httpResponse.status}`);
      setStatusMessage("Key revoked.");
      await fetchKeysForAccount();
    } catch (thrownError) {
      setErrorMessage(
        thrownError instanceof Error ? thrownError.message : "Unknown error revoking key."
      );
    }
  }

  return (
    <main className="mx-auto flex max-w-3xl flex-col gap-8 p-8 font-sans">
      <div>
        <Link href="/" className="text-sm text-neutral-500 underline">
          ← Back to dashboard
        </Link>
        <h1 className="text-xl font-semibold">Developer API keys</h1>
        <p className="text-sm text-neutral-500">
          Backed by api-gateway&apos;s real <code>internal/apikeymanager</code> on :8089 — real key issuance,
          listing, and revocation. api-gateway has no CORS middleware today (see this page&apos;s file-header
          comment) — a real browser fetch from a different origin may be blocked even while the server itself is
          up and reachable via curl.
        </p>
      </div>

      {errorMessage && <p className="text-sm text-red-600">{errorMessage}</p>}
      {statusMessage && <p className="text-sm text-neutral-600">{statusMessage}</p>}

      <section className="flex flex-col gap-3 rounded border border-neutral-200 p-4">
        <h2 className="text-lg font-medium">Account</h2>
        <div className="flex items-end gap-3">
          <label className="flex flex-col gap-1 text-sm">
            Account identifier
            <input
              className="rounded border px-3 py-2"
              value={accountIdentifier}
              onChange={(e) => setAccountIdentifier(e.target.value)}
            />
          </label>
          <button
            type="button"
            className="rounded border px-3 py-2 text-sm"
            onClick={fetchKeysForAccount}
            disabled={isListing}
          >
            {isListing ? "Fetching…" : "Fetch keys"}
          </button>
        </div>
      </section>

      <section className="flex flex-col gap-3 rounded border border-neutral-200 p-4">
        <h2 className="text-lg font-medium">Issue a new key</h2>
        <div className="flex flex-wrap items-end gap-3">
          <label className="flex flex-col gap-1 text-sm">
            Tenant identifier (optional)
            <input
              className="rounded border px-3 py-2"
              value={tenantIdentifier}
              onChange={(e) => setTenantIdentifier(e.target.value)}
            />
          </label>
          <label className="flex flex-col gap-1 text-sm">
            Rate-limit tier
            <select
              className="rounded border px-3 py-2"
              value={rateLimitTier}
              onChange={(e) => setRateLimitTier(e.target.value)}
            >
              {RATE_LIMIT_TIERS.map((tier) => (
                <option key={tier} value={tier}>
                  {tier}
                </option>
              ))}
            </select>
          </label>
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={isSandboxKey} onChange={(e) => setIsSandboxKey(e.target.checked)} />
            Sandbox key
          </label>
          <button
            type="button"
            className="rounded bg-black px-4 py-2 text-sm text-white disabled:opacity-50"
            onClick={issueApiKey}
            disabled={isIssuing}
          >
            {isIssuing ? "Issuing…" : "Issue key"}
          </button>
        </div>
        <p className="text-xs text-neutral-500">
          Sandbox keys are automatically issued at the sandbox rate-limit tier server-side, regardless of the tier
          selected above (see <code>apikeymanager.IssueApiKey</code>).
        </p>
      </section>

      {justIssuedKey && (
        <section className="flex flex-col gap-2 rounded border border-amber-400 bg-amber-50 p-4 text-sm">
          <p className="font-semibold text-amber-800">
            Copy this key now — this page only shows it prominently once, as a UI convention. The server itself
            does NOT enforce one-time display: {`GET /developer/api-keys`} will return this same raw value again on
            every future call.
          </p>
          <p className="break-all rounded bg-white p-2 font-mono text-xs">{justIssuedKey.apiKeyValue}</p>
        </section>
      )}

      <section className="flex flex-col gap-3 rounded border border-neutral-200 p-4">
        <h2 className="text-lg font-medium">Keys for this account</h2>
        {issuedKeys.length === 0 ? (
          <p className="text-sm text-neutral-500">No keys fetched yet — click &quot;Fetch keys&quot; above.</p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[700px] border-collapse text-xs">
              <thead>
                <tr className="bg-neutral-50 text-left">
                  <th className="p-2">Key</th>
                  <th className="p-2">Tenant</th>
                  <th className="p-2">Tier</th>
                  <th className="p-2">Sandbox</th>
                  <th className="p-2">Issued</th>
                  <th className="p-2">Status</th>
                  <th className="p-2"></th>
                </tr>
              </thead>
              <tbody>
                {issuedKeys.map((key) => (
                  <tr key={key.apiKeyValue} className="border-t border-neutral-200">
                    <td className="max-w-[220px] truncate p-2 font-mono" title={key.apiKeyValue}>
                      {key.apiKeyValue}
                    </td>
                    <td className="p-2">{key.tenantIdentifier || "—"}</td>
                    <td className="p-2">{key.rateLimitTier}</td>
                    <td className="p-2">{key.isSandboxKey ? "yes" : "no"}</td>
                    <td className="p-2">{new Date(key.issuedAtTime).toLocaleString()}</td>
                    <td className="p-2">{key.isRevoked ? "revoked" : "active"}</td>
                    <td className="p-2">
                      <button
                        type="button"
                        className="rounded border px-2 py-1 text-red-600 disabled:opacity-50"
                        disabled={key.isRevoked}
                        onClick={() => revokeApiKey(key.apiKeyValue)}
                      >
                        Revoke
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </main>
  );
}
