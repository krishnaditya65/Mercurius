"use client";

// Mercurius / web — AI research copilot (FEATURES.md §16, item 2), wired
// against quant-engine's real `researchCopilotRetrievalAugmentedGeneration`
// endpoint on :8085:
//
//   POST /research/copilot/ask  {query, topK?}
//     -> {query, disclaimer, composedAnswerText, retrievedChunks: [{documentId, documentTitle, chunkIndex, text, cosineSimilarityScore}]}
//
// A real TF-IDF + cosine-similarity retrieval pipeline over a small
// hand-authored SYNTHETIC ("SIM-"-prefixed) filing/earnings-call corpus —
// explicitly NOT a generative LLM, and every response carries a fixed
// non-advisory disclaimer (rendered verbatim below, never paraphrased).
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

import { useState } from "react";
import Link from "next/link";

const quantEngineBaseUrl = process.env.NEXT_PUBLIC_QUANT_ENGINE_BASE_URL ?? "http://localhost:8085";

type RetrievedChunk = {
  documentId: string;
  documentTitle: string;
  chunkIndex: number;
  text: string;
  cosineSimilarityScore: number;
};

type CopilotResponse = {
  query: string;
  disclaimer: string;
  composedAnswerText: string;
  retrievedChunks: RetrievedChunk[];
};

const EXAMPLE_QUERIES = ["revenue growth", "litigation risk", "manufacturing capacity", "supply chain"];

export default function ResearchCopilotPage() {
  const [query, setQuery] = useState("revenue growth");
  const [topK, setTopK] = useState(2);
  const [response, setResponse] = useState<CopilotResponse | null>(null);
  const [isAsking, setIsAsking] = useState(false);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);

  async function askCopilot() {
    setIsAsking(true);
    setErrorMessage(null);
    try {
      const httpResponse = await fetch(`${quantEngineBaseUrl}/research/copilot/ask`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ query, topK }),
      });
      const bodyText = await httpResponse.text();
      if (!httpResponse.ok) throw new Error(bodyText || `HTTP ${httpResponse.status}`);
      setResponse(JSON.parse(bodyText));
    } catch (thrownError) {
      setErrorMessage(
        thrownError instanceof Error
          ? `Couldn't reach quant-engine: ${thrownError.message}. Is it running on ${quantEngineBaseUrl}?`
          : "Unknown error asking the research copilot."
      );
    } finally {
      setIsAsking(false);
    }
  }

  return (
    <main className="mx-auto flex max-w-2xl flex-col gap-8 p-8 font-sans">
      <div>
        <Link href="/" className="text-sm text-neutral-500 underline">
          ← Back to dashboard
        </Link>
        <h1 className="text-xl font-semibold">AI research copilot</h1>
        <p className="text-sm text-neutral-500">
          Backed by quant-engine&apos;s real TF-IDF retrieval-augmented pipeline on :8085, over a small synthetic
          (&quot;SIM-&quot;-prefixed) filing/earnings-call corpus. Extractive, template-composed answers with exact
          citations — not a generative LLM.
        </p>
      </div>

      {errorMessage && <p className="text-sm text-red-600">{errorMessage}</p>}

      <section className="flex flex-col gap-3 rounded border border-neutral-200 p-4">
        <label className="flex flex-col gap-1 text-sm">
          Ask a question
          <input
            className="rounded border px-3 py-2"
            value={query}
            onChange={(changeEvent) => setQuery(changeEvent.target.value)}
          />
        </label>
        <div className="flex flex-wrap gap-2 text-xs">
          {EXAMPLE_QUERIES.map((exampleQuery) => (
            <button
              key={exampleQuery}
              type="button"
              className="rounded border px-2 py-1 text-neutral-600"
              onClick={() => setQuery(exampleQuery)}
            >
              {exampleQuery}
            </button>
          ))}
        </div>
        <label className="flex flex-col gap-1 text-sm">
          Top K chunks
          <input
            type="number"
            min={1}
            max={10}
            className="w-24 rounded border px-3 py-2"
            value={topK}
            onChange={(changeEvent) => setTopK(Number(changeEvent.target.value))}
          />
        </label>
        <button
          type="button"
          disabled={isAsking || !query.trim()}
          onClick={askCopilot}
          className="self-start rounded bg-black px-4 py-2 text-sm text-white disabled:opacity-50"
        >
          {isAsking ? "Asking…" : "Ask"}
        </button>
      </section>

      {response && (
        <>
          <section className="rounded border border-amber-200 bg-amber-50 p-4 text-sm text-amber-900">
            <p className="font-medium">Disclaimer</p>
            <p>{response.disclaimer}</p>
          </section>

          <section className="flex flex-col gap-2 rounded border border-neutral-200 p-4">
            <h2 className="text-lg font-medium">Answer</h2>
            <p className="whitespace-pre-wrap text-sm">{response.composedAnswerText}</p>
          </section>

          <section className="flex flex-col gap-3 rounded border border-neutral-200 p-4">
            <h2 className="text-lg font-medium">Retrieved chunks (cited sources)</h2>
            <ul className="flex flex-col gap-2">
              {response.retrievedChunks.map((chunk) => (
                <li key={`${chunk.documentId}-${chunk.chunkIndex}`} className="rounded border border-neutral-100 p-3 text-sm">
                  <p className="font-medium">
                    {chunk.documentTitle} <span className="text-neutral-400">(chunk {chunk.chunkIndex})</span>
                  </p>
                  <p className="text-xs text-neutral-500">relevance {chunk.cosineSimilarityScore.toFixed(3)}</p>
                  <p className="mt-1">{chunk.text}</p>
                </li>
              ))}
            </ul>
          </section>
        </>
      )}
    </main>
  );
}
