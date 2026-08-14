// Mercurius / terminal — GoldenLayout-based tiling workspace.
//
// FEATURES.md §10 "[P2] GoldenLayout-based tiling workspace, saved layouts
// per user". Real `golden-layout` (npm) integration: a `GoldenLayout`
// instance owns a real drag/resize/tab tiling surface; each tile's content
// is a genuine React root mounted into the DOM element GoldenLayout hands
// back from its component factory (the standard, documented way to embed
// a component framework inside GoldenLayout v2 — see
// `registerComponentFactoryFunction` in golden-layout's own types).
// Layout state is saved/restored via `workspaceLayoutPersistence.ts`
// (localStorage-backed, see that module's header comment for why).
//
// Naming convention: long, descriptive camelCase identifiers throughout,
// per project convention.

import { useEffect, useRef } from "react";
import { createRoot, type Root } from "react-dom/client";
import { GoldenLayout, type ComponentContainer, type LayoutConfig } from "golden-layout";
import {
  loadWorkspaceLayoutForUser,
  saveWorkspaceLayoutForUser,
} from "./workspaceLayoutPersistence";
import type { WorkspaceWidgetType } from "./widgetRegistry";
import { WIDGET_RENDERERS } from "./widgetRegistry";

const DEFAULT_LAYOUT_CONFIG: LayoutConfig = {
  root: {
    type: "row",
    content: [
      {
        type: "column",
        width: 65,
        content: [
          { type: "component", componentType: "chart", title: "Chart — DEMO-EQ", height: 65 },
          { type: "component", componentType: "newsTicker", title: "News / Sentiment", height: 35 },
        ],
      },
      {
        type: "column",
        width: 35,
        content: [
          { type: "component", componentType: "domLadder", title: "DOM Ladder — DEMO-EQ" },
          { type: "component", componentType: "pythonHook", title: "Python Hook Sandbox" },
        ],
      },
    ],
  },
};

export type TilingWorkspaceHandle = {
  openWidgetTile: (widgetType: WorkspaceWidgetType, title: string, instrumentSymbol?: string) => void;
};

export function TilingWorkspace(props: { userIdentifier: string; handleRef?: React.MutableRefObject<TilingWorkspaceHandle | null> }) {
  const containerRef = useRef<HTMLDivElement>(null);
  const layoutRef = useRef<GoldenLayout | null>(null);

  useEffect(() => {
    const containerElement = containerRef.current;
    if (!containerElement) return;

    const layout = new GoldenLayout(containerElement);
    layoutRef.current = layout;
    layout.resizeWithContainerAutomatically = true;

    const mountedReactRoots = new Map<ComponentContainer, Root>();

    for (const [widgetType, renderWidget] of Object.entries(WIDGET_RENDERERS)) {
      layout.registerComponentFactoryFunction(widgetType, (container, state) => {
        const reactRoot = createRoot(container.element);
        mountedReactRoots.set(container, reactRoot);
        reactRoot.render(renderWidget((state as { instrumentSymbol?: string } | undefined)?.instrumentSymbol));
        container.on("destroy", () => {
          mountedReactRoots.get(container)?.unmount();
          mountedReactRoots.delete(container);
        });
        return undefined;
      });
    }

    const savedLayoutJson = loadWorkspaceLayoutForUser(props.userIdentifier);
    try {
      layout.loadLayout(savedLayoutJson ? JSON.parse(savedLayoutJson) : DEFAULT_LAYOUT_CONFIG);
    } catch {
      // A corrupted/incompatible saved layout shouldn't brick the
      // workspace — fall back to the hard-coded default.
      layout.loadLayout(DEFAULT_LAYOUT_CONFIG);
    }

    function persistCurrentLayout() {
      try {
        saveWorkspaceLayoutForUser(props.userIdentifier, JSON.stringify(layout.saveLayout()));
      } catch {
        // Best-effort — see workspaceLayoutPersistence.ts's own no-throw
        // contract.
      }
    }
    layout.on("stateChanged", persistCurrentLayout);

    if (props.handleRef) {
      props.handleRef.current = {
        openWidgetTile: (widgetType, title, instrumentSymbol) => {
          layout.addComponent(widgetType, instrumentSymbol ? { instrumentSymbol } : undefined, title);
        },
      };
    }

    return () => {
      layout.off("stateChanged", persistCurrentLayout);
      for (const root of mountedReactRoots.values()) root.unmount();
      mountedReactRoots.clear();
      layout.destroy();
      layoutRef.current = null;
      if (props.handleRef) props.handleRef.current = null;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- re-running this on every render would tear down/rebuild the whole tiling surface; userIdentifier changes are the only thing that should do that.
  }, [props.userIdentifier]);

  return <div ref={containerRef} className="tilingWorkspace" />;
}
