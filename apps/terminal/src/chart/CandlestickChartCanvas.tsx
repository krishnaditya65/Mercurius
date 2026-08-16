// Mercurius / terminal — hand-rolled Canvas 2D candlestick chart with
// indicator overlays. FEATURES.md §10 "[P2] WebGL/Canvas candlestick
// charts with indicator overlays (MACD, RSI, BB, Fib)".
//
// RENDERING CHOICE (documented per the task brief): a hand-rolled Canvas
// 2D renderer, not a charting library and not raw WebGL. This mirrors
// apps/web's own PriceChartSection comment ("don't reach for a framework
// until there's a real reason to" — matching-engine/market-data's
// hand-rolled TCP/HTTP bridges, quant-engine's stdlib-only HTTP server),
// upgraded from apps/web's SVG renderer to a genuine `<canvas>` element
// with real `CanvasRenderingContext2D` drawing calls (`fillRect`,
// `beginPath`/`stroke`, `fillText`) — this is real Canvas rendering, not a
// simulation of one, and it's what FEATURES.md §10 asks for ("Canvas
// candlestick charts"). A full WebGL renderer (raw buffers/shaders) would
// buy meaningfully more only at candle counts far beyond what a single
// workspace tile realistically displays, so it isn't built here — the
// module boundary below (indicator MATH in src/indicators/, independent of
// this rendering module) is exactly what would let a future WebGL renderer
// swap in without touching a single indicator formula.
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

import { useEffect, useRef } from "react";
import { calculateMovingAverageConvergenceDivergence } from "../indicators/macdIndicatorCalculator";
import { calculateRelativeStrengthIndex } from "../indicators/rsiIndicatorCalculator";
import { calculateBollingerBands } from "../indicators/bollingerBandsCalculator";
import {
  calculateFibonacciRetracementLevels,
  type FibonacciRetracementLevel,
} from "../indicators/fibonacciRetracementCalculator";

export type CandleBar = {
  instrumentSymbol: string;
  bucketStartEpochSeconds: number;
  openPriceInMinorUnits: number;
  highPriceInMinorUnits: number;
  lowPriceInMinorUnits: number;
  closePriceInMinorUnits: number;
  totalVolume: number;
};

export type IndicatorOverlayToggles = {
  showBollingerBands: boolean;
  showFibonacciRetracement: boolean;
  showMacdPane: boolean;
  showRsiPane: boolean;
};

const BULLISH_CANDLE_COLOR = "#26a65b";
const BEARISH_CANDLE_COLOR = "#e0473d";
const GRID_LINE_COLOR = "rgba(148, 163, 184, 0.18)";
const AXIS_TEXT_COLOR = "#94a3b8";
const BOLLINGER_BAND_COLOR = "rgba(96, 165, 250, 0.85)";
const FIBONACCI_LINE_COLOR = "rgba(250, 204, 21, 0.65)";
const MACD_LINE_COLOR = "#60a5fa";
const SIGNAL_LINE_COLOR = "#f97316";
const RSI_LINE_COLOR = "#a78bfa";

/** Real Canvas 2D candlestick renderer with optional Bollinger Bands /
 * Fibonacci retracement overlaid on the price pane, and optional MACD /
 * RSI sub-panes stacked below it. Every indicator value plotted here comes
 * from `src/indicators/*` — no indicator math lives in this file. */
export function CandlestickChartCanvas(props: {
  candles: CandleBar[];
  overlays: IndicatorOverlayToggles;
}) {
  const canvasRef = useRef<HTMLCanvasElement>(null);

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const renderingContext = canvas.getContext("2d");
    if (!renderingContext) return;

    renderChartOntoCanvas(renderingContext, canvas, props.candles, props.overlays);
  }, [props.candles, props.overlays]);

  return (
    <canvas
      ref={canvasRef}
      width={960}
      height={480}
      role="img"
      aria-label={`Candlestick chart${props.candles[0] ? ` for ${props.candles[0].instrumentSymbol}` : ""}`}
      style={{ width: "100%", height: "100%", display: "block", background: "#0b1120" }}
    />
  );
}

function renderChartOntoCanvas(
  ctx: CanvasRenderingContext2D,
  canvas: HTMLCanvasElement,
  candles: CandleBar[],
  overlays: IndicatorOverlayToggles
): void {
  const canvasWidth = canvas.width;
  const canvasHeight = canvas.height;
  ctx.clearRect(0, 0, canvasWidth, canvasHeight);
  ctx.fillStyle = "#0b1120";
  ctx.fillRect(0, 0, canvasWidth, canvasHeight);

  if (candles.length === 0) {
    ctx.fillStyle = AXIS_TEXT_COLOR;
    ctx.font = "13px monospace";
    ctx.fillText("No candles yet.", 16, 24);
    return;
  }

  const macdPaneHeight = overlays.showMacdPane ? 90 : 0;
  const rsiPaneHeight = overlays.showRsiPane ? 70 : 0;
  const pricePaneHeight = canvasHeight - macdPaneHeight - rsiPaneHeight;

  const closingPrices = candles.map((candle) => candle.closePriceInMinorUnits);

  drawPricePane(ctx, candles, canvasWidth, pricePaneHeight, overlays);

  let nextPaneTop = pricePaneHeight;
  if (overlays.showMacdPane) {
    drawMacdPane(ctx, closingPrices, canvasWidth, nextPaneTop, macdPaneHeight);
    nextPaneTop += macdPaneHeight;
  }
  if (overlays.showRsiPane) {
    drawRsiPane(ctx, closingPrices, canvasWidth, nextPaneTop, rsiPaneHeight);
  }
}

