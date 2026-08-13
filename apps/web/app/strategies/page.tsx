"use client";

// Mercurius / web — Social/copy-trading: follow verified strategies.
//
// FEATURES.md §11 "[P3] Social/copy-trading: follow verified strategies
// or traders (opt-in, disclosed)". Talks to oms-gateway's REAL
// internal/strategyfollowing endpoints — a genuine, persisted (in
// memory) follow/unfollow relationship graph, gated to only the
// admin-verified strategies this page lists from `GET /strategies`.
//
// ============ SCOPE BOUNDARY — READ BEFORE EXTENDING ============
// This is opt-in FOLLOW/UNFOLLOW ONLY. There is NO order mirroring, NO
// automatic replication of a followed strategy's trades into your
// account, and NO auto-following of anything — following a strategy
// here has zero effect on your own orders. A real copy-trading engine
// (mirroring trades, position sizing, risk controls for the copier) is
// explicitly out of scope — see oms-gateway's
// internal/strategyfollowing package doc for the same statement.
// ==================================================================
//
// The "Admin: verify a strategy" panel below exists only so this page is
// exercisable end-to-end without a separate admin tool — a real build
// would gate that action behind whatever admin auth backoffice's own
// approval actions use (unauthenticated here, like most of this repo's
// endpoints today).
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

import { useEffect, useState } from "react";
import Link from "next/link";

const omsGatewayBaseUrl = process.env.NEXT_PUBLIC_OMS_GATEWAY_BASE_URL ?? "http://localhost:8081";

type VerifiedStrategy = {
  strategyIdentifier: string;
  displayName: string;
  description: string;
  followerCount: number;
};

