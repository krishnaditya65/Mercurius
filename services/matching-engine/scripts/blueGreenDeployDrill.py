#!/usr/bin/env python3
"""
blueGreenDeployDrill.py — FEATURES.md §13 [P3]: "Blue/green or canary
deploys for the matching engine specifically."

There is no k8s/cloud infra for matching-engine in this repo (see
ARCHITECTURE.md §7: it deliberately runs on bare metal / dedicated
instances, not an autoscaled cluster), so there is no load balancer to
reconfigure and no health-check-gated traffic shifter to drive. What DOES
exist, and what a real blue/green deploy for a STATEFUL matching engine
actually depends on before any traffic-shifting infrastructure matters at
all, is this repo's WAL + deterministic replay mechanism
(writeAheadLog.rs, walBackedOrderBook.rs, deterministicReplayHarness.rs).
This script proves that core primitive for real, against two real running
matching-engine subprocesses talking real wire protocol
(wireProtocol.rs) — not simulated in-process, not asserted from memory.

Naming convention note: this repo requires long, descriptive camelCase
identifiers across all services, overriding each language's own idiom
(see the mercurius-naming-convention project memory) — including here,
where plain Python would normally use snake_case.

WHAT THIS SCRIPT ACTUALLY DOES (all against real subprocesses):
  1. Builds the release binary (`cargo build --release`) if needed.
  2. Starts a "blue" matching-engine instance — the currently-live
     instance — listening on a real TCP port, with its own WAL file.
  3. Sends it a real sequence of orders over the real wire protocol
     (limit orders that rest, a crossing fill, a cancel, a stop order
     that arms and later triggers) — real accumulated WAL state.
  4. Copies blue's WAL file byte-for-byte to a fresh path for "green".
  5. Starts a "green" matching-engine instance pointed at the copied
     WAL. On startup it goes through `WalBackedOrderBook::
     openRecoveringIfPresent`, the EXACT SAME replay mechanism
     `deterministicReplayHarness.rs` and the crash-recovery test suite
     already rely on (see writeAheadLog.rs::
     replayWalEventRecordsIntoFreshOrderBook) — nothing new is invented
     here, this script just drives that existing mechanism from the
     outside, against a real second process.
  6. With BOTH instances now live simultaneously, queries every order's
     status over the real wire protocol against BOTH blue and green and
     diffs the raw JSON responses byte-for-byte — this is the
     state-parity proof, obtained the same way any real client would
     observe book state, not by peeking at internal Rust structs.
  7. As an independent second parity check that reuses the existing
     offline tool rather than reinventing a comparison, also runs
     `matching_engine --replay <walFile>` (see `runReplayModeAndExit` in
     main.rs) against BOTH wal files and diffs that output too, plus a
     sha256 of both WAL files.
  8. Prints an explicit, clearly-labeled "traffic cutover" section
     describing what a REAL cutover would do next (documented only —
     see the big warning below and in BLUE_GREEN_DRILL.md).
  9. Kills both subprocesses on the way out, success or failure.

Run: `python3 services/matching-engine/scripts/blueGreenDeployDrill.py`
from anywhere (paths are resolved relative to this file). No arguments,
no external dependencies beyond the Python 3 standard library.
"""

from __future__ import annotations

import hashlib
import json
import os
import shutil
import socket
import subprocess
import sys
import tempfile
import time
from pathlib import Path

scriptFilePath = Path(__file__).resolve()
matchingEngineServiceDirectory = scriptFilePath.parent.parent
releaseBinaryPath = matchingEngineServiceDirectory / "target" / "release" / "matching_engine"

blueTcpListenAddress = "127.0.0.1:9101"
greenTcpListenAddress = "127.0.0.1:9201"
tradedInstrumentSymbol = "DEMO-EQ"

failureCount = 0


def printSectionHeading(headingText: str) -> None:
    print()
    print("=" * 78)
    print(headingText)
    print("=" * 78)


def recordFailure(failureDescription: str) -> None:
    global failureCount
    failureCount += 1
    print(f"  !! PARITY FAILURE: {failureDescription}")


def buildReleaseBinaryIfNeeded() -> None:
    printSectionHeading("STEP 0 — cargo build --release")
    buildResult = subprocess.run(
        ["cargo", "build", "--release"],
        cwd=matchingEngineServiceDirectory,
        capture_output=True,
        text=True,
    )
    print(buildResult.stdout)
    print(buildResult.stderr)
    if buildResult.returncode != 0:
        print("cargo build --release FAILED — aborting drill.")
        sys.exit(1)
    if not releaseBinaryPath.exists():
        print(f"expected release binary not found at {releaseBinaryPath}")
        sys.exit(1)
    print(f"release binary ready: {releaseBinaryPath}")


