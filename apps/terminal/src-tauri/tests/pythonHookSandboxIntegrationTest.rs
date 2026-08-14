// Real integration tests for the Python hook sandbox — FEATURES.md §10
// "[P3] Local Python hook sandbox for algo traders (isolated subprocess,
// resource-capped)". These genuinely spawn `python3` subprocesses (no
// mocking of the OS) and assert on real resource-limit enforcement. See
// `src/pythonHookSandbox.rs`'s module doc comment for exactly what each
// cap does and does not guarantee on this platform — these tests are
// literally how those honesty notes were derived; they must keep passing
// or the comments above them are lying.
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.
#![allow(non_snake_case)]

use std::time::{Duration, Instant};
use terminal_lib::pythonHookSandbox::{
    spawnAndAwaitSandboxedPythonHook, PythonHookResourceLimits, PythonHookTerminationReason,
};

#[test]
fn trivialScriptExitsCleanlyUnderDefaultLimits() {
    let outcome =
        spawnAndAwaitSandboxedPythonHook("print(2 + 2)", &PythonHookResourceLimits::default())
            .expect("subprocess should spawn");
    assert_eq!(
        outcome.terminationReason,
        PythonHookTerminationReason::ExitedNormally
    );
    assert_eq!(outcome.exitStatusCode, Some(0));
    assert_eq!(outcome.standardOutputText.trim(), "4");
}

/// The core resource-capping claim of this module: a genuinely CPU-bound
/// busy loop, given a tight RLIMIT_CPU, gets killed by the KERNEL (SIGXCPU)
/// well before the much more generous wall-clock watchdog would have fired.
/// This is what makes the sandbox "resource-capped" rather than merely
/// "time-boxed" — the distinction matters for a runaway algo hook that's
/// spinning the CPU rather than sleeping/blocking.
#[test]
fn cpuBoundBusyLoopScriptGetsKilledByCpuTimeLimit() {
    let resourceLimits = PythonHookResourceLimits {
        maximumCpuTimeSeconds: 1,
        maximumAddressSpaceBytes: 256 * 1024 * 1024,
        // Deliberately much larger than the CPU cap — if this test's
        // process gets killed anywhere near 20s instead of ~1s, that's the
        // wall-clock watchdog catching it, not the CPU limit, and this
        // test's assertion on elapsed time below will fail, correctly
        // flagging that the CPU cap stopped working.
        maximumWallClockSeconds: 20,
    };
    let startedAt = Instant::now();
    let outcome = spawnAndAwaitSandboxedPythonHook(
        // A tight, allocation-free busy loop — genuinely CPU-bound, not
        // blocked on I/O, so RLIMIT_CPU (not the wall-clock watchdog) is
        // what should end it.
        "x = 0\nwhile True:\n    x += 1\n",
        &resourceLimits,
    )
    .expect("subprocess should spawn");
    let elapsed = startedAt.elapsed();

    match outcome.terminationReason {
        PythonHookTerminationReason::KilledBySignal(signalNumber) => {
            // SIGXCPU is 24 on both macOS and Linux.
            assert_eq!(
                signalNumber, 24,
                "expected SIGXCPU (24), got signal {signalNumber}"
            );
        }
        other => panic!("expected the busy loop to be killed by SIGXCPU, got {other:?}"),
    }
    assert!(
        elapsed < Duration::from_secs(10),
        "busy loop should have been killed by the ~1s CPU limit, not the 20s wall-clock \
         watchdog, but took {elapsed:?}"
    );
}

/// Complementary case: a script that's blocked in `time.sleep` (NOT
/// burning CPU) should sail past the CPU-time limit untouched — RLIMIT_CPU
/// only counts CPU time actually consumed — and instead get caught by the
/// Rust-side wall-clock watchdog. Confirms the two mechanisms are doing
/// genuinely different jobs, not one silently subsuming the other.
#[test]
fn sleepBoundScriptGetsKilledByWallClockWatchdogNotCpuLimit() {
    let resourceLimits = PythonHookResourceLimits {
        // Generous CPU budget — a sleeping process consumes ~0 CPU time,
        // so this should never be the reason it's killed.
        maximumCpuTimeSeconds: 30,
        maximumAddressSpaceBytes: 256 * 1024 * 1024,
        maximumWallClockSeconds: 1,
    };
    let startedAt = Instant::now();
    let outcome =
        spawnAndAwaitSandboxedPythonHook("import time\ntime.sleep(60)\n", &resourceLimits)
            .expect("subprocess should spawn");
    let elapsed = startedAt.elapsed();

    assert_eq!(
        outcome.terminationReason,
        PythonHookTerminationReason::KilledByWallClockWatchdog
    );
    assert!(
        elapsed < Duration::from_secs(5),
        "should have been killed near the 1s wall-clock budget, took {elapsed:?}"
    );
}

#[test]
fn appliedIsolationNotesHonestlyDescribeWhatWasAttempted() {
    let outcome =
        spawnAndAwaitSandboxedPythonHook("print('ok')", &PythonHookResourceLimits::default())
            .expect("subprocess should spawn");
    // Regardless of platform, the caller should always be told what was
    // attempted for the CPU and memory caps — this is the honesty contract
    // the module doc comment describes, verified structurally here.
    assert!(outcome
        .appliedIsolationNotes
        .iter()
        .any(|note| note.contains("RLIMIT_CPU")));
    assert!(outcome
        .appliedIsolationNotes
        .iter()
        .any(|note| note.contains("RLIMIT_AS")));
}
