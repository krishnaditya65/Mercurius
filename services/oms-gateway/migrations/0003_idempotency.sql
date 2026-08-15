-- Real Postgres schema for internal/idempotency's completed-response
-- cache. See docs/BUILD_LOG.md's Postgres-persistence entry: this
-- closes TWO previously-documented gaps at once — the store not
-- surviving a restart, AND its previously-unbounded in-memory growth
-- (expires_at + a cleanup query below bound it for real).
--
-- Only COMPLETED responses are written here — the in-process
-- claim/await concurrency mechanism (internal/idempotency's
-- claimedKeyEntry/doneChannel) stays exactly as-is and in-memory-only;
-- it is fundamentally a single-process primitive (a Go channel) and
-- cannot be meaningfully distributed across processes via a Postgres
-- row without a much larger redesign (listen/notify or polling) that is
-- out of scope for this pass. What Postgres adds here is durability of
-- the FINAL answer for a key that already completed, so a replayed
-- request after a restart gets the identical cached response instead of
-- re-executing the order a second time.
CREATE TABLE IF NOT EXISTS idempotency_responses (
    idempotency_key       TEXT PRIMARY KEY,
    response_json         JSONB NOT NULL,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at            TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_idempotency_responses_expires_at
    ON idempotency_responses (expires_at);