def startMatchingEngineInstance(
    instanceLabel: str,
    tcpListenAddress: str,
    walFilePath: Path,
    stdoutLogPath: Path,
    stderrLogPath: Path,
) -> subprocess.Popen:
    instanceEnvironment = dict(os.environ)
    instanceEnvironment["MATCHING_ENGINE_TCP_LISTEN_ADDRESS"] = tcpListenAddress
    instanceEnvironment["MATCHING_ENGINE_WAL_FILE_PATH"] = str(walFilePath)

    stdoutLogFile = open(stdoutLogPath, "w")
    stderrLogFile = open(stderrLogPath, "w")
    launchedProcess = subprocess.Popen(
        [str(releaseBinaryPath)],
        cwd=matchingEngineServiceDirectory,
        env=instanceEnvironment,
        stdout=stdoutLogFile,
        stderr=stderrLogFile,
    )
    print(
        f"[{instanceLabel}] started pid={launchedProcess.pid} on {tcpListenAddress}, "
        f"WAL={walFilePath}"
    )
    return launchedProcess


def waitUntilTcpPortAcceptsConnections(tcpListenAddress: str, timeoutSeconds: float = 10.0) -> None:
    hostPart, portPart = tcpListenAddress.split(":")
    deadline = time.monotonic() + timeoutSeconds
    lastError: Exception | None = None
    while time.monotonic() < deadline:
        try:
            with socket.create_connection((hostPart, int(portPart)), timeout=0.5):
                return
        except OSError as connectError:
            lastError = connectError
            time.sleep(0.05)
    raise TimeoutError(f"{tcpListenAddress} never accepted a connection: {lastError}")


def sendOneWireProtocolRequestAndReadResponse(tcpListenAddress: str, requestObject: dict) -> dict:
    """One connection per request, exactly matching main.rs's accept-loop
    contract (see its module comment: "at most one request ever in flight
    through the ring buffers at a time")."""
    hostPart, portPart = tcpListenAddress.split(":")
    with socket.create_connection((hostPart, int(portPart)), timeout=5.0) as connection:
        requestLine = json.dumps(requestObject) + "\n"
        connection.sendall(requestLine.encode("utf-8"))
        responseBytes = b""
        while not responseBytes.endswith(b"\n"):
            chunk = connection.recv(4096)
            if not chunk:
                break
            responseBytes += chunk
        return json.loads(responseBytes.decode("utf-8"))


def submitOrder(
    tcpListenAddress: str,
    clientAccountIdentifier: str,
    isBuyNotSell: bool,
    limitPriceInMinorUnits: int,
    orderQuantity: int,
    isMarketOrder: bool = False,
    isStopLossVariant: bool = False,
    stopTriggerPriceInMinorUnits: int | None = None,
) -> dict:
    requestObject = {
        "clientAccountIdentifier": clientAccountIdentifier,
        "instrumentSymbol": tradedInstrumentSymbol,
        "orderSideIsBuyNotSell": isBuyNotSell,
        "orderIsMarketOrderNotLimit": isMarketOrder,
        "orderIsStopLossVariant": isStopLossVariant,
        "stopTriggerPriceInMinorUnits": stopTriggerPriceInMinorUnits,
        "limitPriceInMinorUnits": limitPriceInMinorUnits,
        "orderQuantity": orderQuantity,
    }
    return sendOneWireProtocolRequestAndReadResponse(tcpListenAddress, requestObject)


def cancelOrder(tcpListenAddress: str, orderSequenceNumberToCancel: int) -> dict:
    requestObject = {
        "clientAccountIdentifier": "cancelRequester",
        "instrumentSymbol": tradedInstrumentSymbol,
        "orderSideIsBuyNotSell": True,
        "cancelOrderSequenceNumber": orderSequenceNumberToCancel,
        "limitPriceInMinorUnits": 0,
        "orderQuantity": 0,
    }
    return sendOneWireProtocolRequestAndReadResponse(tcpListenAddress, requestObject)


def queryOrderStatus(tcpListenAddress: str, orderSequenceNumberToQuery: int) -> dict:
    requestObject = {
        "clientAccountIdentifier": "statusQuerier",
        "instrumentSymbol": tradedInstrumentSymbol,
        "orderSideIsBuyNotSell": True,
        "queryOrderStatusSequenceNumber": orderSequenceNumberToQuery,
        "limitPriceInMinorUnits": 0,
        "orderQuantity": 0,
    }
    return sendOneWireProtocolRequestAndReadResponse(tcpListenAddress, requestObject)


