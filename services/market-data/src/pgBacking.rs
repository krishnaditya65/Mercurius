// Shared real-Postgres connection helper for watchlist.rs's
// WatchlistStore and pricealerts.rs's PriceAlertStore — see
// docs/BUILD_LOG.md's Postgres-persistence entry.
//
// DESIGN NOTE (the one genuinely awkward part of this pass): every
// existing method on WatchlistStore/PriceAlertStore is a plain
// SYNCHRONOUS `fn(&self, ...)` — callable from httpQueryServer.rs's
// hand-rolled, thread-per-connection (NOT async/tokio) HTTP server, and
// from main.rs's synchronous ingestion loop. tokio-postgres is
// necessarily async. Rather than making every call site async (which
// would mean rewriting httpQueryServer.rs onto a real async HTTP
// framework — real, separate, much larger work, out of scope for this
// pass), each Postgres-backed store owns its OWN small dedicated
// `tokio::runtime::Runtime` and every method just calls
// `runtime.block_on(...)` around the real async tokio-postgres call —
// this preserves every EXISTING method's signature byte-for-byte
// (interface-preserving, matching this pass's Go-side pattern), at the
// cost of one blocking hop per call. Fine for this skeleton's traffic
// volume; a real high-throughput build would want either a fully async
// HTTP layer or a bounded worker-queue instead of blocking each caller
// thread on a fresh runtime hop.
#![allow(non_snake_case)]

use std::str::FromStr;

use tokio_postgres::{Client, Config, NoTls};

/// One embedded migration file, applied at construction — see
/// migrations/0001_watchlist_and_pricealerts.sql's own header comment
/// for the "idempotent DDL, no tracking table" convention shared with
/// services/ledger and services/oms-gateway's Go migrations.
const MIGRATION_SQL: &str = include_str!("../migrations/0001_watchlist_and_pricealerts.sql");

pub struct PgBacking {
    runtime: tokio::runtime::Runtime,
    client: Client,
}

impl PgBacking {
    /// Connects to postgresDsn, spawns the tokio-postgres connection
    /// driver task, and applies every migration. Returns an error
    /// (never panics) if Postgres is unreachable — callers (main.rs)
    /// decide whether to fall back to an in-memory store.
    pub fn connect(postgresDsn: &str) -> Result<Self, String> {
        let runtime = tokio::runtime::Builder::new_multi_thread()
            .worker_threads(2)
            .enable_all()
            .build()
            .map_err(|error| format!("pgBacking: failed to build tokio runtime: {error}"))?;

        // Postgres has no `CREATE DATABASE IF NOT EXISTS` — the compose
        // Postgres (infra/docker/docker-compose.yml) only provisions
        // `ledger` via POSTGRES_DB, so `marketdata` does not exist on a
        // fresh stack. Same real handling as oms-gateway's Go
        // ensureTargetDatabaseExists: connect to the SAME server's
        // admin `postgres` database first, check pg_database, and issue
        // a real CREATE DATABASE if missing — idempotent by
        // construction (checks first), safe on every startup.
        runtime.block_on(ensureTargetDatabaseExists(postgresDsn))?;

        let (client, connection) = runtime
            .block_on(tokio_postgres::connect(postgresDsn, NoTls))
            .map_err(|error| format!("pgBacking: connect: {error}"))?;

        // The connection object drives the actual TCP I/O — without
        // polling it on a background task, no query ever actually
        // completes. Errors here (e.g. the connection dropping) are
        // logged, not propagated — matches tokio-postgres's own
        // documented usage pattern.
        runtime.spawn(async move {
            if let Err(connectionError) = connection.await {
                eprintln!("pgBacking: connection error: {connectionError}");
            }
        });

        runtime
            .block_on(client.batch_execute(MIGRATION_SQL))
            .map_err(|error| format!("pgBacking: migrate: {error}"))?;

        Ok(PgBacking { runtime, client })
    }

    pub fn client(&self) -> &Client {
        &self.client
    }

    pub fn blockOn<F: std::future::Future>(&self, future: F) -> F::Output {
        self.runtime.block_on(future)
    }
}

/// See `PgBacking::connect`'s call site above for why this exists.
/// Connects to the target DSN's server but the `postgres` administrative
/// database (never the target database itself — you cannot
/// `CREATE DATABASE` from within the database being created), checks
/// `pg_database` for the target name, and issues a real
/// `CREATE DATABASE` if it's absent.
async fn ensureTargetDatabaseExists(targetDsn: &str) -> Result<(), String> {
    let mut config = Config::from_str(targetDsn).map_err(|error| format!("ensureTargetDatabaseExists: parse DSN: {error}"))?;
    let targetDatabaseName = config
        .get_dbname()
        .ok_or_else(|| format!("ensureTargetDatabaseExists: DSN {targetDsn} has no database name"))?
        .to_string();

    config.dbname("postgres");
    let (adminClient, adminConnection) = config
        .connect(NoTls)
        .await
        .map_err(|error| format!("ensureTargetDatabaseExists: connect to admin database: {error}"))?;
    tokio::spawn(async move {
        if let Err(connectionError) = adminConnection.await {
            eprintln!("ensureTargetDatabaseExists: admin connection error: {connectionError}");
        }
    });

    let existsRow = adminClient
        .query_one("SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", &[&targetDatabaseName])
        .await
        .map_err(|error| format!("ensureTargetDatabaseExists: check pg_database: {error}"))?;
    let alreadyExists: bool = existsRow.get(0);
    if alreadyExists {
        return Ok(());
    }

    // CREATE DATABASE is DDL and cannot take the name as a bound
    // parameter — targetDatabaseName came out of OUR OWN DSN's path (an
    // operator-controlled env var, not untrusted end-user input), same
    // trust boundary as every other env-var-driven config value in this
    // codebase. Quoted per the standard SQL identifier-quoting rule
    // (double the embedded double-quotes) since it's still going
    // through string formatting.
    let quotedDatabaseName = format!("\"{}\"", targetDatabaseName.replace('"', "\"\""));
    adminClient
        .batch_execute(&format!("CREATE DATABASE {quotedDatabaseName}"))
        .await
        .map_err(|error| format!("ensureTargetDatabaseExists: create database {targetDatabaseName}: {error}"))?;
    println!("market-data: created Postgres database {targetDatabaseName:?} (did not previously exist)");
    Ok(())
}
