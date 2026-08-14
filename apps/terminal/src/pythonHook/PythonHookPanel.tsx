// Mercurius / terminal — Python hook sandbox panel.
//
// FEATURES.md §10 "[P3] Local Python hook sandbox for algo traders
// (isolated subprocess, resource-capped)". This is the frontend half —
// a real editor buffer + real Tauri `invoke("runSandboxedPythonHook", ...)`
// call into `src-tauri/src/pythonHookSandbox.rs`. See that Rust module's
// header comment for exactly what's genuinely enforced vs. best-effort;
// this panel surfaces `appliedIsolationNotes` verbatim so a trader running
// their own hook script sees the same honesty this codebase holds itself
// to, not a false "fully sandboxed" claim.
//
// HONEST LIMITATION: `invoke` only resolves against a running Tauri
// backend (i.e. inside `tauri dev`/a built app) — there's no Tauri runtime
// in a plain browser tab or in this Vitest/jsdom test environment, so this
// component cannot be exercised end-to-end here. The Rust side it calls
// IS exercised end-to-end by `src-tauri/tests/pythonHookSandboxIntegrationTest.rs`
// (see that file — real subprocesses, real resource-limit assertions).
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

import { useState } from "react";

const DEFAULT_HOOK_SCRIPT = `# Example algo-trading "hook" — read this file's sandbox module for what
# is/isn't actually enforced around this script (CPU-time cap is real and
# kernel-enforced; network isolation is attempted via macOS sandbox-exec
# when available; this script never places a real order — see
# services/quant-engine/illustrativeSentimentTradingHook.py for why wiring
# a hook's suggestion to a real order path is explicitly out of scope).
print("hook running")
signal = "HOLD"
print(f"suggested action: {signal}")
`;

type PythonHookExecutionOutcome = {
  terminationReason:
    | "ExitedNormally"
    | "KilledByWallClockWatchdog"
    | { KilledBySignal: number };
  exitStatusCode: number | null;
  standardOutputText: string;
  standardErrorText: string;
  appliedIsolationNotes: string[];
};

export function PythonHookPanel() {
  const [scriptSource, setScriptSource] = useState(DEFAULT_HOOK_SCRIPT);
  const [isRunning, setIsRunning] = useState(false);
  const [outcome, setOutcome] = useState<PythonHookExecutionOutcome | null>(null);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);

  async function handleRun() {
    setIsRunning(true);
    setErrorMessage(null);
    setOutcome(null);
    try {
      // Dynamically imported so this module still loads (for layout
      // registration purposes) outside a Tauri runtime — see the module
      // doc comment's honest limitation.
      const { invoke } = await import("@tauri-apps/api/core");
      const result = await invoke<PythonHookExecutionOutcome>("runSandboxedPythonHook", {
        pythonScriptSourceCode: scriptSource,
        resourceLimitOverrides: null,
      });
      setOutcome(result);
    } catch (thrownError) {
      setErrorMessage(
        thrownError instanceof Error
          ? thrownError.message
          : "Couldn't reach the Tauri backend — this panel only works inside a running Tauri app, not a plain browser tab."
      );
    } finally {
      setIsRunning(false);
    }
  }

  return (
    <div className="pythonHookPanel">
      <textarea
        className="pythonHookPanel__editor"
        value={scriptSource}
        onChange={(changeEvent) => setScriptSource(changeEvent.target.value)}
        spellCheck={false}
      />
      <div className="pythonHookPanel__controls">
        <button type="button" onClick={handleRun} disabled={isRunning}>
          {isRunning ? "Running (resource-capped)…" : "Run in sandbox"}
        </button>
      </div>
      {errorMessage && <div className="pythonHookPanel__error">{errorMessage}</div>}
      {outcome && (
        <div className="pythonHookPanel__outcome">
          <div>Termination: {describeTerminationReason(outcome.terminationReason)}</div>
          <div>Exit code: {outcome.exitStatusCode ?? "n/a"}</div>
          <pre className="pythonHookPanel__stdout">{outcome.standardOutputText}</pre>
          {outcome.standardErrorText && (
            <pre className="pythonHookPanel__stderr">{outcome.standardErrorText}</pre>
          )}
          <ul className="pythonHookPanel__isolationNotes">
            {outcome.appliedIsolationNotes.map((note, index) => (
              <li key={index}>{note}</li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}

function describeTerminationReason(reason: PythonHookExecutionOutcome["terminationReason"]): string {
  if (reason === "ExitedNormally") return "Exited normally";
  if (reason === "KilledByWallClockWatchdog") return "Killed — exceeded wall-clock time budget";
  return `Killed by signal ${reason.KilledBySignal} (e.g. 24 = SIGXCPU from the CPU-time cap)`;
}
