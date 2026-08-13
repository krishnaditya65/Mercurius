#!/usr/bin/env bash
#
# backupLedgerSnapshot.sh — FEATURES.md §13 "[P1] Automated backups +
# tested restore procedure for ledger DB".
#
# Calls GET /admin/snapshot on a RUNNING ledger process and writes the
# complete in-memory ledger state (every account balance, every posted
# journal entry) to a timestamped JSON file under services/ledger/backups/.
#
# TODO(real build): once the ledger is Postgres-backed, the primary
# backup mechanism becomes pg_dump / a managed point-in-time-recovery
# snapshot of the actual database, not this script — this script (or a
# descendant of it) would then be, at most, a supplementary export tool.
# Today, with no database at all, this script IS the backup mechanism.
#
# Usage:
#   ./backupLedgerSnapshot.sh [ledgerBaseUrl] [backupsDirectory]
#
# Defaults:
#   ledgerBaseUrl     http://localhost:8082
#   backupsDirectory  <repo>/services/ledger/backups
#
# Exit codes:
#   0  backup written successfully
#   1  the ledger's /admin/snapshot endpoint could not be reached or
#      returned a non-2xx response
#   2  curl or another required tool is missing

set -euo pipefail

scriptDirectory="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ledgerServiceDirectory="$(cd "${scriptDirectory}/.." && pwd)"

ledgerBaseUrl="${1:-http://localhost:8082}"
backupsDirectory="${2:-${ledgerServiceDirectory}/backups}"

if ! command -v curl >/dev/null 2>&1; then
	echo "backupLedgerSnapshot.sh: curl is required but not found on PATH" >&2
	exit 2
fi

mkdir -p "${backupsDirectory}"

timestampUtc="$(date -u +%Y%m%dT%H%M%SZ)"
backupFilePath="${backupsDirectory}/ledgerSnapshot-${timestampUtc}.json"
tempFilePath="${backupFilePath}.tmp"

httpStatusCode="$(curl -sS -w '%{http_code}' -o "${tempFilePath}" "${ledgerBaseUrl}/admin/snapshot" || echo "curl_failed")"

if [[ "${httpStatusCode}" != "200" ]]; then
	echo "backupLedgerSnapshot.sh: GET ${ledgerBaseUrl}/admin/snapshot failed (http_status=${httpStatusCode})" >&2
	rm -f "${tempFilePath}"
	exit 1
fi

# Sanity-check the response actually looks like a snapshot before
# committing to the final filename — an operator relying on this backup
# later should never find a truncated/malformed file silently sitting in
# backups/.
if ! grep -q '"snapshotFormatVersion"' "${tempFilePath}"; then
	echo "backupLedgerSnapshot.sh: response from ${ledgerBaseUrl}/admin/snapshot did not look like a ledger snapshot" >&2
	rm -f "${tempFilePath}"
	exit 1
fi

mv "${tempFilePath}" "${backupFilePath}"
echo "backupLedgerSnapshot.sh: wrote ${backupFilePath}"
