// Mercurius / terminal — maps GoldenLayout componentType strings to the
// real React widget each one renders. Kept as its own module so
// TilingWorkspace.tsx doesn't need to know about every individual widget's
// props/import — one place to register a new tile type.
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

import type { ReactNode } from "react";
import { DomLadderWidget } from "../domLadder/DomLadderWidget";
import { NewsTickerWidget } from "../newsTicker/NewsTickerWidget";
import { PythonHookPanel } from "../pythonHook/PythonHookPanel";
import { ChartTileContainer } from "../chart/ChartTileContainer";

export type WorkspaceWidgetType = "chart" | "domLadder" | "newsTicker" | "pythonHook";

const omsGatewayBaseUrl = import.meta.env.VITE_OMS_GATEWAY_BASE_URL ?? "http://127.0.0.1:8081";
const matchingEngineDomReplayBaseUrl =
  import.meta.env.VITE_MATCHING_ENGINE_DOM_REPLAY_BASE_URL ?? "http://127.0.0.1:9106";
const marketDataBaseUrl = import.meta.env.VITE_MARKET_DATA_BASE_URL ?? "http://127.0.0.1:9103";
const DEFAULT_CLIENT_ACCOUNT_IDENTIFIER = "acct-001";

export const WIDGET_RENDERERS: Record<WorkspaceWidgetType, (instrumentSymbol?: string) => ReactNode> = {
  chart: (instrumentSymbol = "DEMO-EQ") => (
    <ChartTileContainer marketDataBaseUrl={marketDataBaseUrl} instrumentSymbol={instrumentSymbol} />
  ),
  domLadder: (instrumentSymbol = "DEMO-EQ") => (
    <DomLadderWidget
      matchingEngineDomReplayBaseUrl={matchingEngineDomReplayBaseUrl}
      omsGatewayBaseUrl={omsGatewayBaseUrl}
      instrumentSymbol={instrumentSymbol}
      clientAccountIdentifier={DEFAULT_CLIENT_ACCOUNT_IDENTIFIER}
    />
  ),
  newsTicker: () => <NewsTickerWidget />,
  pythonHook: () => <PythonHookPanel />,
};
