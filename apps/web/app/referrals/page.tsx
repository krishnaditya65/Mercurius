"use client";

// Mercurius / web — Referral & rewards program (FEATURES.md §14, item 3),
// wired against backoffice's real `internal/referralrewards` endpoints on
// :8084:
//
//   POST /referral-rewards/generate-code     {accountIdentifier} -> {accountIdentifier, referralCode}  (idempotent)
//   POST /referral-rewards/record-referral    {referralCode, referredAccountIdentifier}
//   POST /referral-rewards/check-and-qualify  {referredAccountIdentifier}
//   GET  /referral-rewards/status?accountId=...
//   GET  /referral-rewards/referrals?accountId=...
//
// The qualifying event is a REAL first trade (checked live against
// oms-gateway's positions) and the reward is a REAL cash credit posted
// to ledger's actual /journal-entries API — see backoffice's README for
// the full verified-live transcript this page's flows mirror.
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

import { useState } from "react";
import Link from "next/link";
import { loadSession } from "../session/authSession";

const backofficeBaseUrl = process.env.NEXT_PUBLIC_BACKOFFICE_BASE_URL ?? "http://localhost:8084";
const LOG_IN_FIRST_MESSAGE = "Log in first from the dashboard's Account panel to use this.";

type ReferralLink = {
  referralCode: string;
  referrerAccountIdentifier: string;
  referredAccountIdentifier: string;
  status: string;
  createdAtTime: string;
  rewardedAtTime?: string;
};