/** `calculateFibonacciRetracementLevels` throws when the visible range is
 * flat (swingHigh <= swingLow, e.g. every candle in this window traded at
 * the exact same price). Callers must check this BEFORE calling into it
 * rather than wrapping in try/catch, so a render pass never calls a
 * function known to throw — pure function, exported for unit testing. */
export function shouldRenderFibonacciOverlay(
  showFibonacciRetracement: boolean,
  swingHigh: number,
  swingLow: number
): boolean {
  return showFibonacciRetracement && swingHigh > swingLow;
}

function drawPricePane(
  ctx: CanvasRenderingContext2D,
  candles: CandleBar[],
  paneWidth: number,
  paneHeight: number,
  overlays: IndicatorOverlayToggles
): void {
  const bollinger = overlays.showBollingerBands ? calculateBollingerBands(candles.map((c) => c.closePriceInMinorUnits)) : null;

  const swingHigh = Math.max(...candles.map((c) => c.highPriceInMinorUnits));
  const swingLow = Math.min(...candles.map((c) => c.lowPriceInMinorUnits));
  const canRenderFibonacciOverlay = shouldRenderFibonacciOverlay(
    overlays.showFibonacciRetracement,
    swingHigh,
    swingLow
  );
  const fibonacciLevels = canRenderFibonacciOverlay
    ? calculateFibonacciRetracementLevels(swingHigh, swingLow, "uptrend")
    : null;

  let paneLowestPrice = swingLow;
  let paneHighestPrice = swingHigh;
  if (bollinger && bollinger.lowerBand.length > 0) {
    paneLowestPrice = Math.min(paneLowestPrice, ...bollinger.lowerBand);
    paneHighestPrice = Math.max(paneHighestPrice, ...bollinger.upperBand);
  }
  const priceRange = Math.max(1, paneHighestPrice - paneLowestPrice);
  const priceToY = (price: number) =>
    paneHeight - ((price - paneLowestPrice) / priceRange) * (paneHeight - 20) - 10;

  drawHorizontalGridLines(ctx, paneWidth, paneHeight, 4);

  const candleSlotWidth = paneWidth / candles.length;
  const candleBodyWidth = Math.max(2, candleSlotWidth * 0.6);

  candles.forEach((candle, index) => {
    const isBullish = candle.closePriceInMinorUnits >= candle.openPriceInMinorUnits;
    const candleColor = isBullish ? BULLISH_CANDLE_COLOR : BEARISH_CANDLE_COLOR;
    const slotCenterX = index * candleSlotWidth + candleSlotWidth / 2;

    ctx.strokeStyle = candleColor;
    ctx.lineWidth = 1;
    ctx.beginPath();
    ctx.moveTo(slotCenterX, priceToY(candle.highPriceInMinorUnits));
    ctx.lineTo(slotCenterX, priceToY(candle.lowPriceInMinorUnits));
    ctx.stroke();

    const bodyTopY = priceToY(Math.max(candle.openPriceInMinorUnits, candle.closePriceInMinorUnits));
    const bodyBottomY = priceToY(Math.min(candle.openPriceInMinorUnits, candle.closePriceInMinorUnits));
    ctx.fillStyle = candleColor;
    ctx.fillRect(
      slotCenterX - candleBodyWidth / 2,
      bodyTopY,
      candleBodyWidth,
      Math.max(1, bodyBottomY - bodyTopY)
    );
  });

  if (bollinger) {
    drawAlignedLineSeries(ctx, bollinger.upperBand, candles.length, candleSlotWidth, priceToY, BOLLINGER_BAND_COLOR);
    drawAlignedLineSeries(ctx, bollinger.middleBand, candles.length, candleSlotWidth, priceToY, BOLLINGER_BAND_COLOR, 0.4);
    drawAlignedLineSeries(ctx, bollinger.lowerBand, candles.length, candleSlotWidth, priceToY, BOLLINGER_BAND_COLOR);
  }

  if (fibonacciLevels) {
    drawFibonacciLevels(ctx, fibonacciLevels, paneWidth, priceToY);
  } else if (overlays.showFibonacciRetracement) {
    // Fib was requested but skipped (flat price range) — a small,
    // non-intrusive note rather than silently doing nothing, so it's
    // clear the overlay isn't just slow to appear.
    ctx.save();
    ctx.fillStyle = AXIS_TEXT_COLOR;
    ctx.font = "10px monospace";
    ctx.fillText("Fib unavailable for flat price range", 6, paneHeight - 6);
    ctx.restore();
  }
}

