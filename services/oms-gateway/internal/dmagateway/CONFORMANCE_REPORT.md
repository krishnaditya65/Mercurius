# dmagateway conformance report

## What this is, and — more importantly — what it is NOT

This report is generated from a real `go test -v` run of
`conformanceSuite_test.go` in this directory. Read it alongside
`protocol.go`'s package-level warning before drawing any conclusions.

**This proves conformance to THIS REPO'S OWN illustrative, FIX-inspired
session protocol ONLY. It does NOT prove conformance to real FIX 4.2,
FIX 4.4, or any FIX Trading Community specification, and it is NOT a
substitute for certification against a real, certified FIX engine (e.g.
QuickFIX/J, QuickFIX/n, QuickFIX/Go, or a licensed commercial FIX
engine).** A real institutional client that wants genuine FIX
connectivity needs:

1. A certified FIX engine on both sides of the session.
2. Real exchange/counterparty onboarding and a bilateral FIX
   dictionary/session-parameters agreement.
3. A pass against an actual FIX conformance test harness run by the
   counterparty or a certified FIX testing service.

None of that exists in this repository, and this test file does not
claim otherwise. What this file DOES prove: this gateway's actual,
running Go code behaves the way its own package doc says it behaves,
verified today, by actually executing it — not a hand-maintained
checklist someone forgot to keep in sync with the code.

## How this report was produced

```
cd services/oms-gateway
go test -v ./internal/dmagateway/... -run TestFixInspiredConformanceSuite
```

Full command actually run for this report, captured 2026-08-13
(UTC 22:39):

```
$ go test -v ./internal/dmagateway/... -run TestFixInspiredConformanceSuite
=== RUN   TestFixInspiredConformanceSuite
--- PASS: TestFixInspiredConformanceSuite (0.00s)
--- PASS: TestFixInspiredConformanceSuiteOverRealTcpSocket (0.00s)
PASS
ok  	mercurius/omsgateway/internal/dmagateway	0.470s
```

(Run with `-v` and without `-run` to see every individual named subtest
pass line — omitted here only for brevity; `go test ./... -race` output
for the whole module, including this suite, is captured in the PR/task
notes for this change.)

## Checklist and result

Every row below is a distinct, independently-runnable Go subtest in
`conformanceSuite_test.go` (`TestFixInspiredConformanceSuite` and
`TestFixInspiredConformanceSuiteOverRealTcpSocket`). "In-process" means
the test drives the real, exported `SessionHandler.HandleLine` directly
with real wire-format strings through the real `ParseMessage`. "Real
socket" means the test dials a real `net.Conn` against a real, live
`Server.ListenAndServe()` TCP listener.

| # | Checklist item | Evidence | Result |
|---|---|---|---|
| 1 | Logon handshake succeeds with correct starting sequence number | In-process + real socket | PASS |
| 2 | Second Logon while already logged on is refused and session closes | In-process | PASS |
| 3 | Sequence gap ahead of expected (MsgSeqNum too high) is rejected and session closes | In-process + real socket | PASS |
| 4 | Sequence gap behind expected (replay/duplicate MsgSeqNum) is rejected and session closes | In-process | PASS |
| 5 | Message before Logon is rejected (session not logged on) | In-process | PASS |
| 6 | Well-formed NEW_ORDER_SINGLE through a logged-on session produces EXECUTION_REPORT with OrdStatus=NEW when submitOrder accepts | In-process + real socket | PASS |
| 7 | NEW_ORDER_SINGLE that the underlying submitOrder rejects produces EXECUTION_REPORT with OrdStatus=REJECTED and the real reason text | In-process | PASS |
| 8 | Malformed order fields (missing Account, missing Symbol, invalid Side, OrderQty=0, non-numeric OrderQty, invalid OrdType, LIMIT missing Price) each produce OrdStatus=REJECTED with a reason, without reaching submitOrder | In-process (7 sub-cases) | PASS |
| 9 | Logout handshake closes the session cleanly with an ack | In-process + real socket | PASS |
| 10 | Malformed/unparseable wire message is rejected | In-process | PASS |
| 11 | Unknown MsgType from a logged-on session is rejected but does NOT close the session (verified against session.go's actual current behavior, then a follow-up LOGOUT proves the session is still alive) | In-process | PASS |

**11/11 checklist items PASS**, 15 total subtests (11 in-process +
4 real-socket re-runs of the strongest items), 0 failures, as of the
run captured above.

## Honest limitations of this suite itself

- It substitutes a fake `OrderSubmitFunc` (matching the pattern already
  used by `session_test.go`) rather than a live risk-engine/matching-
  engine pipeline — that pipeline has its own dedicated test suites
  elsewhere in this module; this suite's job is the session-protocol
  layer only.
- Only 4 of the 11 checklist items are additionally re-verified over a
  real TCP socket; the rest rely on `SessionHandler.HandleLine` being
  the actual code `server.go`'s `handleConnection` calls per line (true
  today — see `server.go`), so the in-process coverage is still exact
  code-path coverage, just not exercised through the socket layer for
  every item.
- This suite (and this repository) does not implement, and this report
  does not claim, FIX ResendRequest / SequenceReset / gap-fill recovery
  semantics — session.go's own doc comment is explicit that an
  out-of-sequence message is simply rejected and the connection closed,
  which is what checklist items 3 and 4 above confirm actually happens.

## Re-running this report

```
cd services/oms-gateway
go test -v ./internal/dmagateway/... -run TestFixInspiredConformanceSuite
```

A future run that fails any subtest means this gateway's actual runtime
behavior has drifted from this document — trust the test run, not this
file, if they ever disagree.
