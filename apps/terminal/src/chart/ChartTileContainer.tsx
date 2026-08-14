// Mercurius / terminal — chart tile: polls market-data's real HTTP query
// API (same one apps/web's PriceChartSection uses, read-only reference)
// and renders CandlestickChartCanvas with toggleable indicator overlays.
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

import { useEffect, useState } from "react";
import { CandlestickChartCanvas, type CandleBar, type IndicatorOverlayToggles } from "./CandlestickChartCanvas";

const CHART_POLL_INTERVAL_MILLISECONDS = 5_000;

const DEFAULT_OVERLAYS: IndicatorOverlayToggles = {
  showBollingerBands: true,
  showFibonacciRetracement: false,
  showMacdPane: true,
  showRsiPane: true,
};

export function ChartTileContainer(props: { marketDataBaseUrl: string; instrumentSymbol: string }) {
  const [candles, setCandles] = useState<CandleBar[]>([]);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);
  const [overlays, setOverlays] = useState<IndicatorOverlayToggles>(DEFAULT_OVERLAYS);

  useEffect(() => {
    let cancelled = false;

    async function refreshChartData() {
      try {
        const httpResponse = await fetch(
          `${props.marketDataBaseUrl}/candles?instrumentSymbol=${encodeURIComponent(props.instrumentSymbol)}&limit=100`
        );
        if (!httpResponse.ok) throw new Error(`HTTP ${httpResponse.status}`);
        const parsedCandles: CandleBar[] = await httpResponse.json();
        if (cancelled) return;
        setCandles(parsedCandles);
        setErrorMessage(null);
      } catch (thrownError) {
        if (cancelled) return;
        setErrorMessage(
          thrownError instanceof Error
            ? `Couldn't reach market-data: ${thrownError.message}. Is it running on ${props.marketDataBaseUrl}?`
            : "Unknown error fetching candles."
        );
      }
    }

    // eslint-disable-next-line react-hooks/set-state-in-effect -- see apps/web's PriceChartSection: state updates happen after an await, not synchronously.
    refreshChartData();
    const intervalId = setInterval(refreshChartData, CHART_POLL_INTERVAL_MILLISECONDS);
    return () => {
      cancelled = true;
      clearInterval(intervalId);
    };
  }, [props.marketDataBaseUrl, props.instrumentSymbol]);

  return (
    <div className="chartTileContainer">
      <div className="chartTileContainer__toolbar">
        <OverlayToggle
          label="BB"
          checked={overlays.showBollingerBands}
          onChange={(checked) => setOverlays((prev) => ({ ...prev, showBollingerBands: checked }))}
        />
        <OverlayToggle
          label="Fib"
          checked={overlays.showFibonacciRetracement}
          onChange={(checked) => setOverlays((prev) => ({ ...prev, showFibonacciRetracement: checked }))}
        />
        <OverlayToggle
          label="MACD"
          checked={overlays.showMacdPane}
          onChange={(checked) => setOverlays((prev) => ({ ...prev, showMacdPane: checked }))}
        />
        <OverlayToggle
          label="RSI"
          checked={overlays.showRsiPane}
          onChange={(checked) => setOverlays((prev) => ({ ...prev, showRsiPane: checked }))}
        />
      </div>
      {errorMessage && <div className="chartTileContainer__error">{errorMessage}</div>}
      <div className="chartTileContainer__canvasWrapper">
        <CandlestickChartCanvas candles={candles} overlays={overlays} />
      </div>
    </div>
  );
}

function OverlayToggle(props: { label: string; checked: boolean; onChange: (checked: boolean) => void }) {
  return (
    <label className="chartTileContainer__overlayToggle">
      <input type="checkbox" checked={props.checked} onChange={(e) => props.onChange(e.target.checked)} />
      {props.label}
    </label>
  );
}
