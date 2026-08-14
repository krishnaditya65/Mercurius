// Mercurius / terminal — Pro Desktop Terminal shell. FEATURES.md §10.
//
// Composes: the command bar (top), the GoldenLayout tiling workspace
// (everything below it), and wires a parsed command bar command to
// actually open a new tile in the workspace (`TilingWorkspace`'s
// `openWidgetTile`) — this is the "hotkey system" side of §10's command
// bar item: typing `AAPL DES <GO>` (or `AAPL GP <GO>` for a chart,
// `AAPL DOM <GO>` for a ladder, `AAPL NEWS <GO>` for the news ticker) adds
// a real new tile to the real tiling workspace, not just a toast/log line.
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

import { useRef } from "react";
import { CommandBar } from "./commandBar/CommandBar";
import type { ParsedCommandBarCommand } from "./commandBar/commandBarParser";
import { TilingWorkspace, type TilingWorkspaceHandle } from "./workspace/TilingWorkspace";
import type { WorkspaceWidgetType } from "./workspace/widgetRegistry";
import "./styles/terminal.css";

const DEFAULT_USER_IDENTIFIER = "acct-001";

const VERB_TO_WIDGET_TYPE: Partial<Record<ParsedCommandBarCommand["verb"], WorkspaceWidgetType>> = {
  GP: "chart",
  DOM: "domLadder",
  NEWS: "newsTicker",
};

function App() {
  const workspaceHandleRef = useRef<TilingWorkspaceHandle | null>(null);

  function handleCommandDispatched(command: ParsedCommandBarCommand) {
    const widgetType = VERB_TO_WIDGET_TYPE[command.verb];
    if (!widgetType) {
      // DES/MOD/BLOTTER aren't backed by a distinct tile type in this
      // build — a real build would open an instrument-overview panel
      // (DES) or the order-modification ticket (MOD) here. Documented
      // rather than silently dropped: nothing pretends this opened
      // something it didn't.
      window.alert(
        `"${command.verb}" isn't wired to a workspace tile in this build yet — try GP (chart), DOM (ladder), or NEWS.`
      );
      return;
    }
    workspaceHandleRef.current?.openWidgetTile(
      widgetType,
      `${command.tickerSymbol} — ${widgetType}`,
      command.tickerSymbol
    );
  }

  return (
    <div className="terminalShell">
      <CommandBar onCommandDispatched={handleCommandDispatched} />
      <TilingWorkspace userIdentifier={DEFAULT_USER_IDENTIFIER} handleRef={workspaceHandleRef} />
    </div>
  );
}

export default App;