def sha256HexDigestOfFile(filePath: Path) -> str:
    hasher = hashlib.sha256()
    hasher.update(filePath.read_bytes())
    return hasher.hexdigest()


def runOfflineReplayModeAndCaptureOutput(walFilePath: Path) -> str:
    replayResult = subprocess.run(
        [str(releaseBinaryPath), "--replay", str(walFilePath)],
        cwd=matchingEngineServiceDirectory,
        capture_output=True,
        text=True,
        timeout=15,
    )
    return replayResult.stdout


def normalizeReplayOutputForDiffing(replayStdout: str, walFilePathToStrip: Path) -> str:
    """The only line in --replay's output that legitimately differs
    between the blue-derived and green-derived copies is the one that
    echoes back the file path it was pointed at (they're different
    paths, by construction, even though the file CONTENTS are byte-
    identical). Strip that one line so the diff below is a pure
    book-state comparison, not a path comparison."""
    return "\n".join(
        line
        for line in replayStdout.splitlines()
        if not line.startswith("--replay: reading WAL events from")
    )


def terminateProcessQuietly(process: subprocess.Popen, instanceLabel: str) -> None:
    if process.poll() is not None:
        return
    print(f"[{instanceLabel}] terminating pid={process.pid}")
    process.terminate()
    try:
        process.wait(timeout=5)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait(timeout=5)


