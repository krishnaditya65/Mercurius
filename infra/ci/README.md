# CI

The actual, executable CI workflow lives at
`.github/workflows/ci.yml` — GitHub Actions only runs workflows from that
exact repo-root path, so it can't live under `infra/ci/` directly and
still function. This directory holds CI-related notes and any
non-GitHub-Actions tooling (e.g. local pre-commit hook config, if/when
added) that isn't itself a workflow file.

## What `ci.yml` currently does

One job per service (not one shared matrix job), so a break in one
language's build never blocks feedback on an unrelated service:

- `matching-engine`, `market-data` — `cargo build` + `cargo test`
- `oms-gateway`, `ledger`, `kyc-onboarding`, `backoffice`, `auth` —
  `gofmt` check, `go vet`, `go build`, `go test` (`auth` additionally
  runs with `-race`, given its concurrency-heavy `internal/sessionstore`
  package). `kyc-onboarding`/`backoffice` only ran `go build` until this
  build — their real test suites (`internal/kycstate`,
  `internal/accountcontrol`) existed in the repo but the CI job never
  grew a `go test` step to actually run them; fixed alongside adding the
  `auth` job.
- `quant-engine` — `pip install -e ".[dev]"` + `pytest`
- `web` — `tsc --noEmit` + `npm run build`

## Not yet covered

- `apps/terminal` — not scaffolded yet (see its own README)
- No integration/end-to-end job — nothing in CI currently proves two
  services talk to each other correctly, because they don't talk to each
  other yet (see `DOCUMENTATION.md`'s cross-cutting limitations section)
- No deployment/CD stage
