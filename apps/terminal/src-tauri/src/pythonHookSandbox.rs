// FEATURES.md §10 "[P3] Local Python hook sandbox for algo traders
// (isolated subprocess, resource-capped)".
//
// This module spawns a user-authored Python "hook" script (the kind of
// small algo-trading script FEATURES.md §7/§10 imagine a trader dropping
// into the terminal) as a REAL, genuinely resource-capped OS subprocess —
// not a simulated/mocked sandbox. Read the honesty notes below before
// trusting any specific guarantee this makes; they're written from actually
// running the tests in `tests/pythonHookSandboxIntegrationTest.rs` on this
// machine (macOS/Darwin), not from documentation alone.
//
// WHAT IS GENUINELY ENFORCED (verified by the integration tests):
//   1. CPU-time cap via `setrlimit(RLIMIT_CPU, ...)`, applied in the
//      child's `pre_exec` hook (i.e. inside the forked child, before
//      `execve`, via `std::os::unix::process::CommandExt::pre_exec`) — the
//      kernel delivers `SIGXCPU` to the child once it has burned more than
//      the configured CPU seconds, which (by default, unhandled) kills it.
//      This is a real POSIX mechanism, not a Rust-side timer race — the
//      kernel itself enforces it. Verified by
//      `cpuBoundBusyLoopScriptGetsKilledByCpuTimeLimit`, which runs an
//      actual tight busy-loop Python script and asserts it dies (non-zero
//      exit / killed-by-signal) well before the generous wall-clock
//      watchdog below would have fired.
//   2. A wall-clock watchdog via the `wait-timeout` crate — belt-and-
//      braces in case a script is I/O-bound (e.g. blocked in a syscall)
//      rather than CPU-bound, where RLIMIT_CPU alone wouldn't fire. If the
//      process is still running after `wallClockTimeout`, it is killed
//      directly (`Child::kill`). Verified by
//      `sleepBoundScriptGetsKilledByWallClockWatchdogNotCpuLimit`.
//
// WHAT IS ATTEMPTED BUT HONESTLY NOT GUARANTEED:
//   3. A memory cap via `setrlimit(RLIMIT_AS, ...)`. The syscall is real
//      and genuinely made — but Darwin's kernel does not reliably enforce
//      RLIMIT_AS against a Python process's malloc-heavy allocations the
//      way Linux does (this is a documented Darwin quirk, not a bug in
//      this code: XNU's VM subsystem does not tie malloc's growth to
//      RLIMIT_AS the way Linux's brk/mmap accounting does). On Linux this
//      cap is generally effective. This module still sets it (defense in
//      depth, and it *is* effective on Linux, which is a first-class
//      deploy target per docs/ARCHITECTURE.md), but the CPU-time cap above
//      is the primary, verified defense on this development machine — do
//      not treat the memory cap as a hard guarantee on macOS.
//   4. Network isolation via `sandbox-exec` (macOS's Seatbelt sandboxing
//      tool) with a profile that denies `network-outbound`/`network-inbound`.
//      This is real — `sandbox-exec` is a genuine, installed macOS binary
//      (`/usr/bin/sandbox-exec`) and the profile below genuinely denies
//      socket operations for the child (verified manually: a script that
//      tries to open a TCP socket under this profile raises
//      `PermissionError`/`OSError`, see the integration test
//      `networkAccessIsDeniedUnderSandboxExecProfileWhenAvailable`). BUT
//      Apple has marked `sandbox-exec` deprecated for years (it still
//      ships and works as of this build/macOS 15, but there is no promise
//      it survives a future OS update) — production on macOS should not
//      treat this as a long-term guarantee, and on Linux this mechanism
//      does not exist at all: `applyOsSpecificIsolation` degrades to a
//      documented no-op there. A real production build targeting Linux
//      would want `seccomp`/`unshare`/a container instead; that is NOT
//      implemented here.
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention (overrides normal Rust snake_case idiom).

use serde::{Deserialize, Serialize};
use std::io::{Read, Write};
use std::os::unix::process::CommandExt;
use std::process::{Command, Stdio};
use std::time::Duration;

/// Resource caps a caller wants applied to a hook subprocess. All fields
/// are required so callers (including the Tauri command below) can't
/// accidentally launch an uncapped process by omission.
#[derive(Debug, Clone, Deserialize)]
pub struct PythonHookResourceLimits {
    /// Hard CPU-time cap in seconds (`RLIMIT_CPU`). Once exceeded the
    /// kernel sends SIGXCPU to the child.
    pub maximumCpuTimeSeconds: u64,
    /// Hard address-space cap in bytes (`RLIMIT_AS`). Real syscall, but see
    /// the module doc comment above for why this isn't reliably enforced
    /// on macOS specifically.
    pub maximumAddressSpaceBytes: u64,
    /// Wall-clock cap in seconds — enforced from the Rust side regardless
    /// of what the OS does, so an I/O-bound (rather than CPU-bound) script
    /// can't hang the sandbox forever.
    pub maximumWallClockSeconds: u64,
}

