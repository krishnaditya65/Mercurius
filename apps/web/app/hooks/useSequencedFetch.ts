// Mercurius / web — shared "ignore stale response" sequence guard.
//
// NOT a shared fetch-client abstraction — every page still inlines its
// own `fetch` calls per this codebase's convention. This is just the one
// bit of bookkeeping (a per-request sequence number) that was about to be
// hand-copied identically into three different poll loops (the dashboard
// price chart, the home-screen live P&L widget, the watchlist page), so
// it's centralized here instead of drifting across call sites.
//
// Usage:
//
//   const startNextRequest = useSequencedFetch();
//
//   async function refresh() {
//     const isStillMostRecentRequest = startNextRequest();
//     const response = await fetch(...);
//     if (!isStillMostRecentRequest()) return; // a newer request already resolved/started
//     setState(await response.json());
//   }

import { useRef } from "react";

export function useSequencedFetch(): () => () => boolean {
  const mostRecentRequestSequenceNumber = useRef(0);

  return function startNextRequest(): () => boolean {
    const thisRequestSequenceNumber = ++mostRecentRequestSequenceNumber.current;
    return function isStillMostRecentRequest(): boolean {
      return thisRequestSequenceNumber === mostRecentRequestSequenceNumber.current;
    };
  };
}