/** Draws a computed indicator series (e.g. a Bollinger band) whose first
 * value corresponds to the END of the underlying data (see the indicator
 * modules' own alignment docs) — right-aligns the series against the
 * candle slots accordingly. */
function drawAlignedLineSeries(
  ctx: CanvasRenderingContext2D,
  series: number[],
  totalCandleCount: number,
  candleSlotWidth: number,
  priceToY: (price: number) => number,
  strokeStyle: string,
  alphaOverride?: number
): void {
  if (series.length === 0) return;
  const startCandleIndex = totalCandleCount - series.length;
  ctx.save();
  ctx.strokeStyle = strokeStyle;
  ctx.lineWidth = 1.25;
  if (alphaOverride !== undefined) ctx.globalAlpha = alphaOverride;
  ctx.beginPath();
  series.forEach((value, seriesIndex) => {
    const candleIndex = startCandleIndex + seriesIndex;
    const x = candleIndex * candleSlotWidth + candleSlotWidth / 2;
    const y = priceToY(value);
    if (seriesIndex === 0) ctx.moveTo(x, y);
    else ctx.lineTo(x, y);
  });
  ctx.stroke();
  ctx.restore();
}

function drawFibonacciLevels(
  ctx: CanvasRenderingContext2D,
  levels: FibonacciRetracementLevel[],
  paneWidth: number,
  priceToY: (price: number) => number
): void {
  ctx.save();
  ctx.strokeStyle = FIBONACCI_LINE_COLOR;
  ctx.fillStyle = FIBONACCI_LINE_COLOR;
  ctx.font = "10px monospace";
  ctx.setLineDash([4, 4]);
  for (const level of levels) {
    const y = priceToY(level.price);
    ctx.beginPath();
    ctx.moveTo(0, y);
    ctx.lineTo(paneWidth, y);
    ctx.stroke();
    ctx.fillText(`${(level.ratio * 100).toFixed(1)}%`, paneWidth - 40, y - 2);
  }
  ctx.restore();
}

function drawMacdPane(
  ctx: CanvasRenderingContext2D,
  closingPrices: number[],
  paneWidth: number,
  paneTop: number,
  paneHeight: number
): void {
  ctx.save();
  ctx.translate(0, paneTop);
  drawPaneLabel(ctx, "MACD (12,26,9)");

  const { macdLine, signalLine } = calculateMovingAverageConvergenceDivergence(closingPrices);
  if (macdLine.length === 0) {
    ctx.restore();
    return;
  }
  const allValues = [...macdLine, ...signalLine];
  const maxAbs = Math.max(1e-9, ...allValues.map((value) => Math.abs(value)));
  const valueToY = (value: number) => paneHeight / 2 - (value / maxAbs) * (paneHeight / 2 - 8);

  const candleSlotWidth = paneWidth / closingPrices.length;
  drawAlignedLineSeries(ctx, macdLine, closingPrices.length, candleSlotWidth, valueToY, MACD_LINE_COLOR);
  drawAlignedLineSeries(ctx, signalLine, closingPrices.length, candleSlotWidth, valueToY, SIGNAL_LINE_COLOR);
  ctx.restore();
}

function drawRsiPane(
  ctx: CanvasRenderingContext2D,
  closingPrices: number[],
  paneWidth: number,
  paneTop: number,
  paneHeight: number
): void {
  ctx.save();
  ctx.translate(0, paneTop);
  drawPaneLabel(ctx, "RSI (14)");

  const rsiValues = calculateRelativeStrengthIndex(closingPrices);
  if (rsiValues.length === 0) {
    ctx.restore();
    return;
  }
  const valueToY = (value: number) => paneHeight - (value / 100) * (paneHeight - 8) - 4;

  // 30/70 overbought/oversold reference lines.
  ctx.strokeStyle = "rgba(148, 163, 184, 0.35)";
  ctx.setLineDash([2, 3]);
  for (const threshold of [30, 70]) {
    ctx.beginPath();
    ctx.moveTo(0, valueToY(threshold));
    ctx.lineTo(paneWidth, valueToY(threshold));
    ctx.stroke();
  }
  ctx.setLineDash([]);

  const candleSlotWidth = paneWidth / closingPrices.length;
  drawAlignedLineSeries(ctx, rsiValues, closingPrices.length, candleSlotWidth, valueToY, RSI_LINE_COLOR);
  ctx.restore();
}

function drawPaneLabel(ctx: CanvasRenderingContext2D, labelText: string): void {
  ctx.fillStyle = AXIS_TEXT_COLOR;
  ctx.font = "10px monospace";
  ctx.fillText(labelText, 6, 12);
}

function drawHorizontalGridLines(
  ctx: CanvasRenderingContext2D,
  paneWidth: number,
  paneHeight: number,
  lineCount: number
): void {
  ctx.save();
  ctx.strokeStyle = GRID_LINE_COLOR;
  ctx.lineWidth = 1;
  for (let index = 1; index < lineCount; index++) {
    const y = (paneHeight / lineCount) * index;
    ctx.beginPath();
    ctx.moveTo(0, y);
    ctx.lineTo(paneWidth, y);
    ctx.stroke();
  }
  ctx.restore();
}
