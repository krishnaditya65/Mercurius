import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

// Separate from vite.config.ts (which is tailored for `tauri dev`/`tauri
// build` — fixed port, strictPort, ignoring src-tauri) since Vitest has no
// business touching any of that. jsdom environment is needed for the
// component-adjacent modules (window/localStorage globals used by
// workspaceLayoutPersistence.ts and detachedTileWindowLauncher.ts).
export default defineConfig({
  plugins: [react()],
  test: {
    environment: "jsdom",
    include: ["src/**/*.test.ts", "src/**/*.test.tsx"],
  },
});
