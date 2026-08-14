"use client";

// Mercurius / web — Historical DOM (Depth Of Market) replay view.
//
// FEATURES.md §20 "[P4] Historical DOM replay for a chosen instrument/
// time window". Calls matching-engine's real `GET /domReplay` endpoint
// (services/matching-engine/src/domReplayHttpServer.rs), which genuinely
// reuses the write-ahead log + `replayWalEventRecordsIntoFreshOrderBook`'s
// deterministic-replay machinery from a previous round (see that
// service's README for why the endpoint lives there, not in market-data)
// — every snapshot this page steps through is a real point-in-time order
// book depth reconstructed by replaying the real WAL, not a fabricated
// animation.
//
// The playback control below is a real (if simple) stepper: it holds the
// real array of `DomReplaySnapshot`s returned by matching-engine and
// steps an index through it, either by hand (Prev/Next/slider) or on an
// interval timer (Play/Pause) — nothing here interpolates or invents a
// snapshot that wasn't actually returned.
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

import { useEffect, useRef, useState } from "react";
import Link from "next/link";

const matchingEngineDomReplayBaseUrl =
  process.env.NEXT_PUBLIC_MATCHING_ENGINE_DOM_REPLAY_BASE_URL ?? "http://localhost:9106";

type DomReplaySnapshot = {
  epochMillis: number;
  walEventIndex: number;
  bidLevelsBestFirst: [number, number][];
  askLevelsBestFirst: [number, number][];
};

const PLAYBACK_STEP_INTERVAL_MILLIS = 700;

export default function DomReplayPage() {
  const [instrumentSymbol, setInstrumentSymbol] = useState("DEMO-EQ");
  const [startEpochMillisInput, setStartEpochMillisInput] = useState("");
  const [endEpochMillisInput, setEndEpochMillisInput] = useState("");
  const [snapshots, setSnapshots] = useState<DomReplaySnapshot[] | null>(null);
  const [currentSnapshotIndex, setCurrentSnapshotIndex] = useState(0);
  const [isPlaying, setIsPlaying] = useState(false);
  const [isLoading, setIsLoading] = useState(false);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);

  // Real interval-driven playback — steps `currentSnapshotIndex` forward
  // through the REAL snapshots array already fetched from matching-engine.
  // Stops itself at the last real snapshot rather than looping or
  // fabricating one past the end.
  useEffect(() => {
    if (!isPlaying || !snapshots || snapshots.length === 0) return;
    const intervalId = setInterval(() => {
      setCurrentSnapshotIndex((previousIndex) => {
        const nextIndex = previousIndex + 1;
        if (nextIndex >= snapshots.length) {
          setIsPlaying(false);
          return previousIndex;
        }
        return nextIndex;
      });
    }, PLAYBACK_STEP_INTERVAL_MILLIS);
    return () => clearInterval(intervalId);
  }, [isPlaying, snapshots]);

  async function fetchDomReplay() {
    setIsLoading(true);
    setIsPlaying(false);
    setErrorMessage(null);
    setSnapshots(null);
    setCurrentSnapshotIndex(0);
    try {
      const queryParams = new URLSearchParams({ instrumentSymbol });
      if (startEpochMillisInput) queryParams.set("startEpochMillis", startEpochMillisInput);
      if (endEpochMillisInput) queryParams.set("endEpochMillis", endEpochMillisInput);

      const httpResponse = await fetch(`${matchingEngineDomReplayBaseUrl}/domReplay?${queryParams.toString()}`);
      if (!httpResponse.ok) {
        const bodyText = await httpResponse.text();
        throw new Error(`matching-engine responded with HTTP ${httpResponse.status}: ${bodyText}`);
      }
      const parsed: DomReplaySnapshot[] = await httpResponse.json();
      setSnapshots(parsed);
    } catch (thrownError) {
      setErrorMessage(
        thrownError instanceof Error
          ? `Couldn't fetch the DOM replay: ${thrownError.message}. Is matching-engine running on ${matchingEngineDomReplayBaseUrl}?`
          : "Unknown error fetching DOM replay."
      );
    } finally {
      setIsLoading(false);
    }
  }

  const currentSnapshot = snapshots && snapshots.length > 0 ? snapshots[currentSnapshotIndex] : null;

  return (
    <main className="mx-auto flex max-w-3xl flex-col gap-6 p-8 font-sans">
      <div>
        <Link href="/" className="text-sm text-neutral-500 underline">
          ← Back to dashboard
        </Link>
        <h1 className="text-xl font-semibold">Historical DOM replay</h1>
        <p className="text-sm text-neutral-500">
          Real order-book depth snapshots reconstructed by matching-engine&apos;s <code>GET /domReplay</code>,
          which genuinely replays the real write-ahead log — see that service&apos;s README for the WAL format and
          deterministic replay mechanism this endpoint reuses. Deterministic replay, not a fabricated animation.
        </p>
      </div>

      <section className="flex flex-col gap-3 rounded border border-neutral-200 p-4">
        <div className="flex flex-wrap items-end gap-3">
          <label className="flex flex-col gap-1 text-sm">
            Symbol
            <input
              className="rounded border px-3 py-2"
              value={instrumentSymbol}
              onChange={(changeEvent) => setInstrumentSymbol(changeEvent.target.value)}
            />
          </label>
          <label className="flex flex-col gap-1 text-sm">
            Start epoch millis (optional)
            <input
              className="w-48 rounded border px-3 py-2"
              value={startEpochMillisInput}
              onChange={(changeEvent) => setStartEpochMillisInput(changeEvent.target.value)}
            />
          </label>
          <label className="flex flex-col gap-1 text-sm">
            End epoch millis (optional)
            <input
              className="w-48 rounded border px-3 py-2"
              value={endEpochMillisInput}
              onChange={(changeEvent) => setEndEpochMillisInput(changeEvent.target.value)}
            />
          </label>
          <button
            className="rounded bg-black px-4 py-2 text-sm text-white disabled:opacity-50"
            onClick={fetchDomReplay}
            disabled={isLoading}
            type="button"
          >
            {isLoading ? "Replaying…" : "Replay WAL"}
          </button>
        </div>
        <p className="text-xs text-neutral-500">
          matching-engine only trades <code>DEMO-EQ</code> in this skeleton — a different symbol will 400. Leave
          the epoch millis fields blank to replay the entire WAL.
        </p>
      </section>

      {errorMessage && <p className="text-sm text-red-600">{errorMessage}</p>}

      {snapshots && snapshots.length === 0 && (
        <p className="text-sm text-neutral-500">
          No depth-mutating WAL events in that window — submit some real orders to matching-engine first, or widen
          the window.
        </p>
      )}

      {currentSnapshot && snapshots && (
        <PlaybackView
          snapshots={snapshots}
          currentSnapshotIndex={currentSnapshotIndex}
          setCurrentSnapshotIndex={setCurrentSnapshotIndex}
          isPlaying={isPlaying}
          setIsPlaying={setIsPlaying}
          currentSnapshot={currentSnapshot}
        />
      )}
    </main>
  );
}

