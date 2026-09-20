import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

// The frontend shipped with no test runner at all, which is why a review found
// eighteen defects that all compiled cleanly and built green. Behaviour here --
// the optimistic-submit state machine, focus and keyboard handling, live event
// reconciliation -- is exactly the kind a type-checker cannot see.
export default defineConfig({
  plugins: [react()],
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
    css: false,
  },
});
