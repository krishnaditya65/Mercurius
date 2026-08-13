# web (retail app)

Skeleton — see `FEATURES.md` §11 in the repo root for the full retail app
scope (watchlists, MF/SIP flows, options chain, alerts, etc.).

## Status: one real page

`app/page.tsx` is a working order ticket that POSTs directly to
`oms-gateway`'s `/orders/submit` and renders its
`humanReadableRejectionReason` on failure — this proves the FEATURES.md
§21 plain-language-rejection differentiator end-to-end from a real
browser client, not just from `curl`.

Everything else (auth, dashboard, portfolio, MF investing, SIPs,
watchlists) does not exist yet.

## Run it

```bash
npm install
npm run dev       # http://localhost:3000

# in another terminal, so the order ticket has something to talk to:
cd ../../services/oms-gateway && go run ./cmd/server
```

Set `NEXT_PUBLIC_OMS_GATEWAY_BASE_URL` if oms-gateway isn't at the
default `http://localhost:8081`.