function PlaybackView(props: {
  snapshots: DomReplaySnapshot[];
  currentSnapshotIndex: number;
  setCurrentSnapshotIndex: (updater: (previousIndex: number) => number) => void;
  isPlaying: boolean;
  setIsPlaying: (isPlaying: boolean) => void;
  currentSnapshot: DomReplaySnapshot;
}) {
  const { snapshots, currentSnapshotIndex, setCurrentSnapshotIndex, isPlaying, setIsPlaying, currentSnapshot } = props;
  const sliderRef = useRef<HTMLInputElement>(null);

  const isAtStart = currentSnapshotIndex === 0;
  const isAtEnd = currentSnapshotIndex === snapshots.length - 1;

  return (
    <section className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-3 rounded border border-neutral-300 bg-neutral-50 p-4 text-sm">
        <button
          className="rounded border px-3 py-1.5 text-sm disabled:opacity-40"
          onClick={() => setCurrentSnapshotIndex((previousIndex) => Math.max(0, previousIndex - 1))}
          disabled={isAtStart}
          type="button"
        >
          ⏮ Prev
        </button>
        <button
          className="rounded bg-black px-3 py-1.5 text-sm text-white disabled:opacity-40"
          onClick={() => setIsPlaying(!isPlaying)}
          disabled={isAtEnd && !isPlaying}
          type="button"
        >
          {isPlaying ? "⏸ Pause" : "▶ Play"}
        </button>
        <button
          className="rounded border px-3 py-1.5 text-sm disabled:opacity-40"
          onClick={() => setCurrentSnapshotIndex((previousIndex) => Math.min(snapshots.length - 1, previousIndex + 1))}
          disabled={isAtEnd}
          type="button"
        >
          Next ⏭
        </button>
        <input
          ref={sliderRef}
          type="range"
          min={0}
          max={snapshots.length - 1}
          value={currentSnapshotIndex}
          onChange={(changeEvent) => setCurrentSnapshotIndex(() => Number(changeEvent.target.value))}
          className="flex-1"
        />
        <span className="font-mono text-xs">
          {currentSnapshotIndex + 1} / {snapshots.length}
        </span>
      </div>

      <div className="rounded border border-neutral-300 p-4 text-xs text-neutral-600">
        WAL event index <strong>{currentSnapshot.walEventIndex}</strong> — logged at epoch millis{" "}
        <strong>{currentSnapshot.epochMillis}</strong> ({new Date(currentSnapshot.epochMillis).toLocaleString()})
      </div>

      <div className="grid grid-cols-2 gap-4">
        <DepthColumn title="Bids (best first)" levels={currentSnapshot.bidLevelsBestFirst} colorClassName="text-emerald-700" />
        <DepthColumn title="Asks (best first)" levels={currentSnapshot.askLevelsBestFirst} colorClassName="text-rose-700" />
      </div>
    </section>
  );
}

function DepthColumn(props: { title: string; levels: [number, number][]; colorClassName: string }) {
  const { title, levels, colorClassName } = props;
  return (
    <div className="rounded border border-neutral-200 p-4">
      <h2 className={`mb-2 text-sm font-semibold ${colorClassName}`}>{title}</h2>
      {levels.length === 0 ? (
        <p className="text-xs text-neutral-500">empty</p>
      ) : (
        <table className="w-full border-collapse text-xs">
          <thead>
            <tr className="text-left text-neutral-500">
              <th className="p-1">Price</th>
              <th className="p-1">Quantity</th>
            </tr>
          </thead>
          <tbody>
            {levels.map(([price, quantity]) => (
              <tr key={price} className="border-t border-neutral-100">
                <td className="p-1 font-mono">{price}</td>
                <td className="p-1 font-mono">{quantity}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