export default function ReferralsPage() {
  const [accountIdentifier, setAccountIdentifier] = useState("acct-001");
  const [referralCode, setReferralCode] = useState<string | null>(null);
  const [isGeneratingCode, setIsGeneratingCode] = useState(false);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);

  const [myReferrals, setMyReferrals] = useState<ReferralLink[]>([]);

  const [referredAccountIdentifier, setReferredAccountIdentifier] = useState("acct-002");
  const [codeToRedeem, setCodeToRedeem] = useState("");
  const [recordStatusMessage, setRecordStatusMessage] = useState<string | null>(null);

  const [qualifyAccountIdentifier, setQualifyAccountIdentifier] = useState("acct-002");
  const [qualifyResultMessage, setQualifyResultMessage] = useState<string | null>(null);
  const [myReferralStatus, setMyReferralStatus] = useState<{ found: boolean; link?: ReferralLink } | null>(null);

  async function generateMyReferralCode() {
    setErrorMessage(null);
    const storedSession = loadSession();
    if (!storedSession?.accessToken) {
      setErrorMessage(LOG_IN_FIRST_MESSAGE);
      return;
    }
    setIsGeneratingCode(true);
    try {
      const httpResponse = await fetch(`${backofficeBaseUrl}/referral-rewards/generate-code`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Authorization: `Bearer ${storedSession.accessToken}`,
        },
        body: JSON.stringify({ accountIdentifier }),
      });
      if (!httpResponse.ok) throw new Error(`HTTP ${httpResponse.status}`);
      const parsed: { accountIdentifier: string; referralCode: string } = await httpResponse.json();
      setReferralCode(parsed.referralCode);
    } catch (thrownError) {
      setErrorMessage(
        thrownError instanceof Error
          ? `Couldn't reach backoffice: ${thrownError.message}. Is it running on ${backofficeBaseUrl}?`
          : "Unknown error generating referral code."
      );
    } finally {
      setIsGeneratingCode(false);
    }
  }

  async function refreshMyReferrals() {
    const storedSession = loadSession();
    if (!storedSession?.accessToken) {
      setErrorMessage(LOG_IN_FIRST_MESSAGE);
      return;
    }
    try {
      const httpResponse = await fetch(
        `${backofficeBaseUrl}/referral-rewards/referrals?accountId=${encodeURIComponent(accountIdentifier)}`,
        { headers: { Authorization: `Bearer ${storedSession.accessToken}` } }
      );
      if (!httpResponse.ok) throw new Error(`HTTP ${httpResponse.status}`);
      const parsed: { accountIdentifier: string; referrals: ReferralLink[] | null } = await httpResponse.json();
      setMyReferrals(parsed.referrals ?? []);
    } catch (thrownError) {
      setErrorMessage(thrownError instanceof Error ? thrownError.message : "Failed to fetch referrals.");
    }
  }

  async function refreshMyReferralStatus() {
    const storedSession = loadSession();
    if (!storedSession?.accessToken) {
      setErrorMessage(LOG_IN_FIRST_MESSAGE);
      return;
    }
    try {
      const httpResponse = await fetch(
        `${backofficeBaseUrl}/referral-rewards/status?accountId=${encodeURIComponent(qualifyAccountIdentifier)}`,
        { headers: { Authorization: `Bearer ${storedSession.accessToken}` } }
      );
      const bodyText = await httpResponse.text();
      if (!httpResponse.ok) {
        // A 404-shaped "no referral link found" is a legitimate, expected
        // state for an account that hasn't been referred — not an error.
        setMyReferralStatus({ found: false });
        return;
      }
      setMyReferralStatus({ found: true, link: JSON.parse(bodyText) });
    } catch (thrownError) {
      setErrorMessage(thrownError instanceof Error ? thrownError.message : "Failed to fetch referral status.");
    }
  }

  async function recordReferral() {
    setRecordStatusMessage(null);
    const storedSession = loadSession();
    if (!storedSession?.accessToken) {
      setRecordStatusMessage(LOG_IN_FIRST_MESSAGE);
      return;
    }
    try {
      const httpResponse = await fetch(`${backofficeBaseUrl}/referral-rewards/record-referral`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Authorization: `Bearer ${storedSession.accessToken}`,
        },
        body: JSON.stringify({ referralCode: codeToRedeem, referredAccountIdentifier }),
      });
      const bodyText = await httpResponse.text();
      if (!httpResponse.ok) throw new Error(bodyText || `HTTP ${httpResponse.status}`);
      const parsed: ReferralLink = JSON.parse(bodyText);
      setRecordStatusMessage(`Recorded: ${parsed.referredAccountIdentifier} referred by ${parsed.referrerAccountIdentifier} (${parsed.status}).`);
      await refreshMyReferrals();
    } catch (thrownError) {
      setRecordStatusMessage(
        thrownError instanceof Error ? `Failed: ${thrownError.message}` : "Unknown error recording referral."
      );
    }
  }

  async function checkAndQualify() {
    setQualifyResultMessage(null);
    const storedSession = loadSession();
    if (!storedSession?.accessToken) {
      setQualifyResultMessage(LOG_IN_FIRST_MESSAGE);
      return;
    }
    try {
      const httpResponse = await fetch(`${backofficeBaseUrl}/referral-rewards/check-and-qualify`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Authorization: `Bearer ${storedSession.accessToken}`,
        },
        body: JSON.stringify({ referredAccountIdentifier: qualifyAccountIdentifier }),
      });
      const bodyText = await httpResponse.text();
      if (!httpResponse.ok) throw new Error(bodyText || `HTTP ${httpResponse.status}`);
      const parsed: { qualified: boolean; reason?: string; alreadyRewarded?: boolean } = JSON.parse(bodyText);
      setQualifyResultMessage(
        parsed.qualified
          ? "Qualified — referrer credited ₹100.00 in ledger."
          : parsed.alreadyRewarded
            ? "Already rewarded previously (idempotent no-op)."
            : `Not yet qualified: ${parsed.reason ?? "no reason given"}`
      );
      await Promise.all([refreshMyReferrals(), refreshMyReferralStatus()]);
    } catch (thrownError) {
      setQualifyResultMessage(
        thrownError instanceof Error ? `Failed: ${thrownError.message}` : "Unknown error checking qualification."
      );
    }
  }

  return (
    <main className="mx-auto flex max-w-2xl flex-col gap-8 p-8 font-sans">
      <div>
        <Link href="/" className="text-sm text-neutral-500 underline">
          ← Back to dashboard
        </Link>
        <h1 className="text-xl font-semibold">Referrals &amp; rewards</h1>
        <p className="text-sm text-neutral-500">
          Backed by backoffice&apos;s real <code>internal/referralrewards</code> package on :8084 — a real,
          idempotent referral code, real referred-account tracking, and a real cash reward genuinely credited via
          ledger&apos;s <code>/journal-entries</code> API once the referred account completes a real first trade.
        </p>
      </div>

      {errorMessage && <p className="text-sm text-red-600">{errorMessage}</p>}

      <section className="flex flex-col gap-3 rounded border border-neutral-200 p-4">
        <h2 className="text-lg font-medium">Your referral code</h2>
        <label className="flex flex-col gap-1 text-sm">
          Your account
          <input
            className="w-48 rounded border px-3 py-2"
            value={accountIdentifier}
            onChange={(changeEvent) => setAccountIdentifier(changeEvent.target.value)}
          />
        </label>
        <div className="flex items-center gap-3">
          <button
            type="button"
            disabled={isGeneratingCode}
            onClick={generateMyReferralCode}
            className="rounded bg-black px-4 py-2 text-sm text-white disabled:opacity-50"
          >
            {isGeneratingCode ? "Generating…" : "Get / generate my code"}
          </button>
          {referralCode && (
            <p className="text-lg font-mono font-semibold">
              {referralCode} <span className="text-xs font-sans text-neutral-500">(share this)</span>
            </p>
          )}
        </div>
      </section>

      <section className="flex flex-col gap-3 rounded border border-neutral-200 p-4">
        <h2 className="text-lg font-medium">Redeem a referral code (as the new/referred account)</h2>
        <div className="flex flex-wrap gap-3">
          <label className="flex flex-col gap-1 text-sm">
            Referral code
            <input
              className="w-40 rounded border px-3 py-2"
              placeholder="MERC-XXXXXX"
              value={codeToRedeem}
              onChange={(changeEvent) => setCodeToRedeem(changeEvent.target.value)}
            />
          </label>
          <label className="flex flex-col gap-1 text-sm">
            Referred account
            <input
              className="w-40 rounded border px-3 py-2"
              value={referredAccountIdentifier}
              onChange={(changeEvent) => setReferredAccountIdentifier(changeEvent.target.value)}
            />
          </label>
          <button type="button" className="self-end rounded border px-4 py-2 text-sm" onClick={recordReferral}>
            Record referral
          </button>
        </div>
        {recordStatusMessage && <p className="text-sm text-neutral-600">{recordStatusMessage}</p>}
      </section>

      <section className="flex flex-col gap-3 rounded border border-neutral-200 p-4">
        <h2 className="text-lg font-medium">Check qualification (first real trade → reward)</h2>
        <p className="text-xs text-neutral-500">
          Qualifies the moment the referred account&apos;s real position book (via oms-gateway) is non-empty — place
          a real order for that account elsewhere in this app, then check here.
        </p>
        <div className="flex flex-wrap gap-3">
          <label className="flex flex-col gap-1 text-sm">
            Referred account
            <input
              className="w-40 rounded border px-3 py-2"
              value={qualifyAccountIdentifier}
              onChange={(changeEvent) => setQualifyAccountIdentifier(changeEvent.target.value)}
            />
          </label>
          <button type="button" className="self-end rounded border px-4 py-2 text-sm" onClick={checkAndQualify}>
            Check &amp; qualify
          </button>
          <button type="button" className="self-end rounded border px-4 py-2 text-sm" onClick={refreshMyReferralStatus}>
            Load status
          </button>
        </div>
        {qualifyResultMessage && <p className="text-sm text-neutral-600">{qualifyResultMessage}</p>}
        {myReferralStatus && (
          <p className="text-sm">
            {myReferralStatus.found && myReferralStatus.link
              ? `Status: ${myReferralStatus.link.status} (referred by ${myReferralStatus.link.referrerAccountIdentifier})`
              : "No referral link found for this account."}
          </p>
        )}
      </section>

      <section className="flex flex-col gap-3 rounded border border-neutral-200 p-4">
        <div className="flex items-center justify-between">
          <h2 className="text-lg font-medium">Referrals you&apos;ve made</h2>
          <button type="button" className="rounded border px-3 py-2 text-sm" onClick={refreshMyReferrals}>
            Refresh
          </button>
        </div>
        {myReferrals.length === 0 ? (
          <p className="text-sm text-neutral-500">No referrals loaded yet.</p>
        ) : (
          <ul className="flex flex-col gap-2 text-sm">
            {myReferrals.map((referral) => (
              <li key={referral.referredAccountIdentifier} className="rounded border border-neutral-100 p-2">
                {referral.referredAccountIdentifier} — <span className="font-medium">{referral.status}</span>
                {referral.rewardedAtTime && ` (rewarded ${new Date(referral.rewardedAtTime).toLocaleString()})`}
              </li>
            ))}
          </ul>
        )}
      </section>
    </main>
  );
}
