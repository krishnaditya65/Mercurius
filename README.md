# Mercurius

Unified retail + institutional trading platform. Codename **Mercurius**
(the Roman god of trade, commerce, communication, and speed — a
deliberate double meaning: this platform is about both commerce and
extreme low-latency execution).

Start with:

- [`docs/FEATURES.md`](./docs/FEATURES.md) — the full feature backlog,
  phased P0–P4
- [`docs/ARCHITECTURE.md`](./docs/ARCHITECTURE.md) — system design,
  latency/scale architecture, tech stack rationale
- [`docs/DOCUMENTATION.md`](./docs/DOCUMENTATION.md) — the living,
  synthesized reference for everything actually implemented: every
  service, every function, what's real vs. placeholder. Edited in place
  as code changes.
- [`docs/BUILD_LOG.md`](./docs/BUILD_LOG.md) — append-only chronological
  record of every build step. Never edited or reordered; only appended to.

This repo is currently a **scaffold**: every service builds/runs and
returns a health check, but the actual business logic (matching, risk,
ledger, quant math) is not yet implemented. See each service's own
`README.md` for its status and the `TODO`s marking where real logic goes.

## Repo layout

```
Mercurius/
├── services/
│   ├── matching-engine/   Rust  — order book core (Tier 0, not yet real)
│   ├── market-data/       Rust  — feed normalization + WS fan-out (Tier 1)
│   ├── oms-gateway/       Go    — order mgmt, risk checks, routing
│   ├── ledger/            Go    — double-entry accounting, settlement
│   ├── kyc-onboarding/    Go    — identity verification workflows
│   ├── backoffice/        Go    — admin/support API
│   ├── auth/              Go    — register/login, JWT, refresh-token rotation
│   └── quant-engine/      Python — Black-Scholes, GARCH, Sharpe, VaR
├── apps/
│   ├── web/                Next.js — retail web app
│   └── terminal/            Tauri + React — pro desktop terminal
├── libs/
│   └── proto/              Shared wire-format schemas (protobuf for now;
│                            Tier 0 will move to SBE/binary per ARCHITECTURE.md §3.5)
└── infra/
    ├── docker/              docker-compose for local dev infra
    └── ci/                  CI workflow definitions
```

## Local development

Prerequisites: `cargo`, `go` (1.22+), `node` (20+), `docker`.

```bash
# 1. Start local infra (Postgres, Redpanda, ClickHouse)
make infra-up

# 2. Build everything
make build

# 3. Run everything (each service in its own terminal, or via `make dev`)
make dev
```

See the `Makefile` for the full command list.

## Build order (matches FEATURES.md phasing)

This scaffold covers Phase 0 service boundaries. Do not build Phase 2+
logic (options chain, matching engine internals, FIX access) before
Phase 0/1 is real and tested: auth, ledger, single-instrument paper
trading, end-to-end order flow through the OMS.