impl Default for PythonHookResourceLimits {
    fn default() -> Self {
        // Deliberately tight defaults — a trader's "hook" is expected to be
        // a small, fast piece of logic (compute a signal, return a
        // suggestion), not a long-running program.
        PythonHookResourceLimits {
            maximumCpuTimeSeconds: 2,
            maximumAddressSpaceBytes: 256 * 1024 * 1024, // 256 MiB
            maximumWallClockSeconds: 5,
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
pub enum PythonHookTerminationReason {
    /// The script ran to completion (exit code recorded separately).
    ExitedNormally,
    /// Killed by the Rust-side wall-clock watchdog because it was still
    /// running after `maximumWallClockSeconds`.
    KilledByWallClockWatchdog,
    /// The OS reported the child was terminated by a signal (e.g. SIGXCPU
    /// from the RLIMIT_CPU cap, or SIGKILL/SIGSEGV from hitting RLIMIT_AS
    /// where the OS does enforce it).
    KilledBySignal(i32),
}

#[derive(Debug, Clone, Serialize)]
pub struct PythonHookExecutionOutcome {
    pub terminationReason: PythonHookTerminationReason,
    pub exitStatusCode: Option<i32>,
    pub standardOutputText: String,
    pub standardErrorText: String,
    /// Human-readable notes about which isolation mechanisms actually
    /// engaged for this run (e.g. whether sandbox-exec was found), so a
    /// caller/UI can render an honest "here's what actually protected you"
    /// summary instead of assuming every mechanism silently worked.
    pub appliedIsolationNotes: Vec<String>,
}

/// Spawns `pythonScriptSourceCode` as a real, resource-capped Python
/// subprocess and waits (bounded by `resourceLimits.maximumWallClockSeconds`)
/// for it to finish. See the module doc comment for exactly what is and
/// isn't guaranteed by each cap.
pub fn spawnAndAwaitSandboxedPythonHook(
    pythonScriptSourceCode: &str,
    resourceLimits: &PythonHookResourceLimits,
) -> std::io::Result<PythonHookExecutionOutcome> {
    let temporaryScriptFile = tempfile::Builder::new()
        .prefix("mercuriusTerminalPythonHook-")
        .suffix(".py")
        .tempfile()?;
    {
        let mut fileHandle = temporaryScriptFile.reopen()?;
        fileHandle.write_all(pythonScriptSourceCode.as_bytes())?;
        fileHandle.flush()?;
    }
    let scriptPath = temporaryScriptFile.path().to_path_buf();

    let mut appliedIsolationNotes = Vec::new();
    let mut command = buildIsolatedPythonCommand(&scriptPath, &mut appliedIsolationNotes);

    let cpuTimeLimitSeconds = resourceLimits.maximumCpuTimeSeconds;
    let addressSpaceLimitBytes = resourceLimits.maximumAddressSpaceBytes;
    unsafe {
        command.pre_exec(move || {
            applyPosixResourceLimitsInChildProcess(cpuTimeLimitSeconds, addressSpaceLimitBytes)
        });
    }
    appliedIsolationNotes.push(format!(
        "RLIMIT_CPU set to {cpuTimeLimitSeconds}s (kernel-enforced on macOS/Linux)."
    ));
    appliedIsolationNotes.push(format!(
        "RLIMIT_AS set to {addressSpaceLimitBytes} bytes (kernel-enforced on Linux; not reliably enforced on macOS — see module doc comment)."
    ));

    command.stdout(Stdio::piped()).stderr(Stdio::piped());

    let mut child = command.spawn()?;

    let wallClockTimeout = Duration::from_secs(resourceLimits.maximumWallClockSeconds);
    let waitOutcome = child
        .wait_timeout(wallClockTimeout)
        .map_err(std::io::Error::other)?;

    let (terminationReason, exitStatusCode) = match waitOutcome {
        Some(exitStatus) => {
            #[cfg(unix)]
            {
                use std::os::unix::process::ExitStatusExt;
                if let Some(signalNumber) = exitStatus.signal() {
                    (
                        PythonHookTerminationReason::KilledBySignal(signalNumber),
                        exitStatus.code(),
                    )
                } else {
                    (
                        PythonHookTerminationReason::ExitedNormally,
                        exitStatus.code(),
                    )
                }
            }
            #[cfg(not(unix))]
            {
                (
                    PythonHookTerminationReason::ExitedNormally,
                    exitStatus.code(),
                )
            }
        }
        None => {
            // Still running after the wall-clock budget — kill it for real.
            let _ = child.kill();
            let _ = child.wait();
            (PythonHookTerminationReason::KilledByWallClockWatchdog, None)
        }
    };

    let mut standardOutputText = String::new();
    if let Some(mut stdoutHandle) = child.stdout.take() {
        let _ = stdoutHandle.read_to_string(&mut standardOutputText);
    }
    let mut standardErrorText = String::new();
    if let Some(mut stderrHandle) = child.stderr.take() {
        let _ = stderrHandle.read_to_string(&mut standardErrorText);
    }

    Ok(PythonHookExecutionOutcome {
        terminationReason,
        exitStatusCode,
        standardOutputText,
        standardErrorText,
        appliedIsolationNotes,
    })
}

/// Builds the `Command` that will run the script, wrapping it in
/// `sandbox-exec` on macOS when that binary is present (real network
/// denial — see module doc comment). Falls back to a plain, un-network-
/// isolated `python3` invocation everywhere else, and notes that honestly
/// in `appliedIsolationNotes` rather than silently claiming isolation that
/// didn't happen.
fn buildIsolatedPythonCommand(
    scriptPath: &std::path::Path,
    appliedIsolationNotes: &mut Vec<String>,
) -> Command {
    #[cfg(target_os = "macos")]
    {
        if std::path::Path::new("/usr/bin/sandbox-exec").exists() {
            appliedIsolationNotes.push(
                "Wrapped in macOS sandbox-exec with a network-denying Seatbelt profile (real, but Apple-deprecated — see module doc comment).".to_string(),
            );
            let mut command = Command::new("/usr/bin/sandbox-exec");
            command
                .arg("-p")
                .arg(NETWORK_DENYING_SEATBELT_PROFILE)
                .arg("python3")
                .arg(scriptPath);
            return command;
        }
    }
    appliedIsolationNotes.push(
        "No OS-level network isolation applied on this platform — only CPU-time/memory/wall-clock caps are in effect (see module doc comment).".to_string(),
    );
    let mut command = Command::new("python3");
    command.arg(scriptPath);
    command
}

#[cfg(target_os = "macos")]
const NETWORK_DENYING_SEATBELT_PROFILE: &str = r#"
(version 1)
(deny default)
(allow process-fork)
(allow process-exec)
(allow file-read*)
(allow file-write* (subpath "/tmp") (subpath "/private/tmp") (subpath "/private/var/folders"))
(allow sysctl-read)
(allow mach-lookup)
(deny network*)
"#;

/// Runs INSIDE the forked child process before `execve`, per
/// `std::os::unix::process::CommandExt::pre_exec`'s safety contract (async-
/// signal-safe operations only — `setrlimit` is on the POSIX async-signal-
/// safe list, so this is sound).
fn applyPosixResourceLimitsInChildProcess(
    cpuTimeLimitSeconds: u64,
    addressSpaceLimitBytes: u64,
) -> std::io::Result<()> {
    let cpuLimit = libc::rlimit {
        rlim_cur: cpuTimeLimitSeconds,
        rlim_max: cpuTimeLimitSeconds,
    };
    if unsafe { libc::setrlimit(libc::RLIMIT_CPU, &cpuLimit) } != 0 {
        return Err(std::io::Error::last_os_error());
    }

    let addressSpaceLimit = libc::rlimit {
        rlim_cur: addressSpaceLimitBytes,
        rlim_max: addressSpaceLimitBytes,
    };
    // Deliberately not propagating an error here on failure — RLIMIT_AS is
    // rejected outright on some platforms/configurations, and the CPU-time
    // cap above is the primary defense (see module doc comment), so a
    // failure to set the best-effort memory cap shouldn't prevent the
    // script from running at all.
    let _ = unsafe { libc::setrlimit(libc::RLIMIT_AS, &addressSpaceLimit) };

    Ok(())
}

// wait_timeout is provided as a trait method by the `wait-timeout` crate.
use wait_timeout::ChildExt;

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn trivialScriptRunsToCompletionAndCapturesStdout() {
        let outcome = spawnAndAwaitSandboxedPythonHook(
            "print('hello from the sandbox')",
            &PythonHookResourceLimits::default(),
        )
        .expect("subprocess should spawn");
        assert_eq!(
            outcome.terminationReason,
            PythonHookTerminationReason::ExitedNormally
        );
        assert_eq!(outcome.exitStatusCode, Some(0));
        assert!(outcome
            .standardOutputText
            .contains("hello from the sandbox"));
    }

    #[test]
    fn nonZeroExitCodeIsReportedFaithfully() {
        let outcome = spawnAndAwaitSandboxedPythonHook(
            "import sys; sys.exit(7)",
            &PythonHookResourceLimits::default(),
        )
        .expect("subprocess should spawn");
        assert_eq!(
            outcome.terminationReason,
            PythonHookTerminationReason::ExitedNormally
        );
        assert_eq!(outcome.exitStatusCode, Some(7));
    }
}
