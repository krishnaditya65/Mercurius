// Mercurius / terminal (Pro Desktop) — Tauri v2 backend.
//
// The frontend (src/) is a real GoldenLayout tiling workspace + command bar
// + canvas candlestick charts + DOM ladder + news ticker, per FEATURES.md
// §10. This Rust side exposes exactly one genuinely native capability the
// web app (apps/web) can't offer: a resource-capped local Python "hook"
// sandbox for algo traders (`pythonHookSandbox.rs`) invoked as a real Tauri
// command below. Multi-monitor window detachment (also §10) is handled
// entirely on the frontend via `@tauri-apps/api/window`'s real WebviewWindow
// API — no Rust command needed for that one.
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention (overrides normal Rust snake_case idiom) — hence
// the crate-wide `#![allow(non_snake_case)]` below, matching
// services/matching-engine's precedent for the same convention.
#![allow(non_snake_case)]

pub mod pythonHookSandbox;

use pythonHookSandbox::{
    spawnAndAwaitSandboxedPythonHook, PythonHookExecutionOutcome, PythonHookResourceLimits,
};

/// Tauri command the frontend's Python-hook panel calls. Takes the hook's
/// Python source as a plain string (the terminal's UI is expected to hold
/// the trader's script in an editor buffer, not a file on disk) plus
/// optional overrides for the resource caps — see
/// `pythonHookSandbox::PythonHookResourceLimits` for defaults and honesty
/// notes on what's actually enforced on this platform.
#[tauri::command]
fn runSandboxedPythonHook(
    pythonScriptSourceCode: String,
    resourceLimitOverrides: Option<PythonHookResourceLimits>,
) -> Result<PythonHookExecutionOutcome, String> {
    let resourceLimits = resourceLimitOverrides.unwrap_or_default();
    spawnAndAwaitSandboxedPythonHook(&pythonScriptSourceCode, &resourceLimits)
        .map_err(|ioError| format!("failed to spawn sandboxed python hook: {ioError}"))
}

// NOTE on graceful degradation: this is a demo-grade internal terminal
// app, not a shipped consumer product, so we stop short of building a
// full native error-dialog/recovery flow around Tauri's own generated
// bootstrap. The minimum reasonable fix applied here: never let the
// process die with a raw Rust panic backtrace on startup failure. We log
// a clear, actionable message — including the most common real-world
// failure mode (WebView2 missing on Windows) — before exiting with a
// non-zero status, so whoever's staring at the terminal output knows what
// to do next instead of parsing a panic trace.
#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    let runResult = tauri::Builder::default()
        .plugin(tauri_plugin_opener::init())
        .invoke_handler(tauri::generate_handler![runSandboxedPythonHook])
        .run(tauri::generate_context!());

    if let Err(tauriError) = runResult {
        eprintln!(
            "Mercurius terminal failed to start: {tauriError}\n\
             This usually means the platform WebView runtime isn't available \
             (on Windows: install the WebView2 runtime from Microsoft; on \
             Linux: install webkit2gtk; on macOS this should be preinstalled). \
             Exiting."
        );
        std::process::exit(1);
    }
}