export default function StrategiesPage() {
  const [accountIdentifier, setAccountIdentifier] = useState("acct-001");
  const [verifiedStrategies, setVerifiedStrategies] = useState<VerifiedStrategy[]>([]);
  const [followedStrategyIdentifiers, setFollowedStrategyIdentifiers] = useState<string[]>([]);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);
  const [isBusyStrategyIdentifier, setIsBusyStrategyIdentifier] = useState<string | null>(null);

  const [adminStrategyIdentifier, setAdminStrategyIdentifier] = useState("algo-1");
  const [adminDisplayName, setAdminDisplayName] = useState("Momentum Alpha");
  const [adminDescription, setAdminDescription] = useState("An illustrative momentum strategy, disclosed for demo purposes.");
  const [adminStatusMessage, setAdminStatusMessage] = useState<string | null>(null);

  async function refreshVerifiedStrategies() {
    setErrorMessage(null);
    try {
      const httpResponse = await fetch(`${omsGatewayBaseUrl}/strategies`);
      if (!httpResponse.ok) throw new Error(`HTTP ${httpResponse.status}`);
      const parsed: VerifiedStrategy[] = await httpResponse.json();
      setVerifiedStrategies(parsed);
    } catch (thrownError) {
      setErrorMessage(
        thrownError instanceof Error
          ? `Couldn't reach oms-gateway: ${thrownError.message}. Is it running on ${omsGatewayBaseUrl}?`
          : "Unknown error fetching verified strategies."
      );
    }
  }

  async function refreshFollowedStrategies() {
    try {
      const httpResponse = await fetch(
        `${omsGatewayBaseUrl}/strategies/following?accountId=${encodeURIComponent(accountIdentifier)}`
      );
      if (!httpResponse.ok) throw new Error(`HTTP ${httpResponse.status}`);
      const parsed: { followedStrategyIdentifiers: string[] } = await httpResponse.json();
      setFollowedStrategyIdentifiers(parsed.followedStrategyIdentifiers ?? []);
    } catch {
      // Non-fatal — the follow/unfollow buttons still work even if this
      // particular refresh fails; they'll just show a stale follow state
      // until the next successful refresh.
    }
  }

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    refreshVerifiedStrategies();
  }, []);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    refreshFollowedStrategies();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [accountIdentifier]);

  async function toggleFollow(strategyIdentifier: string, shouldFollowNotUnfollow: boolean) {
    setIsBusyStrategyIdentifier(strategyIdentifier);
    setErrorMessage(null);
    try {
      const httpResponse = await fetch(
        `${omsGatewayBaseUrl}/strategies/${shouldFollowNotUnfollow ? "follow" : "unfollow"}`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ accountIdentifier, strategyIdentifier }),
        }
      );
      if (!httpResponse.ok) {
        const bodyText = await httpResponse.text();
        throw new Error(bodyText || `HTTP ${httpResponse.status}`);
      }
      await Promise.all([refreshFollowedStrategies(), refreshVerifiedStrategies()]);
    } catch (thrownError) {
      setErrorMessage(thrownError instanceof Error ? thrownError.message : "Failed to update follow state.");
    } finally {
      setIsBusyStrategyIdentifier(null);
    }
  }

  async function verifyStrategyAsAdmin() {
    setAdminStatusMessage(null);
    try {
      const httpResponse = await fetch(`${omsGatewayBaseUrl}/strategies/admin/verify`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          strategyIdentifier: adminStrategyIdentifier,
          displayName: adminDisplayName,
          description: adminDescription,
        }),
      });
      if (!httpResponse.ok) {
        const bodyText = await httpResponse.text();
        throw new Error(bodyText || `HTTP ${httpResponse.status}`);
      }
      setAdminStatusMessage(`Verified "${adminStrategyIdentifier}" — now visible/followable below.`);
      await refreshVerifiedStrategies();
    } catch (thrownError) {
      setAdminStatusMessage(thrownError instanceof Error ? `Failed: ${thrownError.message}` : "Unknown error verifying strategy.");
    }
  }

  return (
    <main className="mx-auto flex max-w-3xl flex-col gap-8 p-8 font-sans">
      <div>
        <Link href="/" className="text-sm text-neutral-500 underline">
          ← Back to dashboard
        </Link>
        <h1 className="text-xl font-semibold">Follow verified strategies</h1>
        <p className="text-sm text-neutral-500">
          Opt-in follow/unfollow of admin-verified strategies only — <strong>no order mirroring, no automatic
          copying of trades</strong>. Following a strategy here has zero effect on your own orders. Backed by
          oms-gateway&apos;s real <code>internal/strategyfollowing</code> package.
        </p>
      </div>

      <label className="flex flex-col gap-1 text-sm">
        Your account
        <input
          className="w-64 rounded border px-3 py-2"
          value={accountIdentifier}
          onChange={(changeEvent) => setAccountIdentifier(changeEvent.target.value)}
        />
      </label>

      {errorMessage && <p className="text-sm text-red-600">{errorMessage}</p>}

      <section className="flex flex-col gap-3">
        <h2 className="text-lg font-medium">Verified strategies</h2>
        {verifiedStrategies.length === 0 ? (
          <p className="text-sm text-neutral-500">
            No verified strategies yet — use the admin panel below to verify one (e.g. &quot;algo-1&quot;, the same
            strategyIdentifier oms-gateway&apos;s order ticket / internal/algolimits can tag orders with).
          </p>
        ) : (
          <ul className="flex flex-col gap-3">
            {verifiedStrategies.map((strategy) => {
              const isFollowing = followedStrategyIdentifiers.includes(strategy.strategyIdentifier);
              const isBusy = isBusyStrategyIdentifier === strategy.strategyIdentifier;
              return (
                <li
                  key={strategy.strategyIdentifier}
                  className="flex items-center justify-between gap-4 rounded border border-neutral-200 p-4"
                >
                  <div>
                    <p className="font-medium">
                      {strategy.displayName} <span className="text-neutral-400">({strategy.strategyIdentifier})</span>
                    </p>
                    {strategy.description && <p className="text-sm text-neutral-500">{strategy.description}</p>}
                    <p className="text-xs text-neutral-400">{strategy.followerCount} follower(s)</p>
                  </div>
                  <button
                    type="button"
                    disabled={isBusy}
                    onClick={() => toggleFollow(strategy.strategyIdentifier, !isFollowing)}
                    className={`shrink-0 rounded px-4 py-2 text-sm disabled:opacity-50 ${
                      isFollowing ? "border border-neutral-300 bg-white" : "bg-black text-white"
                    }`}
                  >
                    {isBusy ? "…" : isFollowing ? "Unfollow" : "Follow"}
                  </button>
                </li>
              );
            })}
          </ul>
        )}
      </section>

      <section className="flex flex-col gap-3 rounded border border-dashed border-neutral-300 p-4">
        <h2 className="text-sm font-medium text-neutral-600">
          Admin: verify a strategy (demo-only panel, unauthenticated — see file header)
        </h2>
        <div className="flex flex-wrap items-end gap-3">
          <label className="flex flex-col gap-1 text-sm">
            strategyIdentifier
            <input
              className="rounded border px-3 py-2"
              value={adminStrategyIdentifier}
              onChange={(changeEvent) => setAdminStrategyIdentifier(changeEvent.target.value)}
            />
          </label>
          <label className="flex flex-col gap-1 text-sm">
            Display name
            <input
              className="rounded border px-3 py-2"
              value={adminDisplayName}
              onChange={(changeEvent) => setAdminDisplayName(changeEvent.target.value)}
            />
          </label>
          <label className="flex flex-col gap-1 text-sm">
            Description
            <input
              className="rounded border px-3 py-2"
              value={adminDescription}
              onChange={(changeEvent) => setAdminDescription(changeEvent.target.value)}
            />
          </label>
          <button className="rounded border px-3 py-2 text-sm" onClick={verifyStrategyAsAdmin} type="button">
            Verify
          </button>
        </div>
        {adminStatusMessage && <p className="text-xs text-neutral-500">{adminStatusMessage}</p>}
      </section>
    </main>
  );
}