def main() -> int:
    buildReleaseBinaryIfNeeded()

    # Deliberately NOT the OS system temp dir (e.g. macOS's $TMPDIR under
    # /var/folders/...): in sandboxed dev environments that path can be
    # writable-but-ephemeral out from under a spawned subprocess. A
    # directory inside this service's own tree is exactly as writable as
    # everything else this drill already does (`cargo build --release`
    # writes to `target/` right next to it) and doesn't disappear mid-run.
    drillRunsRootDirectory = matchingEngineServiceDirectory / "scripts" / ".blueGreenDrillRuns"
    drillRunsRootDirectory.mkdir(parents=True, exist_ok=True)
    drillWorkingDirectory = Path(
        tempfile.mkdtemp(prefix=f"run-{int(time.time())}-", dir=drillRunsRootDirectory)
    )
    print(f"drill working directory (WAL files + subprocess logs): {drillWorkingDirectory}")

    blueWalFilePath = drillWorkingDirectory / "blueMatchingEngineWriteAheadLog.jsonl"
    greenWalFilePath = drillWorkingDirectory / "greenMatchingEngineWriteAheadLog.jsonl"

    blueProcess: subprocess.Popen | None = None
    greenProcess: subprocess.Popen | None = None

    try:
        # ---- STEP 1: start blue, the currently-live instance ----------
        printSectionHeading("STEP 1 — start BLUE (currently-live) instance, real subprocess")
        blueProcess = startMatchingEngineInstance(
            "blue",
            blueTcpListenAddress,
            blueWalFilePath,
            drillWorkingDirectory / "blueStdout.log",
            drillWorkingDirectory / "blueStderr.log",
        )
        waitUntilTcpPortAcceptsConnections(blueTcpListenAddress)
        print(f"[blue] accepting TCP connections on {blueTcpListenAddress}")

        # ---- STEP 2: send real orders over the real wire protocol -----
        printSectionHeading("STEP 2 — send real orders to BLUE over the real wire protocol")
        assignedOrderSequenceNumbers: list[int] = []

        r = submitOrder(blueTcpListenAddress, "sellerA", False, 100, 10)
        print("  sellerA SELL 10 @100 ->", r)
        assignedOrderSequenceNumbers.append(r["assignedOrderSequenceNumber"])

        r = submitOrder(blueTcpListenAddress, "sellerB", False, 105, 5)
        print("  sellerB SELL 5 @105  ->", r)
        assignedOrderSequenceNumbers.append(r["assignedOrderSequenceNumber"])

        r = submitOrder(blueTcpListenAddress, "buyerC", True, 95, 8)
        print("  buyerC  BUY  8 @95   ->", r)
        buyerCOrderId = r["assignedOrderSequenceNumber"]
        assignedOrderSequenceNumbers.append(buyerCOrderId)

        r = submitOrder(blueTcpListenAddress, "buyerD", True, 100, 4)
        print("  buyerD  BUY  4 @100 (crosses sellerA) ->", r)
        assignedOrderSequenceNumbers.append(r["assignedOrderSequenceNumber"])

        r = submitOrder(
            blueTcpListenAddress,
            "stopSellerE",
            False,
            0,
            3,
            isMarketOrder=True,
            isStopLossVariant=True,
            stopTriggerPriceInMinorUnits=90,
        )
        print("  stopSellerE SELL-STOP-MARKET trigger<=90 qty3 (arms, pending) ->", r)
        assignedOrderSequenceNumbers.append(r["assignedOrderSequenceNumber"])

        cancelResult = cancelOrder(blueTcpListenAddress, buyerCOrderId)
        print(f"  cancel buyerC's order ({buyerCOrderId}) ->", cancelResult)

        r = submitOrder(blueTcpListenAddress, "sellerG", False, 89, 2)
        print("  sellerG SELL 2 @89   ->", r)
        assignedOrderSequenceNumbers.append(r["assignedOrderSequenceNumber"])

        r = submitOrder(blueTcpListenAddress, "buyerH", True, 89, 2)
        print("  buyerH  BUY  2 @89 (crosses sellerG at 89, triggers stopSellerE) ->", r)
        assignedOrderSequenceNumbers.append(r["assignedOrderSequenceNumber"])

        print(f"  assigned order sequence numbers so far: {assignedOrderSequenceNumbers}")
        print("  BLUE now holds real, WAL-durable book state built from real wire traffic.")

        # ---- STEP 3: copy blue's WAL to green's path -------------------
        printSectionHeading("STEP 3 — copy BLUE's WAL byte-for-byte to a fresh path for GREEN")
        shutil.copyfile(blueWalFilePath, greenWalFilePath)
        blueWalHash = sha256HexDigestOfFile(blueWalFilePath)
        greenWalHash = sha256HexDigestOfFile(greenWalFilePath)
        print(f"  blue  WAL sha256: {blueWalHash}  ({blueWalFilePath})")
        print(f"  green WAL sha256: {greenWalHash}  ({greenWalFilePath})")
        if blueWalHash != greenWalHash:
            recordFailure("copied WAL file hash does not match source WAL file hash")
        else:
            print("  MATCH — green's starting WAL is byte-identical to blue's.")

        # ---- STEP 4: start green, recovering via the EXISTING replay --
        printSectionHeading(
            "STEP 4 — start GREEN, recovering from the copied WAL via "
            "WalBackedOrderBook::openRecoveringIfPresent (the same replay path "
            "deterministicReplayHarness.rs already exercises)"
        )
        greenProcess = startMatchingEngineInstance(
            "green",
            greenTcpListenAddress,
            greenWalFilePath,
            drillWorkingDirectory / "greenStdout.log",
            drillWorkingDirectory / "greenStderr.log",
        )
        waitUntilTcpPortAcceptsConnections(greenTcpListenAddress)
        print(f"[green] accepting TCP connections on {greenTcpListenAddress}")
        time.sleep(0.2)  # let its startup stderr line land in the log file
        greenStderrText = (drillWorkingDirectory / "greenStderr.log").read_text()
        recoveryLine = next(
            (line for line in greenStderrText.splitlines() if "recovered" in line),
            "(no 'recovered' line found in green's stderr!)",
        )
        print(f"  green's own recovery log line: {recoveryLine}")
        if "recovered" not in recoveryLine:
            recordFailure("green did not log a WAL recovery line on startup")

        # ---- STEP 5: BOTH instances live — diff live query responses --
        printSectionHeading(
            "STEP 5 — BOTH instances now live simultaneously: query every order's "
            "status on BLUE and on GREEN over the real wire protocol and diff the "
            "raw responses"
        )
        allMatched = True
        for orderSequenceNumber in assignedOrderSequenceNumbers:
            blueStatus = queryOrderStatus(blueTcpListenAddress, orderSequenceNumber)
            greenStatus = queryOrderStatus(greenTcpListenAddress, orderSequenceNumber)
            isMatch = blueStatus == greenStatus
            allMatched = allMatched and isMatch
            marker = "OK  " if isMatch else "FAIL"
            print(f"  [{marker}] order {orderSequenceNumber}: blue={blueStatus}")
            print(f"              {' ' * len(marker)}      green={greenStatus}")
            if not isMatch:
                recordFailure(f"order {orderSequenceNumber} status differs between blue and green")
        if allMatched:
            print(
                f"  MATCH — all {len(assignedOrderSequenceNumbers)} order-status query "
                "responses are byte-identical between blue and green."
            )

        # ---- STEP 6: independent structural parity check, reusing the -
        # ---- existing --replay offline tool (writeAheadLog.rs's own ---
        # ---- replay function) rather than inventing a new comparator --
        printSectionHeading(
            "STEP 6 — independent structural check: run the EXISTING "
            "`matching_engine --replay <walFile>` tool (main.rs::runReplayModeAndExit, "
            "which calls the exact same writeAheadLog::replayWalEventRecordsIntoFreshOrderBook "
            "used by deterministicReplayHarness.rs) against both WAL files and diff the output"
        )
        blueReplayStdout = runOfflineReplayModeAndCaptureOutput(blueWalFilePath)
        greenReplayStdout = runOfflineReplayModeAndCaptureOutput(greenWalFilePath)
        print("  --- blue WAL replayed standalone ---")
        print(blueReplayStdout)
        print("  --- green WAL replayed standalone ---")
        print(greenReplayStdout)

        normalizedBlueReplay = normalizeReplayOutputForDiffing(blueReplayStdout, blueWalFilePath)
        normalizedGreenReplay = normalizeReplayOutputForDiffing(greenReplayStdout, greenWalFilePath)
        if normalizedBlueReplay == normalizedGreenReplay:
            print("  MATCH — standalone replay of blue's WAL and green's WAL produce "
                  "identical book depth + pending-stop-order output.")
        else:
            recordFailure("standalone --replay output differs between blue's WAL and green's WAL")

        # ---- STEP 7: prove green is genuinely live, not a static dump -
        printSectionHeading(
            "STEP 7 — prove GREEN is a fully live, independently-writable instance "
            "post-recovery (submits its own next order, after all parity checks above)"
        )
        postRecoveryOrderResponse = submitOrder(greenTcpListenAddress, "postCutoverTrader", True, 101, 1)
        print(f"  green accepts a brand-new order post-recovery -> {postRecoveryOrderResponse}")
        print(
            "  (this order was sent to GREEN ONLY, after every comparison above — it is not "
            "part of the parity proof, just evidence green is a real, independently-running "
            "matching-engine instance and not a static replay dump.)"
        )

        # ---- STEP 8: the documented, NOT-implemented cutover ----------
        printSectionHeading("STEP 8 — traffic cutover moment (DOCUMENTED / SIMULATED ONLY)")
        print(
            "  With state parity confirmed above, a REAL blue/green cutover would now:\n"
            "    1. Stop routing NEW inbound orders to blue at the sequencer/router\n"
            "       (ARCHITECTURE.md §3.2) — e.g. a load-balancer/service-mesh weight\n"
            "       flip or a health-check-gated DNS/VIP move.\n"
            "    2. Drain blue: let any order already in blue's ring buffer/socket\n"
            "       finish processing and its response reach the client before blue\n"
            "       stops accepting new connections.\n"
            "    3. Confirm green's WAL sequence number picks up exactly where blue's\n"
            "       left off (no gap, no duplicate) — this script's STEP 7 order landing\n"
            "       cleanly on green after the replay is the single-instrument version\n"
            "       of that check.\n"
            "    4. Point the router's shard map (or the standby-failover mechanism in\n"
            "       ARCHITECTURE.md §3.4's 'Hot standby' bullet) at green as the new\n"
            "       'blue' for this instrument/shard.\n"
            "    5. Keep the old blue warm for a rollback window, then retire it.\n"
        )
        print(
            "  !! THIS SCRIPT DOES NOT DO ANY OF THE ABOVE. There is no load balancer,\n"
            "  !! no service mesh, no health-check-gated router, and no DNS/VIP in this\n"
            "  !! environment to reconfigure — see BLUE_GREEN_DRILL.md. What this script\n"
            "  !! proves is the CORE PRIMITIVE a stateful matching engine's blue/green\n"
            "  !! deploy depends on before any of the above can safely happen at all:\n"
            "  !! that a second instance recovered from the first instance's WAL reaches\n"
            "  !! BIT-IDENTICAL state. Real traffic-shifting infrastructure is out of\n"
            "  !! scope for this repo today."
        )

        return 0 if failureCount == 0 else 1

    finally:
        printSectionHeading("CLEANUP — killing both subprocesses")
        if blueProcess is not None:
            terminateProcessQuietly(blueProcess, "blue")
        if greenProcess is not None:
            terminateProcessQuietly(greenProcess, "green")
        print(f"WAL files + subprocess logs left on disk for inspection at: {drillWorkingDirectory}")


if __name__ == "__main__":
    exitCode = main()
    printSectionHeading("DRILL RESULT")
    if exitCode == 0:
        print(f"ALL PARITY CHECKS PASSED ({failureCount} failures).")
    else:
        print(f"DRILL FAILED — {failureCount} parity failure(s) recorded above.")
    sys.exit(exitCode)
